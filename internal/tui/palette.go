package tui

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/reyer3/bunker-go/internal/core"
)

// Command palette (issue #28): Ctrl+K (or F2) lists what the current view
// can do, with its key, plus recent conversations to jump to; "@" turns
// the list into contacts for a new message. Every command runs by
// replaying its own key through update, so the palette can never drift
// from what the key does: the same guards, previews and confirms apply.
//
// Ctrl+K is kill-line in bubbles' textarea and textinput, and a chat's
// composer always has focus, so a chat only opens the palette with F2;
// every other editor (reply, mail editor, filter, picker) keeps both keys
// as text editing and does not open it at all.

const (
	paletteKey    = "ctrl+k"
	paletteAltKey = "f2"
	// paletteMaxWidth keeps the list readable on a wide terminal: the key
	// column stays near the labels instead of at the far edge.
	paletteMaxWidth = 64
	// paletteRecentLimit bounds the recent conversations listed.
	paletteRecentLimit = 8
	// paletteHeaderLines are the title, the query line and the rule;
	// paletteFooterLines the hint line.
	paletteHeaderLines = 3
	paletteFooterLines = 1
)

type commandPalette struct {
	query    string
	selected int
	// The "@" contact list: contactsQuery is the query contacts answer,
	// token discards a stale answer.
	contacts        []core.Contact
	contactsQuery   string
	contactsLoading bool
	contactsErr     error
	token           uint64
}

// paletteContactsMsg is a contacts answer for the palette, kept apart
// from the picker's so neither consumes the other's.
type paletteContactsMsg struct {
	contactsLoadedMsg
	query string
}

type paletteEntryKind int

const (
	paletteCommand paletteEntryKind = iota
	paletteRecent
	paletteContact
)

// paletteEntry is one selectable line. A command carries the key it
// replays; reason, when set, says why it cannot run now (dimmed).
type paletteEntry struct {
	kind    paletteEntryKind
	label   string
	key     string
	msg     tea.KeyMsg
	reason  string
	item    core.Item
	contact core.Contact
	score   int
}

// paletteRune and paletteAlt build the key messages commands replay:
// the same ones Bubble Tea delivers for that key press.
func paletteRune(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }
func paletteAlt(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
}

// canOpenPalette reports whether key opens the palette here. Anywhere a
// text field has focus it is left alone, except F2 in a chat that is not
// in the middle of a send or an action's confirm.
func (m Model) canOpenPalette(key string) bool {
	if key != paletteKey && key != paletteAltKey {
		return false
	}
	if m.openPending() || m.picker != nil || m.filtering || m.composing || m.previewing ||
		m.marking || m.downloadActive || m.viewer != nil || m.mailComposing {
		return false
	}
	if m.chatMode {
		return key == paletteAltKey && m.chatAction == nil && !m.chatConfirm && !m.chatSending && !m.chatPreviewPending
	}
	return true
}

func (m Model) openPalette() Model {
	m.palette = &commandPalette{}
	return m
}

// paletteContext is the view the palette's commands belong to.
func (m Model) paletteContext() string {
	switch {
	case m.chatMode:
		return "chat"
	case m.threadMode:
		return "thread"
	case m.detail:
		return "detail"
	}
	return "inbox"
}

