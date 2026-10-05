package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/reyer3/bunker-go/internal/core"
)

// pendingClient adds the to-do and awaiting-reply capabilities to the
// meetings fake, recording every call.
type pendingClient struct {
	*meetingsClient
	todos       []core.Todo
	awaiting    []core.Awaiting
	pendingErr  error
	todoFilters []core.TodoFilter
	waitFilters []core.AwaitingFilter
	completed   []string
	reopened    []string
	setErr      error
}

func (c *pendingClient) Todos(_ context.Context, f core.TodoFilter) ([]core.Todo, error) {
	c.todoFilters = append(c.todoFilters, f)
	return c.todos, c.pendingErr
}

func (c *pendingClient) AwaitingReply(_ context.Context, f core.AwaitingFilter) ([]core.Awaiting, error) {
	c.waitFilters = append(c.waitFilters, f)
	return c.awaiting, c.pendingErr
}

func (c *pendingClient) CompleteTodo(_ context.Context, id string) (core.Todo, error) {
	c.completed = append(c.completed, id)
	return c.setTodo(id, core.TodoDone)
}

func (c *pendingClient) ReopenTodo(_ context.Context, id string) (core.Todo, error) {
	c.reopened = append(c.reopened, id)
	return c.setTodo(id, core.TodoOpen)
}

func (c *pendingClient) setTodo(id string, status core.TodoStatus) (core.Todo, error) {
	if c.setErr != nil {
		return core.Todo{}, c.setErr
	}
	for _, t := range c.todos {
		if t.ID == id {
			t.Status = status
			return t, nil
		}
	}
	return core.Todo{}, core.ErrTodoNotFound
}

func dayAt(days int) time.Time {
	d := meetNow.AddDate(0, 0, days)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, meetNow.Location())
}

func sampleTodos() []core.Todo {
	return []core.Todo{
		{ID: "t1", Text: "Cotización", Direction: core.TodoTheirs, Status: core.TodoOpen, Due: dayAt(0),
			ItemID: "mail:cl:7", Channel: core.ChannelMail, Account: "cl", Thread: "t7", Person: "Proveedor"},
		{ID: "t2", Text: "Enviar informe", Direction: core.TodoMine, Status: core.TodoOpen, Due: dayAt(4),
			ItemID: "whatsapp:personal:A1", Channel: core.ChannelWhatsApp, Account: "personal",
			Thread: "51900000001@s.whatsapp.net", Person: "Ana"},
	}
}

func sampleAwaiting() []core.Awaiting {
	return []core.Awaiting{{ItemID: "matrix:mx:3", Channel: core.ChannelMatrix, Account: "mx", Thread: "!dm:example.org",
		Person: "Luis", Preview: "te paso el informe", Days: 4, Sent: meetNow.AddDate(0, 0, -4)}}
}

// pendingModel is a loaded model whose poll saw the given to-dos, chats
// awaiting a reply and meetings.
func pendingModel(t *testing.T, w, h int, todos []core.Todo, awaiting []core.Awaiting, meetings ...core.UpcomingMeeting) (Model, *pendingClient) {
	t.Helper()
	mail := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t", Subject: "Factura",
		From: core.Address{ID: "x@example.com", Name: "Proveedor"}, Unread: true, Timestamp: meetNow}
	client := &pendingClient{
		meetingsClient: &meetingsClient{
			inboxClient: &inboxClient{items: []core.Item{mail}, counts: map[core.Channel]map[string]int{core.ChannelMail: {"cl": 1}}},
			meetings:    meetings,
		},
		todos: todos, awaiting: awaiting,
	}
	m := NewModel(client)
	m.width, m.height = w, h
	m.now = func() time.Time { return meetNow }
	m.openURL = func(string) error { return nil }
	next, _ := m.Update(loadInbox(client, m.pollToken)())
	return next.(Model), client
}

