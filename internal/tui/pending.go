package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/reyer3/bunker-go/internal/core"
)

// The "Pendientes" section sits under "Reuniones" (inbox and sidebar):
// the open to-dos, mine (↗, what I promised) and theirs (↙, what I am
// owed), soonest due first, then the chats awaiting a reply (⏳, where I
// wrote last and nobody answered). Reuniones stays on top because a
// meeting is minutes away and gone within the hour; Pendientes is the
// slower backlog, so it sits right above the footer that names its keys.
//
// p walks the rows (a click selects one, a second click opens it), ↵
// opens the focused row's conversation, D marks the focused to-do done
// and U reopens the last one marked. The section is hidden while there is
// nothing to show and shares the room under the list with Reuniones, so
// neither pushes a conversation off screen.

// PendingClient is the optional Client capability behind the section:
// the RPC client and the TUI's query client implement it. Against a
// daemon without it the section stays hidden.
type PendingClient interface {
	Todos(ctx context.Context, filter core.TodoFilter) ([]core.Todo, error)
	AwaitingReply(ctx context.Context, filter core.AwaitingFilter) ([]core.Awaiting, error)
	CompleteTodo(ctx context.Context, id string) (core.Todo, error)
	ReopenTodo(ctx context.Context, id string) (core.Todo, error)
}

const (
	// pendingMaxRows bounds the section's rows however tall the pane is.
	pendingMaxRows = 4
	// pendingFocusKey walks the section's rows; past the last it leaves.
	pendingFocusKey = "p"
	// pendingDoneKey marks the focused to-do done.
	pendingDoneKey = "D"
	// pendingUndoKey reopens the to-do pendingDoneKey last completed.
	pendingUndoKey = "U"
	// pendingPersonMax caps the person so a long name never leaves the
	// text without room.
	pendingPersonMax = 20
	// pendingFetchLimit bounds what one poll loads of each list.
	pendingFetchLimit = 50
)

// fetchPending loads the open to-dos and the chats awaiting a reply; ok
// is false when the client cannot list them or a call failed, so the
// poll keeps what it showed.
func fetchPending(ctx context.Context, client Client) (todos []core.Todo, awaiting []core.Awaiting, ok bool) {
	lister, can := client.(PendingClient)
	if !can {
		return nil, nil, false
	}
	todos, err := lister.Todos(ctx, core.TodoFilter{Status: core.TodoOpen, Limit: pendingFetchLimit})
	if err != nil {
		return nil, nil, false
	}
	awaiting, err = lister.AwaitingReply(ctx, core.AwaitingFilter{Limit: pendingFetchLimit})
	if err != nil {
		return nil, nil, false
	}
	return todos, awaiting, true
}

// pendingRow is one row of the section: a to-do or a chat awaiting a
// reply (exactly one is set).
type pendingRow struct {
	todo *core.Todo
	wait *core.Awaiting
}

// pendingRows are the section's rows in display order: to-dos (as the
// daemon sorts them, soonest due first), then chats awaiting a reply.
func (m Model) pendingRows() []pendingRow {
	rows := make([]pendingRow, 0, len(m.todos)+len(m.awaiting))
	for i := range m.todos {
		rows = append(rows, pendingRow{todo: &m.todos[i]})
	}
	for i := range m.awaiting {
		rows = append(rows, pendingRow{wait: &m.awaiting[i]})
	}
	return rows
}

// conversation is the item a row opens: its source message, or for a
// to-do recorded without one, nothing (ok false).
func (r pendingRow) conversation() (core.Item, bool) {
	if w := r.wait; w != nil {
		item := core.Item{ID: w.ItemID, Channel: w.Channel, Account: w.Account, Thread: w.Thread, ThreadName: w.Person}
		if w.Channel == core.ChannelMail {
			item.Subject = w.Preview
		}
		return item, true
	}
	t := r.todo
	if t == nil || t.Channel == "" || (t.Thread == "" && t.ItemID == "") {
		return core.Item{}, false
	}
	return core.Item{ID: t.ItemID, Channel: t.Channel, Account: t.Account, Thread: t.Thread, ThreadName: t.Person}, true
}