// paletteCommands lists the current view's commands in a fixed order,
// each with the key it replays and, when it cannot run now, why.
func (m Model) paletteCommands() []paletteEntry {
	var out []paletteEntry
	add := func(label, key string, msg tea.KeyMsg, reason string) {
		out = append(out, paletteEntry{kind: paletteCommand, label: label, key: key, msg: msg, reason: reason})
	}
	noClient := ""
	if m.client == nil {
		noClient = "sin conexión con el daemon"
	}
	selected := noClient
	if _, ok := m.selectedItemID(); !ok && selected == "" {
		selected = "nada seleccionado"
	}
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	back := "Volver a la bandeja"
	if m.openID != "" {
		back = "Cerrar"
	}
	ask := func(key string, msg tea.KeyMsg) {
		if m.agentAsk == nil {
			return
		}
		reason := ""
		if m.ringingCall() != nil {
			reason = "suena una llamada: " + key + " la contesta"
		} else if m.asking {
			reason = "ya hay una pregunta en curso"
		} else if id, ok := m.askItemID(); !ok || id == "" {
			reason = "nada seleccionado"
		}
		add("Preguntar a Claude", key, msg, reason)
	}

	switch m.paletteContext() {
	case "inbox":
		add("Nuevo mensaje", "n", paletteRune('n'), noClient)
		add("Buscar contacto", "@", paletteRune('@'), noClient)
		add("Llamar", "c", paletteRune('c'), m.rowCallReason())
		add("Buscar / filtrar", "/", paletteRune('/'), "")
		add("Refrescar", "g", paletteRune('g'), noClient)
		for tab, name := range []string{"Todo", "Mail", "WhatsApp", "Matrix"} {
			reason := ""
			if tab == m.activeTab {
				reason = "ya estás aquí"
			}
			add("Ir a "+name, string(rune('0'+tab)), paletteRune(rune('0'+tab)), reason)
		}
		add("Unirse a la próxima reunión", meetingJoinKey, paletteRune('J'), m.joinReason())
		add("Marcar leído", "m", paletteRune('m'), selected)
		undo := ""
		if len(m.readUndo) == 0 {
			undo = "nada que deshacer"
		}
		add("Deshacer leído", "u", paletteRune('u'), undo)
		add("Responder", "r", paletteRune('r'), selected)
		ask("a", paletteRune('a'))
		add("Ayuda", "?", paletteRune('?'), "")
		add("Salir", "q", paletteRune('q'), "")
	case "detail":
		add("Responder", "r", paletteRune('r'), selected)
		add("Marcar leído", "m", paletteRune('m'), selected)
		add("Refrescar", "g", paletteRune('g'), noClient)
		ask("a", paletteRune('a'))
		add(back, "Esc", esc, "")
		add("Ayuda", "?", paletteRune('?'), "")
		add("Salir", "q", paletteRune('q'), "")
	case "thread":
		reason := ""
		if m.threadSelected < 0 || m.threadSelected >= len(m.threadItems) {
			reason = "el hilo no tiene mensajes cargados"
		}
		add("Responder", "r", paletteRune('r'), reason)
		add("Responder a todos", "R", paletteRune('R'), reason)
		add("Reenviar", "f", paletteRune('f'), reason)
		download := reason
		if download == "" && len(m.threadItems[m.threadSelected].Attachments) == 0 {
			download = "el mensaje no tiene adjuntos"
		}
		add("Descargar adjunto", "d", paletteRune('d'), download)
		add("Abrir adjunto", "Alt+O", paletteAlt('o'), m.openBlocked())
		add("Copiar mensaje", "Alt+Y", paletteAlt('y'), m.copyBlocked())
		add("Modo selección", "F7", tea.KeyMsg{Type: tea.KeyF7}, "")
		ask("a", paletteRune('a'))
		add(back, "Esc", esc, "")
		add("Ayuda", "?", paletteRune('?'), "")
	case "chat":
		actions := ""
		if _, ok := m.client.(MessageClient); !ok {
			actions = "la conexión no lo permite"
		}
		edit, del, react := actions, actions, actions
		if edit == "" {
			if m.chatEditID != "" {
				edit = "ya estás editando"
			} else if _, ok := m.lastOwnMessage(true); !ok {
				edit = "no hay un mensaje tuyo para editar"
			}
			if _, ok := m.lastOwnMessage(false); !ok {
				del = "no hay un mensaje tuyo"
			}
			if _, ok := m.reactionTarget(); !ok {
				react = "no hay mensajes"
			}
		}
		add("Editar último mensaje", "Alt+E", paletteAlt('e'), edit)
		if del == "" && m.ringingCall() != nil {
			del = "suena una llamada: Alt+X la rechaza"
		}
		add("Borrar último mensaje", "Alt+X", paletteAlt('x'), del)
		add("Reaccionar al último mensaje", "Alt++", paletteAlt('+'), react)
		download := ""
		if _, ok := m.chatDownloadCandidate(); !ok {
			download = "no hay adjuntos"
		}
		add("Descargar último adjunto", "Ctrl+D", tea.KeyMsg{Type: tea.KeyCtrlD}, download)
		reason := ""
		if err := m.chatCallBlocked(); err != nil {
			reason = err.Error()
		}
		add("Llamar", "Alt+C", paletteAlt('c'), reason)
		add("Grabar nota de voz", "Alt+V", paletteAlt('v'), m.voiceRecordBlocked())
		add("Reproducir nota de voz", "Alt+P", paletteAlt('p'), m.voicePlayBlocked())
		add("Copiar mensaje", "Alt+Y", paletteAlt('y'), m.copyBlocked())
		add("Abrir adjunto", "Alt+O", paletteAlt('o'), m.openBlocked())
		add("Modo selección", "F7", tea.KeyMsg{Type: tea.KeyF7}, "")
		ask("Alt+A", paletteAlt('a'))
		add(back, "Esc", esc, "")
		add("Ayuda", "F1", tea.KeyMsg{Type: tea.KeyF1}, "")
	}
	// Answering, rejecting and hanging up replay the same keys the call
	// banner shows, and are listed only while they apply.
	// The palette itself is open now, but they run once it is closed.
	base := m
	base.palette = nil
	answer, reject, hangup := base.callKeyNames()
	replay := func(key string) tea.KeyMsg {
		if base.callKeysNeedAlt() {
			return paletteAlt([]rune(key)[0])
		}
		return paletteRune([]rune(key)[0])
	}
	if m.ringingCall() != nil {
		add("Contestar llamada", answer, replay(callAnswerKey), "")
		add("Rechazar llamada", reject, replay(callRejectKey), "")
	}
	if m.liveCall() != nil {
		add("Colgar llamada", hangup, replay(callHangupKey), "")
	}
	return out
}