func TestPendingRowText(t *testing.T) {
	todos := sampleTodos()
	if got := pendingRowText(pendingRow{todo: &todos[1]}, meetNow, 60); got != "↗ Enviar informe · Ana · vie 9" {
		t.Errorf("mine = %q", got)
	}
	if got := pendingRowText(pendingRow{todo: &todos[0]}, meetNow, 60); got != "↙ Cotización · Proveedor · vence hoy" {
		t.Errorf("theirs = %q", got)
	}
	late := core.Todo{Text: "Pagar", Direction: core.TodoMine, Due: dayAt(-1)}
	if got := pendingRowText(pendingRow{todo: &late}, meetNow, 60); got != "↗ Pagar · venció ayer" {
		t.Errorf("overdue = %q", got)
	}
	bare := core.Todo{Text: "Llamar al banco", Direction: core.TodoMine}
	if got := pendingRowText(pendingRow{todo: &bare}, meetNow, 60); got != "↗ Llamar al banco" {
		t.Errorf("no person, no due = %q", got)
	}
	wait := sampleAwaiting()[0]
	if got := pendingRowText(pendingRow{wait: &wait}, meetNow, 60); got != `⏳ Luis · hace 4 d · "te paso el informe"` {
		t.Errorf("awaiting = %q", got)
	}
	// A narrow pane cuts the text and the preview, never the person or the date.
	long := core.Todo{Text: "Revisar el contrato completo con todas sus cláusulas", Direction: core.TodoMine,
		Due: dayAt(4), Person: "Ana"}
	got := pendingRowText(pendingRow{todo: &long}, meetNow, 30)
	if !strings.HasPrefix(got, "↗ Revisar") || !strings.HasSuffix(got, "· Ana · vie 9") || runewidth.StringWidth(got) > 29 {
		t.Errorf("narrow = %q (%d cells)", got, runewidth.StringWidth(got))
	}
	got = pendingRowText(pendingRow{wait: &wait}, meetNow, 28)
	if !strings.HasPrefix(got, "⏳ Luis · hace 4 d · \"te") || !strings.HasSuffix(got, "…\"") || runewidth.StringWidth(got) > 27 {
		t.Errorf("narrow awaiting = %q (%d cells)", got, runewidth.StringWidth(got))
	}
}