// todoDueDays is how many days from now's day the to-do is due (negative
// when overdue); ok is false without a due date.
func todoDueDays(t core.Todo, now time.Time) (days int, ok bool) {
	if t.Due.IsZero() {
		return 0, false
	}
	due := t.Due.In(now.Location())
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	day := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, now.Location())
	return int(day.Sub(today).Hours() / 24), true
}

// todoDueLabel is a due date as a row shows it: "vence hoy", "mañana",
// "vie 9" within the week, "15 oct" further out, and "venció ayer" or
// "venció jue 1" once past.
func todoDueLabel(t core.Todo, now time.Time) string {
	days, ok := todoDueDays(t, now)
	if !ok {
		return ""
	}
	due := t.Due.In(now.Location())
	switch {
	case days == 0:
		return "vence hoy"
	case days == -1:
		return "venció ayer"
	case days < 0:
		return "venció " + meetingDay(due, now)
	}
	return meetingDay(due, now)
}

// pendingRowText is one row without styling: "↗ Enviar informe · Ana ·
// vie 9" for a to-do of mine, "↙ …" for one I am owed, and `⏳ Ana · hace
// 4 d · "te paso el…"` for a chat awaiting a reply. The to-do's text and
// the chat's preview are what get cut when the row does not fit.
func pendingRowText(r pendingRow, now time.Time, width int) string {
	var prefix, body, suffix string
	quoted := false
	if w := r.wait; w != nil {
		person := runewidth.Truncate(oneLine(w.Person), pendingPersonMax, "…")
		prefix = fmt.Sprintf("⏳ %s · hace %d d", person, w.Days)
		body = oneLine(w.Preview)
		if body != "" {
			prefix += " · "
			quoted = true
		}
	} else {
		t := r.todo
		prefix = "↗ "
		if t.Direction == core.TodoTheirs {
			prefix = "↙ "
		}
		body = oneLine(t.Text)
		if p := oneLine(t.Person); p != "" {
			suffix += " · " + runewidth.Truncate(p, pendingPersonMax, "…")
		}
		if due := todoDueLabel(*t, now); due != "" {
			suffix += " · " + due
		}
	}
	if width > 0 {
		budget := width - runewidth.StringWidth(prefix+suffix) - 1
		if quoted {
			budget -= 2
		}
		if budget < 1 {
			budget = 1
		}
		body = runewidth.Truncate(body, budget, "…")
	}
	if quoted {
		body = `"` + body + `"`
	}
	return truncatePlain(prefix+body+suffix, max(width-1, 0))
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(safeLine(s)), " ")
}

// pendingSection renders the header and up to avail-1 rows (avail <= 0
// means none; rows beyond the cap become a "+N más" notice). Every row
// line is a hit on its row.
func (m Model) pendingSection(styles rowStyles, width, avail int) (lines []string, hits []inboxHit) {
	list := m.pendingRows()
	if !m.loaded || len(list) == 0 || avail < 2 {
		return nil, nil
	}
	now := m.clock()
	rows := min(len(list), pendingMaxRows, avail-1)
	more := 0
	if rows < len(list) && rows >= 2 {
		rows--
		more = len(list) - rows
	}
	// Keep the focused row on screen: the window slides to it.
	start := 0
	if m.pendingFocused && m.pendingSel >= rows {
		start = min(m.pendingSel-rows+1, len(list)-rows)
	}
	label := "─ Pendientes "
	rule := ""
	if w := widthOrDefault(width) - runewidth.StringWidth(label); w > 0 {
		rule = strings.Repeat("─", w)
	}
	lines = append(lines, styles.dim.Render(truncatePlain(label+rule, widthOrDefault(width))))
	hits = append(hits, inboxHit{kind: hitNone})
	for i := start; i < start+rows; i++ {
		text := " " + pendingRowText(list[i], now, width)
		switch {
		case m.pendingFocused && i == m.pendingSel:
			text = styles.selectedRow.Render(padTo(text, max(width-1, 0)))
		case list[i].todo != nil:
			if days, ok := todoDueDays(*list[i].todo, now); ok && days <= 0 {
				text = styles.title.Render(text)
			}
		}
		lines = append(lines, text)
		hits = append(hits, inboxHit{kind: hitPending, row: i})
	}
	if more > 0 {
		lines = append(lines, styles.dim.Render(truncatePlain(fmt.Sprintf(" +%d más", more), width)))
		hits = append(hits, inboxHit{kind: hitNone})
	}
	return lines, hits
}