// paletteMatch scores label against the folded query: a prefix beats a
// word start, which beats a substring, which beats every word somewhere,
// which beats the letters in order (a loose "fuzzy" match). Accents and
// case never matter (core.FoldSearch).
func paletteMatch(label, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	l := core.FoldSearch(label)
	switch {
	case strings.HasPrefix(l, query):
		return 0, true
	case strings.Contains(" "+l, " "+query):
		return 1, true
	case strings.Contains(l, query):
		return 2, true
	}
	words := strings.Fields(query)
	all := len(words) > 0
	for _, w := range words {
		if !strings.Contains(l, w) {
			all = false
			break
		}
	}
	if all {
		return 3, true
	}
	rest := []rune(strings.ReplaceAll(query, " ", ""))
	for _, r := range l {
		if len(rest) > 0 && r == rest[0] {
			rest = rest[1:]
		}
	}
	return 4, len(rest) == 0
}

// contactsMode reports whether the query asks for contacts ("@…"), and
// the contact query after the "@".
func (p *commandPalette) contactsMode() (string, bool) {
	if !strings.HasPrefix(p.query, "@") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(p.query, "@")), true
}

// canJump reports whether the palette may leave the current view for
// another conversation: a "bunker open" pane exists for one only.
func (m Model) canJump() bool { return m.openID == "" }

// paletteEntries is what the palette lists for its query: contacts after
// "@", else the matching commands (runnable ones first, best match
// first) and then the matching recent conversations.
func (m Model) paletteEntries() []paletteEntry {
	p := m.palette
	if p == nil {
		return nil
	}
	if _, ok := p.contactsMode(); ok {
		if !m.canJump() {
			return nil
		}
		out := make([]paletteEntry, len(p.contacts))
		for i, c := range p.contacts {
			name := c.Name
			if name == "" {
				name = c.Address
			}
			out[i] = paletteEntry{kind: paletteContact, label: name, key: c.Account, contact: c}
		}
		return out
	}
	query := core.FoldSearch(strings.TrimSpace(p.query))
	var out []paletteEntry
	for _, e := range m.paletteCommands() {
		if score, ok := paletteMatch(e.label, query); ok {
			e.score = score
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].reason == "") != (out[j].reason == "") {
			return out[i].reason == ""
		}
		return out[i].score < out[j].score
	})
	if !m.canJump() {
		return out
	}
	recent := 0
	for _, g := range m.groups {
		if len(g.items) == 0 || recent == paletteRecentLimit {
			continue
		}
		item := g.newest()
		title, _ := rowTitle(item)
		if _, ok := paletteMatch(strings.Join([]string{title, item.From.Name, item.Subject}, " "), query); !ok {
			continue
		}
		out = append(out, paletteEntry{kind: paletteRecent, label: title, item: item})
		recent++
	}
	return out
}

