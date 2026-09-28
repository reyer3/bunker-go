package tui

import (
	"io"
	"os"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/kittygfx"
	"github.com/reyer3/bunker-go/internal/style"
)

// Model is the interactive shell. Its inbox is a bounded snapshot, not a
// complete conversation history.
type Model struct {
	client Client
	groups []inboxGroup
	// mailExpanded is mail-sender-groups.md's collapse/expand state for
	// the Mail section's sender rows, keyed by senderKey (the lower-
	// cased From address): true once a sender's threads have been
	// expanded. It is never touched by a poll (see the inboxLoadedMsg
	// handler in update.go), so it survives polls; it only ever resets
	// by process exit (a fresh Model on the next run), matching the
	// doc's "resets on quit."
	mailExpanded map[string]bool
	counts       map[core.Channel]map[string]int
	selected     int
	loaded       bool
	loadErr      error
	detail       bool
	reading      bool
	readID       string
	readItem     core.Item
	readErr      error
	readToken    uint64
	// detailScroll is the index of the first visible line within the
	// plain single-item detail view's scrollable body (Subject/From/
	// Channel stay fixed above it, "Esc to inbox · q to quit" fixed
	// below), the same fitInbox-style contract the chat/thread views'
	// own chatScroll/threadScroll give a long body: j/k, arrows,
	// PgUp/PgDown, "G" and the mouse wheel scroll it; it resets to 0
	// whenever a new item is opened (see openItem).
	detailScroll    int
	width           int
	height          int
	polling         bool
	refreshPending  bool
	pollToken       uint64
	composing       bool
	previewing      bool
	sending         bool
	quitConfirm     bool
	draftID         string
	composer        textarea.Model
	attachments     []string
	attaching       bool
	attachInput     string
	previewPlan     core.Plan
	replyErr        error
	replyToken      uint64
	marking         bool
	markLoading     bool
	markConfirm     bool
	markSending     bool
	markID          string
	markPlan        core.Plan
	markErr         error
	markToken       uint64
	activeTab       int
	tabSelected     [numTabs]int
	helpOpen        bool
	glyphs          map[core.Channel]string
	render          *lipgloss.Renderer
	now             func() time.Time
	blurred         bool
	notifyEnabled   bool
	notifyWriter    io.Writer
	tmuxPassthrough bool
	lastNotifyAt    time.Time
	pendingNotify   int

	// Inline images (issue #4, media.go): gfx is the terminal's graphics
	// support, gfxOut the locked writer image uploads go through, media
	// the per-session cache of uploaded images (shared across Model
	// copies), mediaDir where attachment bytes are cached on disk, and
	// viewer the full-size overlay (nil when closed).
	gfx      kittygfx.Mode
	gfxOut   io.Writer
	media    *mediaCache
	mediaDir string
	viewer   *imageViewer

	// Chat view (K5): opening a WhatsApp/Matrix conversation sets
	// detail=true and chatMode=true instead of the plain single-item
	// detail view.
	chatMode    bool
	chatChannel core.Channel
	chatAccount string
	chatThread  string
	chatDraftID string
	// chatName is the header's contact/group display name (K7), resolved
	// once at open time the same way an inbox row's title is (rowTitle):
	// ThreadName, then Subject, then the opening item's sender — never a
	// raw protocol identifier.
	chatName        string
	chatItems       []core.Item
	chatLoading     bool
	chatLoadErr     error
	chatToken       uint64
	chatPresence    core.Presence
	chatPresenceErr error
	chatConfirm     bool
	chatPlan        core.Plan
	chatSending     bool
	chatSendErr     error
	chatReplyToken  uint64
	// chatPreviewPending is true from the moment Enter requests a dry-run
	// preview until chatReplyPreviewMsg resolves (K10): every key is
	// ignored while it is true (mirroring chatSending's own guard), so a
	// repeated Enter typed while the round trip is still in flight can
	// never reissue the preview or bump chatReplyToken — the root cause
	// of the reported "tengo que dar como 4 enters" bug, where an
	// impatient extra Enter discarded the in-flight token before its
	// reply ever landed.
	chatPreviewPending bool
	// chatOptimistic is the K10 optimistic own bubble shown the instant
	// the user confirms a send, before the real send even returns: nil
	// when no optimistic bubble is pending. It is cleared once the
	// reloaded thread (chatSendReloadMsg) contains an item whose ID
	// matches the real send's receipt ID (chatOptimistic.id) — the
	// stored FromMe item then renders in its place, deduplicated.
	chatOptimistic *chatOptimisticMsg
	// emojiSel is the highlighted entry of the emoji completion list
	// (emoji.go), and emojiDismissed the draft Esc dismissed it on: the
	// list stays hidden until the draft changes again.
	emojiSel       int
	emojiDismissed string
	chatTypingAt   time.Time
	chatTypingOn   bool
	// chatScroll is how many lines the chat body's rendered window is
	// scrolled up from the bottom (0 = pinned to the newest message,
	// bottom-aligned just above the composer — the fitInbox-style height
	// contract PgUp/mouse wheel scroll against; see chat_view.go). Measured
	// from the bottom, not the top, so prepending an older loaded page
	// never shifts what is currently on screen.
	chatScroll int

	// Mail thread view (K6): opening a mail item sets detail=true and
	// threadMode=true instead of the plain single-item detail view.
	threadMode     bool
	threadChannel  core.Channel
	threadAccount  string
	threadKey      string
	threadItems    []core.Item
	threadExpanded map[int]bool
	threadSelected int
	threadLoading  bool
	threadLoadErr  error
	threadSeenErr  error
	threadToken    uint64
	// threadSubject is K8's bold title line, resolved once at open time
	// from the opening item's own Subject (the newest item in the
	// conversation, which shares the same Subject once mail threading
	// normalizes "Re: "/"Fwd: " prefixes).
	threadSubject string
	// threadBodies/threadBodyLoading/threadBodyErr back K8's body-fetch
	// cache: mail sync stores headers only (conversation-view.md's
	// reported bug — a synced Item.Body is empty until fetched), so an
	// expanded message's full body is fetched on demand via
	// Client.Read(id, receipt=false) and cached per id so re-expanding an
	// already-fetched message never re-fetches it. Reset to fresh empty
	// maps every time a new thread is opened (see openThread).
	threadBodies      map[string]string
	threadBodyLoading map[string]bool
	threadBodyErr     error
	// threadScroll is the index of the first visible line within the
	// thread's full rendered body (the Subject title is fixed above it,
	// never scrolled). Reset to the selected item's own starting line
	// whenever the selection or its expand state changes, so a long
	// expanded body scrolls within the view (PgUp/PgDown/wheel) instead
	// of pushing the Subject or other messages off screen.
	threadScroll int

	// The full To/Cc/Subject editor r/R/f open (K6).
	mailComposing  bool
	mailAction     string
	mailTargetID   string
	mailChannel    core.Channel
	mailAccount    string
	mailThread     string
	mailTo         textinput.Model
	mailCc         textinput.Model
	mailSubject    textinput.Model
	mailFocus      int
	mailAttachInfo []core.Attachment
	mailPreviewing bool
	mailSending    bool
	mailPlan       core.Plan
	mailSendErr    error
	mailToken      uint64

	// Attachment download (mail view `d`, chat view Ctrl+D): a small overlay on top of
	// the chat/mail thread view (chatMode/threadMode stay true
	// underneath, exactly like markConfirm's overlay on the plain detail
	// view), so Esc returns to whichever view opened it.
	downloadActive       bool
	downloadItemID       string
	downloadAttachments  []core.Attachment
	downloadPicking      bool
	downloadIndex        int
	downloadPath         string
	downloadOverwrite    bool
	downloadSending      bool
	downloadErr          error
	downloadResult       core.DownloadResult
	downloadToken        uint64
	downloadDefaultDirFn func() string
}

// numTabs is "Todo" plus one tab per channelOrder entry.
const numTabs = 1 + 3

func NewModel(client Client) Model {
	return Model{client: client, polling: client != nil, pollToken: 1}
}

// withGlyphs returns a copy of m using the given resolved channel glyphs
// (see internal/style.ResolveGlyphs) instead of the package defaults.
func (m Model) withGlyphs(glyphs map[core.Channel]string) Model {
	m.glyphs = glyphs
	return m
}

// clock returns the model's injectable clock, defaulting to time.Now so
// production code needs no wiring while tests pin a fixed time.
func (m Model) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// resolvedGlyphs returns the model's configured channel glyphs, falling
// back to the package defaults.
func (m Model) resolvedGlyphs() map[core.Channel]string {
	if m.glyphs != nil {
		return m.glyphs
	}
	return style.Glyphs
}

// renderer returns the model's lipgloss renderer, defaulting to one over
// io.Discard (which is never a *os.File, so termenv detects no color
// capability — a safe, deterministic default for anything that renders a
// Model without wiring a real terminal output, including most tests).
func (m Model) renderer() *lipgloss.Renderer {
	if m.render != nil {
		return m.render
	}
	return lipgloss.NewRenderer(io.Discard)
}

func (m Model) Init() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return loadInbox(m.client, m.pollToken)
}

// Run owns the Bubble Tea program but not the RPC connection; the caller
// closes that connection whether the terminal exits cleanly or with an
// error. It builds its lipgloss renderer from output (so color detection,
// including NO_COLOR, matches the real terminal Bubble Tea writes to) and
// loads [render.glyphs] config overrides the same way `bunker render`
// does; a missing/unreadable config keeps the package default glyphs.
func Run(client Client, input io.Reader, output io.Writer) error {
	glyphs := style.Glyphs
	var notify *bool
	if cfg, err := config.LoadDefault(); err == nil {
		glyphs = style.ResolveGlyphs(cfg.Render.Glyphs)
		notify = cfg.Tui.Notify
	}
	model := NewModel(client).withGlyphs(glyphs)
	model.render = lipgloss.NewRenderer(output)
	output = lockOutput(output)
	model.notifyEnabled = resolveNotifyEnabled(notify, os.Getenv)
	model.notifyWriter = output
	model.tmuxPassthrough = os.Getenv("TMUX") != ""
	model.gfx = kittygfx.Detect(os.Getenv)
	if model.gfx == kittygfx.Kitty {
		model.gfxOut = output
		model.media = newMediaCache()
		model.mediaDir = mediaCacheDir()
	}
	_, err := tea.NewProgram(model, tea.WithInput(input), tea.WithOutput(output), tea.WithMouseCellMotion(), tea.WithReportFocus()).Run()
	return err
}