// lowerSectionNeed is how many lines a section under the list wants for n
// rows: its header plus up to maxRows rows, or none when empty.
func lowerSectionNeed(n, maxRows int) int {
	if n == 0 {
		return 0
	}
	return 1 + min(n, maxRows)
}

// splitLowerRoom shares avail lines between Reuniones (wants a) and
// Pendientes (wants b): each gets at least a header and a row when both
// fit, half the room each otherwise, and what one does not need goes to
// the other. With room for only one section, Reuniones wins: a meeting is
// due within the hour.
func splitLowerRoom(avail, a, b int) (forMeetings, forPending int) {
	switch {
	case avail < 2:
		return 0, 0
	case a == 0:
		return 0, min(avail, b)
	case b == 0 || avail < 4:
		return min(avail, a), 0
	}
	forMeetings = min(a, max(2, avail/2))
	forPending = min(b, avail-forMeetings)
	forMeetings = min(a, avail-forPending)
	return forMeetings, forPending
}

// lowerSectionsWant is how many lines Reuniones and Pendientes want
// together, before any height budget.
func (m Model) lowerSectionsWant() int {
	if !m.loaded {
		return 0
	}
	return lowerSectionNeed(len(m.activeMeetings()), meetingMaxRows) + lowerSectionNeed(len(m.pendingRows()), pendingMaxRows)
}

// lowerSections renders Reuniones then Pendientes in at most avail lines
// (avail < 0: each whole).
func (m Model) lowerSections(styles rowStyles, width, avail int) (lines []string, hits []inboxHit) {
	a := lowerSectionNeed(len(m.activeMeetings()), meetingMaxRows)
	b := lowerSectionNeed(len(m.pendingRows()), pendingMaxRows)
	forMeetings, forPending := a, b
	if avail >= 0 {
		forMeetings, forPending = splitLowerRoom(avail, a, b)
	}
	lines, hits = m.meetingSection(styles, width, forMeetings)
	pl, ph := m.pendingSection(styles, width, forPending)
	return append(lines, pl...), append(hits, ph...)
}

// clampPendingFocus keeps the focus on an existing row after the rows
// changed, dropping it when the section emptied.
func (m Model) clampPendingFocus() Model {
	n := len(m.pendingRows())
	if n == 0 {
		m.pendingFocused = false
		m.pendingSel = 0
		return m
	}
	m.pendingSel = max(0, min(m.pendingSel, n-1))
	return m
}

// focusNextPending is the p key: the first row, then each next one, then
// out of the section.
func (m Model) focusNextPending() Model {
	n := len(m.pendingRows())
	switch {
	case n == 0:
		return m.withFlash("No hay pendientes")
	case !m.pendingFocused:
		m.pendingFocused, m.pendingSel = true, 0
	case m.pendingSel+1 >= n:
		m.pendingFocused = false
	default:
		m.pendingSel++
	}
	return m
}

// openPendingRow opens row i's conversation the way ↵ on an inbox row
// does (in the sidebar, in herdr's pane).
func (m Model) openPendingRow(i int) (Model, tea.Cmd) {
	rows := m.pendingRows()
	if i < 0 || i >= len(rows) {
		return m, nil
	}
	item, ok := rows[i].conversation()
	if !ok || (m.externalOpen != nil && item.ID == "") {
		return m.withFlash("Este pendiente no viene de una conversación"), nil
	}
	m.pendingFocused = false
	return m.openInboxItem(item)
}

// todoSetMsg is the daemon's answer to marking a to-do done (done) or
// reopening it; index is where the row was, to put a reopened one back.
type todoSetMsg struct {
	todo  core.Todo
	done  bool
	index int
	err   error
}