// reloadPaletteContacts asks for the contacts matching the "@" query
// when it changed since the last answer.
func (m Model) reloadPaletteContacts() (Model, tea.Cmd) {
	p := *m.palette
	query, ok := p.contactsMode()
	if !ok || !m.canJump() || m.client == nil || (query == p.contactsQuery && (p.contacts != nil || p.contactsLoading)) {
		m.palette = &p
		return m, nil
	}
	p.token++
	p.contactsLoading = true
	p.contactsQuery = query
	m.palette = &p
	load := loadContactsCmd(m.client, "", query, p.token)
	return m, func() tea.Msg {
		msg, _ := load().(contactsLoadedMsg)
		return paletteContactsMsg{contactsLoadedMsg: msg, query: query}
	}
}

func (m Model) handlePaletteContacts(msg paletteContactsMsg) (tea.Model, tea.Cmd) {
	if m.palette == nil || msg.token != m.palette.token {
		return m, nil
	}
	p := *m.palette
	p.contactsLoading = false
	p.contacts, p.contactsErr = msg.contacts, msg.err
	if p.contacts == nil && p.contactsErr == nil {
		p.contacts = []core.Contact{}
	}
	if p.selected >= len(p.contacts) {
		p.selected = 0
	}
	m.palette = &p
	return m, nil
}

func (m Model) updatePalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := *m.palette
	entries := m.paletteEntries()
	switch msg.String() {
	case "esc", paletteKey, paletteAltKey:
		m.palette = nil
		return m, nil
	case "enter":
		if p.selected >= 0 && p.selected < len(entries) {
			return m.runPaletteEntry(entries[p.selected])
		}
		return m, nil
	case "up", "ctrl+p", "shift+tab":
		if p.selected > 0 {
			p.selected--
		}
		m.palette = &p
		return m, nil
	case "down", "ctrl+n", "tab":
		if p.selected < len(entries)-1 {
			p.selected++
		}
		m.palette = &p
		return m, nil
	case "backspace":
		r := []rune(p.query)
		if len(r) == 0 {
			return m, nil
		}
		p.query = string(r[:len(r)-1])
	case "ctrl+u":
		p.query = ""
	default:
		switch {
		case msg.Alt:
			return m, nil
		case msg.Type == tea.KeyRunes:
			p.query += string(msg.Runes)
		case msg.Type == tea.KeySpace:
			p.query += " "
		default:
			return m, nil
		}
	}
	p.selected = 0
	m.palette = &p
	return m.reloadPaletteContacts()
}

// runPaletteEntry closes the palette and does what the entry names: a
// command replays its key, a recent conversation opens like ↵ on its
// inbox row, and a contact opens like picking it with "n". A dimmed
// command does nothing: its reason is already on screen.
func (m Model) runPaletteEntry(e paletteEntry) (tea.Model, tea.Cmd) {
	if e.reason != "" {
		return m, nil
	}
	m.palette = nil
	switch e.kind {
	case paletteRecent:
		m, leave := m.leaveConversation()
		next, cmd := m.openInboxItem(e.item)
		return next, tea.Batch(leave, cmd)
	case paletteContact:
		m, leave := m.leaveConversation()
		next, cmd := m.pickContact(e.contact)
		return next, tea.Batch(leave, cmd)
	}
	return m.update(e.msg)
}