func TestInboxShowsPendientesUnderReuniones(t *testing.T) {
	m, client := pendingModel(t, 70, 30, sampleTodos(), sampleAwaiting(),
		upcoming("a", "Revisión semanal", 35*time.Minute, time.Hour, "https://meet.google.com/abc-defg-hij"))
	if len(client.todoFilters) != 1 || client.todoFilters[0].Status != core.TodoOpen {
		t.Fatalf("todo filters = %+v, want the open ones", client.todoFilters)
	}
	if len(client.waitFilters) != 1 {
		t.Fatalf("awaiting filters = %+v", client.waitFilters)
	}
	view := m.View()
	for _, want := range []string{"Pendientes", "↙ Cotización · Proveedor · vence hoy", "↗ Enviar informe · Ana · vie 9",
		`⏳ Luis · hace 4 d`, pendingFocusKey + " pendientes"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Index(view, "Reuniones") > strings.Index(view, "Pendientes") {
		t.Errorf("Pendientes must come under Reuniones:\n%s", view)
	}
	if strings.Index(view, "Matrix") > strings.Index(view, "Pendientes") {
		t.Errorf("Pendientes must come under the sections:\n%s", view)
	}
}

func TestPendientesHiddenWhenEmptyOrUnsupported(t *testing.T) {
	m, _ := pendingModel(t, 70, 24, nil, nil)
	if view := m.View(); strings.Contains(view, "Pendientes") || strings.Contains(view, pendingFocusKey+" pendientes") {
		t.Errorf("empty section is shown:\n%s", view)
	}
	// The meetings-only client cannot list to-dos: no section.
	plain, _, _ := meetingModel(t, 70, 24)
	if strings.Contains(plain.View(), "Pendientes") {
		t.Errorf("section shown without PendingClient:\n%s", plain.View())
	}
}

func TestPendientesKeptWhenARefreshFails(t *testing.T) {
	m, client := pendingModel(t, 70, 24, sampleTodos(), nil)
	client.pendingErr = errors.New("daemon viejo")
	m.pollToken++
	m.polling = true
	next, _ := m.Update(loadInbox(client, m.pollToken)())
	if view := next.(Model).View(); !strings.Contains(view, "Enviar informe") {
		t.Errorf("a failed fetch erased the section:\n%s", view)
	}
}

func TestPendingAndMeetingsShareTheHeightBudget(t *testing.T) {
	var todos []core.Todo
	for i := 0; i < 7; i++ {
		todos = append(todos, core.Todo{ID: string(rune('a' + i)), Text: "Tarea " + string(rune('A'+i)),
			Direction: core.TodoMine, Status: core.TodoOpen})
	}
	var meetings []core.UpcomingMeeting
	for i := 0; i < 6; i++ {
		meetings = append(meetings, upcoming(string(rune('a'+i)), "Reunión "+string(rune('A'+i)),
			time.Duration(i+1)*20*time.Minute, time.Hour, "https://zoom.us/j/1"))
	}
	for _, sidebar := range []bool{false, true} {
		for _, h := range []int{0, 6, 9, 11, 12, 14, 16, 18, 20, 24, 30, 40} {
			m, _ := pendingModel(t, 50, h, todos, sampleAwaiting(), meetings...)
			m.sidebar = sidebar
			lines := strings.Split(m.View(), "\n")
			bare := m
			bare.meetings, bare.todos, bare.awaiting = nil, nil, nil
			limit := max(h, len(strings.Split(bare.View(), "\n")))
			if h > 0 && len(lines) > limit {
				t.Errorf("sidebar=%v height %d: view is %d lines, limit %d:\n%s", sidebar, h, len(lines), limit, strings.Join(lines, "\n"))
			}
			if hits := m.inboxHits(); len(hits) != len(lines) {
				t.Errorf("sidebar=%v height %d: %d hits for %d lines", sidebar, h, len(hits), len(lines))
			}
			view := strings.Join(lines, "\n")
			pending, meeting := strings.Count(view, "↗"), strings.Count(view, "📅")
			if pending > pendingMaxRows || meeting > meetingMaxRows {
				t.Errorf("sidebar=%v height %d: %d pending and %d meeting rows", sidebar, h, pending, meeting)
			}
			if h >= 11 && !sidebar && !(strings.Contains(view, "Mail") && strings.Contains(view, "WhatsApp") && strings.Contains(view, "Matrix")) {
				t.Errorf("height %d: the sections pushed a channel section off:\n%s", h, view)
			}
			// Fair share: when either section shows, a section with
			// something to list is not starved while the other has rows.
			if h >= 24 && !sidebar && (pending == 0 || meeting == 0) {
				t.Errorf("height %d: pending %d, meetings %d, want both:\n%s", h, pending, meeting, view)
			}
		}
	}
	m, _ := pendingModel(t, 50, 60, todos, sampleAwaiting())
	if view := m.View(); !strings.Contains(view, "+5 más") {
		t.Errorf("want a +5 más notice for 8 rows with 3 shown:\n%s", view)
	}
}

func pendingRowLine(t *testing.T, m Model, text string) int {
	t.Helper()
	for i, l := range strings.Split(m.View(), "\n") {
		if strings.Contains(l, text) && (strings.Contains(l, "↗") || strings.Contains(l, "↙") || strings.Contains(l, "⏳")) {
			return i
		}
	}
	t.Fatalf("no pending row for %q:\n%s", text, m.View())
	return -1
}

func TestClickSelectsThenOpensATodoConversation(t *testing.T) {
	m, _ := pendingModel(t, 70, 30, sampleTodos(), sampleAwaiting())
	y := pendingRowLine(t, m, "Enviar informe")
	m, cmd := click(m, y)
	if cmd != nil || m.chatMode || !m.pendingFocused || m.pendingSel != 1 {
		t.Fatalf("first click: chat=%v focused=%v sel=%d, want the row focused only", m.chatMode, m.pendingFocused, m.pendingSel)
	}
	m, cmd = click(m, pendingRowLine(t, m, "Enviar informe"))
	if cmd == nil || !m.chatMode || m.chatThread != "51900000001@s.whatsapp.net" || m.chatAccount != "personal" || m.chatName != "Ana" {
		t.Fatalf("second click: chat=%v thread=%q account=%q name=%q", m.chatMode, m.chatThread, m.chatAccount, m.chatName)
	}
}

func TestEnterOnFocusedRowsOpensTheirThreads(t *testing.T) {
	m, _ := pendingModel(t, 70, 30, sampleTodos(), sampleAwaiting())
	// p walks the rows: Cotización (mail), Enviar informe, the awaiting chat.
	next, _ := m.Update(runeKey(pendingFocusKey))
	m = next.(Model)
	if !m.pendingFocused || m.pendingSel != 0 {
		t.Fatalf("p: focused=%v sel=%d", m.pendingFocused, m.pendingSel)
	}
	opened, cmd := m.Update(enterKey())
	om := opened.(Model)
	if cmd == nil || !om.threadMode || om.threadKey != "t7" || om.threadAccount != "cl" {
		t.Fatalf("enter on a mail to-do: thread=%v key=%q", om.threadMode, om.threadKey)
	}

	for range 2 {
		next, _ = m.Update(runeKey(pendingFocusKey))
		m = next.(Model)
	}
	opened, _ = m.Update(enterKey())
	if om := opened.(Model); !om.chatMode || om.chatThread != "!dm:example.org" || om.chatChannel != core.ChannelMatrix {
		t.Fatalf("enter on an awaiting row: chat=%v thread=%q", om.chatMode, om.chatThread)
	}

	// One more p leaves the section; Esc does too.
	next, _ = m.Update(runeKey(pendingFocusKey))
	if next.(Model).pendingFocused {
		t.Error("p past the last row keeps the focus")
	}
	next, _ = m.Update(escKey)
	if next.(Model).pendingFocused {
		t.Error("esc keeps the focus")
	}
}

func TestTodoWithoutConversationSaysSo(t *testing.T) {
	m, _ := pendingModel(t, 70, 30, []core.Todo{{ID: "x", Text: "Llamar al banco", Direction: core.TodoMine, Status: core.TodoOpen}}, nil)
	next, _ := m.Update(runeKey(pendingFocusKey))
	next, cmd := next.(Model).Update(enterKey())
	if cmd != nil || next.(Model).detail {
		t.Fatal("a to-do without a conversation must not open anything")
	}
	if flash, _ := next.(Model).currentFlash(); flash != "Este pendiente no viene de una conversación" {
		t.Errorf("flash = %q", flash)
	}
}

func TestDoneKeyCompletesAndUndoReopens(t *testing.T) {
	m, client := pendingModel(t, 70, 30, sampleTodos(), sampleAwaiting())
	next, _ := m.Update(runeKey(pendingFocusKey))
	next, _ = next.(Model).Update(runeKey(pendingFocusKey)) // Enviar informe
	next, cmd := next.(Model).Update(runeKey(pendingDoneKey))
	m = runCmd(t, next.(Model), cmd)
	if len(client.completed) != 1 || client.completed[0] != "t2" {
		t.Fatalf("completed = %v, want t2", client.completed)
	}
	if strings.Contains(m.View(), "↗ Enviar informe") {
		t.Errorf("a done to-do stays listed:\n%s", m.View())
	}
	if flash, _ := m.currentFlash(); !strings.Contains(flash, "«Enviar informe» hecho") || !strings.Contains(flash, pendingUndoKey+" deshacer") {
		t.Errorf("flash = %q", flash)
	}

	next, cmd = m.Update(runeKey(pendingUndoKey))
	m = runCmd(t, next.(Model), cmd)
	if len(client.reopened) != 1 || client.reopened[0] != "t2" {
		t.Fatalf("reopened = %v", client.reopened)
	}
	if !strings.Contains(m.View(), "↗ Enviar informe") {
		t.Errorf("a reopened to-do is not listed again:\n%s", m.View())
	}
	next, cmd = m.Update(runeKey(pendingUndoKey))
	if cmd != nil {
		t.Error("a second undo has nothing to reopen")
	}
	if flash, _ := next.(Model).currentFlash(); flash != "Nada que deshacer" {
		t.Errorf("flash = %q", flash)
	}
}

func TestDoneKeyNeedsAFocusedTodo(t *testing.T) {
	m, client := pendingModel(t, 70, 30, sampleTodos(), sampleAwaiting())
	next, cmd := m.Update(runeKey(pendingDoneKey))
	if cmd != nil {
		t.Fatal("done without a focused to-do must not call the daemon")
	}
	if flash, _ := next.(Model).currentFlash(); !strings.Contains(flash, pendingFocusKey) {
		t.Errorf("flash = %q, want a hint to focus with %s", flash, pendingFocusKey)
	}
	m = next.(Model)
	for range 3 {
		next, _ = m.Update(runeKey(pendingFocusKey))
		m = next.(Model)
	}
	next, cmd = m.Update(runeKey(pendingDoneKey))
	if cmd != nil || len(client.completed) != 0 {
		t.Fatal("an awaiting row is not a to-do")
	}
	if flash, _ := next.(Model).currentFlash(); !strings.Contains(flash, "espera respuesta") {
		t.Errorf("flash = %q", flash)
	}
}

func TestDoneFailureIsLoud(t *testing.T) {
	m, client := pendingModel(t, 70, 30, sampleTodos(), nil)
	client.setErr = errors.New("rpc: daemon caído")
	next, _ := m.Update(runeKey(pendingFocusKey))
	next, cmd := next.(Model).Update(runeKey(pendingDoneKey))
	m = runCmd(t, next.(Model), cmd)
	if flash, _ := m.currentFlash(); !strings.HasPrefix(flash, "No se pudo marcar el pendiente:") {
		t.Errorf("flash = %q", flash)
	}
	if !strings.Contains(m.View(), "Cotización") {
		t.Error("a failed completion removed the row")
	}
}

func TestPaletteMarksPendingDone(t *testing.T) {
	m, client := pendingModel(t, 70, 30, sampleTodos(), nil)
	find := func(m Model) *paletteEntry {
		for _, e := range m.paletteCommands() {
			if e.label == "Marcar pendiente hecho" {
				e := e
				return &e
			}
		}
		return nil
	}
	if e := find(m); e == nil || e.reason == "" || e.key != pendingDoneKey {
		t.Fatalf("unfocused palette entry = %+v, want it dimmed", e)
	}
	next, _ := m.Update(runeKey(pendingFocusKey))
	m = next.(Model)
	e := find(m)
	if e == nil || e.reason != "" {
		t.Fatalf("focused palette entry = %+v", e)
	}
	next, cmd := m.Update(e.msg)
	runCmd(t, next.(Model), cmd)
	if len(client.completed) != 1 || client.completed[0] != "t1" {
		t.Fatalf("completed = %v", client.completed)
	}
}

func TestSidebarShowsPendientes(t *testing.T) {
	m, _ := pendingModel(t, 36, 22, sampleTodos(), nil)
	m.sidebar = true
	view := m.View()
	if !strings.Contains(view, "Pendientes") || !strings.Contains(view, "↗ Enviar") || !strings.Contains(view, "Factura") {
		t.Errorf("sidebar view:\n%s", view)
	}
	for _, l := range strings.Split(view, "\n") {
		if runewidth.StringWidth(stripANSI(l)) > m.width {
			t.Errorf("line %q is wider than the pane", l)
		}
	}
}

func enterKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }
