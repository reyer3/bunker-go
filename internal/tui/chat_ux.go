package tui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
)

// Copying text, opening attachments and the selection mode. The panel
// captures the mouse (clicks open rows, the wheel scrolls), which also
// keeps the terminal from selecting text. Three ways out, none of which
// need the composer to lose focus:
//   - selection mode (F7 or Alt+S) hands the mouse back to the terminal
//     until Esc/F7, so it selects natively;
//   - Alt+Y copies a whole message to the system clipboard;
//   - many terminals also select with Shift+drag while the mouse is
//     captured, with no mode at all.

const (
	// selectModeKey and selectModeAltKey toggle the selection mode. F7
	// and Alt+S are unused elsewhere (Alt+A/C/E/L/P/V/X, Alt+↑/↓ and
	// Alt++ are taken, see voice.go, calls.go, chat_actions.go,
	// chat_links.go, chat_select.go and herdr.go).
	selectModeKey    = "f7"
	selectModeAltKey = "alt+s"
	// copyKey copies the selected message (a chat: the one a click
	// selected, else the newest; a mail thread: the selected message).
	copyKey = "alt+y"
	// openFileKey opens a message's attachment with the system opener.
	openFileKey = "alt+o"

	// doubleClickWindow is how close two presses on the same attachment
	// must be to count as a double click.
	doubleClickWindow = 400 * time.Millisecond

	// openMaxBytes bounds an attachment downloaded to be opened: the
	// daemon's own download cap.
	openMaxBytes = 100 << 20

	// osc52MaxBytes bounds the text sent as an OSC 52 sequence: many
	// terminals ignore longer ones, which would look like a copy that
	// silently did nothing.
	osc52MaxBytes = 74994
)

// selectModeBannerText is the status line while the mouse is released.
const selectModeBannerText = "Modo selección: selecciona con el ratón · Esc/F7 volver"

// selectModeToggle handles the selection mode keys before any view sees
// them: F7/Alt+S toggle the mode, and Esc leaves it. It reports false for
// any other key (or Esc outside the mode).
func (m Model) selectModeToggle(key string) (Model, tea.Cmd, bool) {
	switch {
	case key == selectModeKey || key == selectModeAltKey:
	case key == "esc" && m.selectMode:
	default:
		return m, nil, false
	}
	m.selectMode = !m.selectMode
	if m.selectMode {
		return m, tea.DisableMouse, true
	}
	return m, tea.EnableMouseCellMotion, true
}

// withSelectBanner draws the selection mode's line over the view's last
// line, like the call banner does, so no other line moves.
func (m Model) withSelectBanner(view string) string {
	if !m.selectMode {
		return view
	}
	line := selectModeBannerText
	if m.width > 0 {
		line = runewidth.Truncate(line, m.width, "…")
	}
	lines := strings.Split(view, "\n")
	lines[len(lines)-1] = m.renderer().NewStyle().Bold(true).Reverse(true).Render(line)
	return strings.Join(lines, "\n")
}

// noteUIError shows a failed action where the current view reports
// errors: the chat's error line, else the status notice.
func (m Model) noteUIError(err error) Model {
	if m.chatMode {
		m.mediaErr = err
		return m
	}
	return m.withFlash("Error: " + humanError(err))
}

// registerClick records a left press on key ("" for a press on nothing
// that double-clicks) and reports whether it completes a double click: a
// second press on the same key within doubleClickWindow of the first, by
// the model clock. A double click consumes both presses, so a triple
// click is a double click plus a fresh single one.
func (m Model) registerClick(key string) (Model, bool) {
	now := m.clock()
	if key != "" && key == m.lastClickKey && !m.lastClickAt.IsZero() && now.Sub(m.lastClickAt) <= doubleClickWindow {
		m.lastClickKey, m.lastClickAt = "", time.Time{}
		return m, true
	}
	m.lastClickKey, m.lastClickAt = key, now
	return m, false
}

// textCopier puts text on the system clipboard: through wl-copy, xclip
// or xsel when one is installed (external processes, no CGO), else as an
// OSC 52 sequence the terminal itself turns into a clipboard write, which
// also works over SSH and tmux (with set-clipboard on).
type textCopier struct {
	getenv   func(string) string
	lookPath func(string) (string, error)
	// run executes argv with stdin as its input.
	run func(ctx context.Context, argv []string, stdin string) error
	// osc is where the OSC 52 fallback is written (the terminal).
	osc  io.Writer
	tmux bool
}

func newTextCopier(getenv func(string) string, osc io.Writer, tmux bool) textCopier {
	return textCopier{
		getenv:   getenv,
		lookPath: exec.LookPath,
		run: func(ctx context.Context, argv []string, stdin string) error {
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			cmd.Stdin = strings.NewReader(stdin)
			// No stdout/stderr pipes: xclip and wl-copy fork a server
			// that keeps them open, which would block Wait.
			return cmd.Run()
		},
		osc:  osc,
		tmux: tmux,
	}
}

// clipboardTools lists the clipboard writers to try, most specific first.
func (c textCopier) clipboardTools() [][]string {
	var tools [][]string
	if c.getenv("WAYLAND_DISPLAY") != "" {
		tools = append(tools, []string{"wl-copy"})
	}
	if c.getenv("DISPLAY") != "" {
		tools = append(tools, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "--clipboard", "--input"})
	}
	return tools
}

// Copy writes text to the clipboard and says how ("wl-copy", "OSC 52").
// A tool that is installed but fails is not the end: OSC 52 is tried
// next, and only when that is unavailable too is the failure reported.
func (c textCopier) Copy(ctx context.Context, text string) (string, error) {
	var toolErr error
	for _, argv := range c.clipboardTools() {
		if _, err := c.lookPath(argv[0]); err != nil {
			continue
		}
		if err := c.run(ctx, argv, text); err != nil {
			toolErr = fmt.Errorf("%s: %w", argv[0], err)
			continue
		}
		return argv[0], nil
	}
	if c.osc == nil || len(text) > osc52MaxBytes {
		switch {
		case toolErr != nil:
			return "", toolErr
		case c.osc != nil:
			return "", fmt.Errorf("el texto es demasiado largo para copiarlo por OSC 52 (%d bytes); instala wl-copy, xclip o xsel", len(text))
		}
		return "", errors.New("no hay portapapeles: instala wl-copy, xclip o xsel")
	}
	seq := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"
	if c.tmux {
		seq = tmuxPassthrough(seq)
	}
	if _, err := io.WriteString(c.osc, seq); err != nil {
		return "", fmt.Errorf("OSC 52: %w", err)
	}
	return "OSC 52", nil
}

// textCopier is the model's clipboard writer: the injected one in tests,
// else one over the real environment and terminal.
func (m Model) textCopier() textCopier {
	if m.copier != nil {
		return *m.copier
	}
	return newTextCopier(m.envFunc(), m.notifyWriter, m.tmuxPassthrough)
}

// copyDoneMsg reports a finished copy.
type copyDoneMsg struct {
	via string
	err error
}

// copyTextCmd copies text off the update loop.
func (m Model) copyTextCmd(text string) tea.Cmd {
	c := m.textCopier()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		via, err := c.Copy(ctx, text)
		return copyDoneMsg{via: via, err: err}
	}
}

func (m Model) handleCopyDone(msg copyDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.noteUIError(fmt.Errorf("no se pudo copiar: %w", msg.err)), nil
	}
	m.mediaErr = nil
	if msg.via == "OSC 52" {
		// The terminal does the write, and not every one allows it.
		return m.withFlash("Copiado (OSC 52: si no pega, activa su portapapeles)"), nil
	}
	return m.withFlash("Copiado"), nil
}

// messageCopyText is what copying item puts on the clipboard: its text
// as shown (control sequences stripped), else its attachments' names.
func messageCopyText(item core.Item) (string, error) {
	if item.Deleted {
		return "", errors.New("el mensaje fue eliminado")
	}
	if text := strings.TrimSpace(sanitizeTerminalText(item.Body)); text != "" {
		return text, nil
	}
	var names []string
	for _, a := range item.Attachments {
		if a.Voice {
			continue
		}
		if name := strings.TrimSpace(safeLine(a.Name)); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", errors.New("el mensaje no tiene texto que copiar")
	}
	return strings.Join(names, "\n"), nil
}

// chatCopyTarget is the message Alt+Y copies: the one a click selected,
// else the newest that is not deleted.
func (m Model) chatCopyTarget() (core.Item, bool) {
	if m.chatFocus != "" {
		for _, it := range m.chatItems {
			if it.ID == m.chatFocus {
				return it, true
			}
		}
	}
	for i := len(m.chatItems) - 1; i >= 0; i-- {
		if !m.chatItems[i].Deleted {
			return m.chatItems[i], true
		}
	}
	return core.Item{}, false
}