// leaveConversation goes back to the inbox from whatever conversation is
// open, the way Esc does, so the palette can open another one.
func (m Model) leaveConversation() (Model, tea.Cmd) {
	switch {
	case m.chatMode:
		return m.leaveChat()
	case m.detail:
		m.detail = false
		m.threadMode = false
		m.reading = false
		m.readErr = nil
		m.readItem = core.Item{}
		m.readToken++
		m.detailScroll = 0
	}
	return m, nil
}

func (m Model) updatePaletteMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		return m.updatePalette(tea.KeyMsg{Type: tea.KeyUp})
	case tea.MouseButtonWheelDown:
		return m.updatePalette(tea.KeyMsg{Type: tea.KeyDown})
	}
	return m, nil
}

func (m Model) paletteWidth() int {
	return min(widthOrDefault(m.width), paletteMaxWidth)
}

// paletteEntryLine renders one entry at width w: the label (and, when
// dimmed, why) on the left, the key on the right.
func (m Model) paletteEntryLine(e paletteEntry, selected bool, w int, styles rowStyles) string {
	marker := "  "
	if selected {
		marker = "▸ "
	}
	label := safeLine(e.label)
	switch e.kind {
	case paletteRecent:
		label = m.resolvedGlyphs()[e.item.Channel] + " " + label
	case paletteContact:
		label = m.resolvedGlyphs()[e.contact.Channel] + " " + label
	}
	if e.reason != "" {
		label += " · " + e.reason
	}
	key := safeLine(e.key)
	keyW := runewidth.StringWidth(key)
	space := w - runewidth.StringWidth(marker) - keyW - 1
	if space < 1 {
		key, keyW = "", 0
		space = w - runewidth.StringWidth(marker)
	}
	line := marker + truncatePlain(label, space)
	if keyW > 0 || selected {
		// Right-align the key; the selected row also pads so its
		// background spans the whole width.
		line = marker + padTo(truncatePlain(label, space), space)
	}
	if keyW > 0 {
		line += " " + key
	}
	switch {
	case selected:
		return styles.selectedRow.Render(line)
	case e.reason != "":
		return styles.dim.Render(line)
	}
	return line
}

func (m Model) paletteView() string {
	p := m.palette
	w := m.paletteWidth()
	styles := m.styles()
	_, contacts := p.contactsMode()
	title := "Comandos"
	if contacts {
		title = "Nuevo mensaje · contactos"
	}
	header := []string{
		styles.title.Render(truncatePlain(title, w)),
		truncatePlain("> "+safeLine(p.query)+"▏", w),
		strings.Repeat("─", w),
	}

	entries := m.paletteEntries()
	var body []string
	selLine := 0
	for i, e := range entries {
		if e.kind == paletteRecent && (i == 0 || entries[i-1].kind != paletteRecent) {
			body = append(body, styles.dim.Render(truncatePlain("Recientes", w)))
		}
		if i == p.selected {
			selLine = len(body)
		}
		body = append(body, m.paletteEntryLine(e, i == p.selected, w, styles))
	}
	if len(entries) == 0 {
		var line string
		switch {
		case contacts && !m.canJump():
			line = "no disponible en este panel"
		case contacts && p.contactsErr != nil:
			line = "Error: " + humanError(p.contactsErr)
		case contacts && p.contactsLoading:
			line = "buscando…"
		default:
			line = "sin coincidencias"
		}
		body = append(body, styles.dim.Render(truncatePlain(line, w)))
	}
	if m.height > 0 {
		rows := max(1, m.height-paletteHeaderLines-paletteFooterLines)
		if len(body) > rows {
			start := 0
			if selLine >= rows {
				start = selLine - rows + 1
			}
			body = body[start : start+rows]
		}
	}

	action := "ejecutar"
	if contacts {
		action = "abrir"
	}
	hints := []keyHint{{"↵", action, true}, {"↑/↓", "elegir", false}}
	if !contacts && m.canJump() {
		hints = append(hints, keyHint{"@", "contacto", false})
	}
	hints = append(hints, keyHint{"Esc", "cerrar", true})
	footer := styles.dim.Render(hintLine(w, hints...))

	lines := append(header, body...)
	lines = append(lines, footer)
	return strings.Join(lines, "\n")
}