// todoUndo is the last to-do marked done and where its row was.
type todoUndo struct {
	todo  core.Todo
	index int
}

func setTodoCmd(client PendingClient, todo core.Todo, done bool, index int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
		defer cancel()
		var got core.Todo
		var err error
		if done {
			got, err = client.CompleteTodo(ctx, todo.ID)
		} else {
			got, err = client.ReopenTodo(ctx, todo.ID)
		}
		if err == nil && got.ID == "" {
			got = todo
		}
		return todoSetMsg{todo: got, done: done, index: index, err: err}
	}
}

// pendingDoneReason says why D cannot run now, or "" (the palette dims
// it with the reason).
func (m Model) pendingDoneReason() string {
	if _, ok := m.client.(PendingClient); !ok {
		return "la conexión no lo permite"
	}
	rows := m.pendingRows()
	if !m.pendingFocused || m.pendingSel >= len(rows) {
		return "elige un pendiente con " + pendingFocusKey
	}
	if rows[m.pendingSel].todo == nil {
		return "ese chat espera respuesta: no es un pendiente"
	}
	return ""
}

// completeFocusedTodo is the D key.
func (m Model) completeFocusedTodo() (Model, tea.Cmd) {
	if reason := m.pendingDoneReason(); reason != "" {
		return m.withFlash(strings.ToUpper(reason[:1]) + reason[1:]), nil
	}
	todo := *m.pendingRows()[m.pendingSel].todo
	return m, setTodoCmd(m.client.(PendingClient), todo, true, m.pendingSel)
}

// undoTodoDone is the U key: it reopens the to-do D last completed.
func (m Model) undoTodoDone() (Model, tea.Cmd) {
	client, ok := m.client.(PendingClient)
	if !ok || m.todoUndo == nil {
		return m.withFlash("Nada que deshacer"), nil
	}
	undo := *m.todoUndo
	return m, setTodoCmd(client, undo.todo, false, undo.index)
}

func (m Model) handleTodoSet(msg todoSetMsg) (tea.Model, tea.Cmd) {
	text := oneLine(msg.todo.Text)
	if msg.err != nil {
		if msg.done {
			return m.withFlash("No se pudo marcar el pendiente: " + humanError(msg.err)), nil
		}
		return m.withFlash("No se pudo reabrir el pendiente: " + humanError(msg.err)), nil
	}
	if msg.done {
		kept := m.todos[:0:0]
		for _, t := range m.todos {
			if t.ID != msg.todo.ID {
				kept = append(kept, t)
			}
		}
		m.todos = kept
		m.todoUndo = &todoUndo{todo: msg.todo, index: msg.index}
		m = m.clampPendingFocus()
		return m.withFlash("Pendiente «" + text + "» hecho · " + pendingUndoKey + " deshacer"), nil
	}
	m.todoUndo = nil
	for _, t := range m.todos {
		if t.ID == msg.todo.ID {
			return m.withFlash("Pendiente «" + text + "» reabierto"), nil
		}
	}
	at := max(0, min(msg.index, len(m.todos)))
	todos := append([]core.Todo(nil), m.todos[:at]...)
	todos = append(todos, msg.todo)
	m.todos = append(todos, m.todos[at:]...)
	return m.withFlash("Pendiente «" + text + "» reabierto"), nil
}

// withPendingHint adds "p pendientes" to a hint line while the section
// has rows, and "D hecho" while a to-do is focused, right after "↵ abrir".
func (m Model) withPendingHint(hints []keyHint) []keyHint {
	if len(m.pendingRows()) == 0 {
		return hints
	}
	add := []keyHint{{pendingFocusKey, "pendientes", false}}
	if m.pendingDoneReason() == "" {
		add = append(add, keyHint{pendingDoneKey, "hecho", false})
	}
	at := 0
	for i, h := range hints {
		if h.key == "↵" {
			at = i + 1
			break
		}
	}
	out := make([]keyHint, 0, len(hints)+len(add))
	out = append(out, hints[:at]...)
	out = append(out, add...)
	return append(out, hints[at:]...)
}