// copySelected copies the open view's message (the key Alt+Y in a chat,
// a mail thread and the plain detail view).
func (m Model) copySelected() (Model, tea.Cmd) {
	var item core.Item
	switch {
	case m.chatMode:
		it, ok := m.chatCopyTarget()
		if !ok {
			return m.noteUIError(errors.New("no hay mensajes que copiar")), nil
		}
		item = it
	case m.threadMode:
		if m.threadSelected < 0 || m.threadSelected >= len(m.threadItems) {
			return m.noteUIError(errors.New("no hay mensajes que copiar")), nil
		}
		item = m.threadItems[m.threadSelected]
		if _, ok := m.threadBodies[item.ID]; !ok && item.Body == "" && m.threadBodyLoading[item.ID] {
			return m.noteUIError(errors.New("el mensaje todavía se está cargando")), nil
		}
		item.Body = m.threadItemBody(item)
	case m.detail && !m.reading && m.readErr == nil:
		item = m.readItem
	default:
		return m, nil
	}
	text, err := messageCopyText(item)
	if err != nil {
		return m.noteUIError(err), nil
	}
	return m, m.copyTextCmd(text)
}

// fileLineTags marks, parallel to bubble, the lines that name one of
// item's file attachments with its media key and leaves the rest "": a
// double click on such a line opens the file. Lines are found by their
// content, as voiceLineTags does; an item without an ID is never tagged.
func fileLineTags(item core.Item, bubble []string, width int) []string {
	tags := make([]string, len(bubble))
	if item.ID == "" || item.Deleted {
		return tags
	}
	bubbleWidth := chatBubbleWidth(width)
	next := 0
	for i, a := range item.Attachments {
		if a.Voice {
			continue
		}
		text := runewidth.Truncate(fmt.Sprintf("📎 %s (%d bytes)", safeLine(a.Name), a.Size), bubbleWidth, "…")
		for ; next < len(bubble); next++ {
			if strings.Contains(bubble[next], text) {
				tags[next] = mediaKey(item.ID, i)
				next++
				break
			}
		}
	}
	return tags
}

// chatLineMetaAt describes the body line shown on screen row y of the
// chat view.
func (m Model) chatLineMetaAt(y int) chatLineMeta {
	header := len(m.chatHeaderLines())
	_, meta := m.chatBodyMeta()
	start, end, _ := windowBounds(len(meta), m.chatScroll, m.chatScrollBudget())
	row := y - header
	if row < 0 || start+row >= end {
		return chatLineMeta{}
	}
	return meta[start+row]
}

// chatOpenTarget is the attachment Alt+O opens: the first file of the
// message a click selected, else of the newest message that has one.
func (m Model) chatOpenTarget() (itemID string, index int, a core.Attachment, ok bool) {
	pick := func(it core.Item) bool {
		if it.Deleted {
			return false
		}
		for i, att := range it.Attachments {
			if !att.Voice {
				itemID, index, a, ok = it.ID, i, att, true
				return true
			}
		}
		return false
	}
	for _, it := range m.chatItems {
		if it.ID == m.chatFocus && m.chatFocus != "" && pick(it) {
			return
		}
	}
	for i := len(m.chatItems) - 1; i >= 0; i-- {
		if pick(m.chatItems[i]) {
			return
		}
	}
	return "", 0, core.Attachment{}, false
}

// errNoOpener names what to install when the file opener is missing.
func errNoOpener(name string) error {
	return fmt.Errorf("falta %s para abrir adjuntos (instálalo o define BUNKER_OPEN_FILE)", name)
}

// openerArgv is the command that opens a file: BUNKER_OPEN_FILE (the path
// is appended as the last argument), else xdg-open.
func openerArgv(getenv func(string) string) []string {
	if custom := strings.Fields(getenv("BUNKER_OPEN_FILE")); len(custom) > 0 {
		return custom
	}
	return []string{"xdg-open"}
}

// startDetached runs argv without waiting for it and apart from the
// panel: an opened document or viewer is its own window and must outlive
// the panel, so it is neither attached to the terminal nor killed with
// the voice helpers.
func startDetached(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return errNoOpener(argv[0])
		}
		return fmt.Errorf("%s: %w", argv[0], err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// openAttachMsg carries a downloaded attachment, ready to open.
type openAttachMsg struct {
	name string
	path string
	err  error
}

// startOpenAttachment downloads an attachment into the media cache (the
// path images and voice notes use) and then opens it with the system
// opener. A missing opener is reported before downloading anything.
func (m Model) startOpenAttachment(itemID string, index int, a core.Attachment) (Model, tea.Cmd) {
	if m.client == nil {
		return m.noteUIError(errors.New("sin conexión con el daemon")), nil
	}
	if m.openFile == nil {
		argv := openerArgv(m.envFunc())
		if _, err := exec.LookPath(argv[0]); err != nil {
			return m.noteUIError(errNoOpener(argv[0])), nil
		}
	}
	name := strings.TrimSpace(safeLine(a.Name))
	if name == "" {
		name = "adjunto"
	}
	m.mediaErr = nil
	m = m.withFlash("Abriendo " + name + "…")
	client, dir := m.client, m.mediaDir
	if dir == "" {
		dir = mediaCacheDir()
	}
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		path, err := cachedMedia(ctx, client, dir, itemID, index, a.Name, openMaxBytes)
		return openAttachMsg{name: name, path: path, err: err}
	}
}

func (m Model) handleOpenAttach(msg openAttachMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.noteUIError(fmt.Errorf("no se pudo descargar %s: %w", msg.name, msg.err)), nil
	}
	open := m.openFile
	if open == nil {
		open = startDetached
	}
	if err := open(append(openerArgv(m.envFunc()), msg.path)); err != nil {
		return m.noteUIError(err), nil
	}
	return m.withFlash("Abriendo " + msg.name + "…"), nil
}

// openChatAttachment opens the chat attachment at media key.
func (m Model) openChatAttachment(key string) (Model, tea.Cmd) {
	itemID, index, a, ok := m.chatAttachment(key)
	if !ok {
		return m, nil
	}
	return m.startOpenAttachment(itemID, index, a)
}

// openNewestAttachment is Alt+O in a chat or a mail thread.
func (m Model) openNewestAttachment() (Model, tea.Cmd) {
	if m.threadMode {
		if m.threadSelected >= 0 && m.threadSelected < len(m.threadItems) {
			item := m.threadItems[m.threadSelected]
			if len(item.Attachments) > 0 {
				return m.startOpenAttachment(item.ID, 0, item.Attachments[0])
			}
		}
		return m.noteUIError(errors.New("el mensaje no tiene adjuntos que abrir")), nil
	}
	itemID, index, a, ok := m.chatOpenTarget()
	if !ok {
		return m.noteUIError(errors.New("no hay adjuntos que abrir en este chat")), nil
	}
	return m.startOpenAttachment(itemID, index, a)
}

// openBlocked says why Abrir adjunto cannot run now, or "".
func (m Model) openBlocked() string {
	if m.client == nil {
		return "sin conexión con el daemon"
	}
	if m.threadMode {
		if m.threadSelected < 0 || m.threadSelected >= len(m.threadItems) || len(m.threadItems[m.threadSelected].Attachments) == 0 {
			return "el mensaje no tiene adjuntos"
		}
	} else if _, _, _, ok := m.chatOpenTarget(); !ok {
		return "no hay adjuntos"
	}
	if m.openFile == nil {
		if argv := openerArgv(m.envFunc()); argv != nil {
			if _, err := exec.LookPath(argv[0]); err != nil {
				return errNoOpener(argv[0]).Error()
			}
		}
	}
	return ""
}

// copyBlocked says why Copiar mensaje cannot run now, or "".
func (m Model) copyBlocked() string {
	switch {
	case m.chatMode:
		if _, ok := m.chatCopyTarget(); !ok {
			return "no hay mensajes"
		}
	case m.threadMode:
		if m.threadSelected < 0 || m.threadSelected >= len(m.threadItems) {
			return "el hilo no tiene mensajes cargados"
		}
	case m.detail:
		if m.reading || m.readErr != nil {
			return "el mensaje no está cargado"
		}
	}
	return ""
}

// chatIsNewConversation is true for a chat opened from the contact
// picker that has no message to reply to yet.
func (m Model) chatIsNewConversation() bool {
	return m.chatDraftID == "" && m.chatNewTo != ""
}

// chatSendsOnOneEnter reports whether Enter sends the draft without a
// second confirming Enter: only a plain text message in an existing
// conversation. Attachments, voice notes, a new conversation and edits
// keep the explicit preview and confirm, as does confirm_chat_send.
func (m Model) chatSendsOnOneEnter() bool {
	return !m.confirmChatSend &&
		m.chatEditID == "" &&
		len(m.chatAttachments) == 0 &&
		!m.chatVoice &&
		!m.chatIsNewConversation()
}
