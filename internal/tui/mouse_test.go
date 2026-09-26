package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// mouseFixtureModel returns a loaded, sized model with one unread mail
// conversation (and empty WhatsApp/Matrix sections) in the overview, so
// every test in this file can rely on the same line layout:
//
//	0  Mail (1) header      -> hitFocus tab=1
//	1  Mail rule            -> hitFocus tab=1
//	2  row line 1 (title)   -> hitRow row=0
//	3  row line 2 (preview) -> hitRow row=0
//	4  WhatsApp (0) header  -> hitFocus tab=2
//	5  WhatsApp rule        -> hitFocus tab=2
//	6  sin pendientes       -> none
//	7  Matrix (0) header    -> hitFocus tab=3
//	8  Matrix rule          -> hitFocus tab=3
//	9  sin pendientes       -> none
//	10 separator            -> none
//	11 footer               -> none
func mouseFixtureModel(client Client) Model {
	m := NewModel(client)
	m.loaded = true
	m.width, m.height = 60, 20
	m.now = func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) }
	m.groups = []inboxGroup{{items: []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Subject: "Hola",
		Unread: true, Timestamp: m.now(),
	}}}}
	m.counts = map[core.Channel]map[string]int{core.ChannelMail: {"cl": 1}}
	return m
}

// TestMouseClickOnRowSelectsIt covers clicking a row that is not yet
// selected: G2 requires that first click to select it, not open it (a
// second click on the now-selected row is what opens — see
// TestMouseClickOnSelectedRowOpensIt).
func TestMouseClickOnRowSelectsIt(t *testing.T) {
	client := &inboxClient{}
	m := mouseFixtureModel(client)
	// A second, not-yet-selected mail row (older, so it lands second).
	m.groups = append(m.groups, inboxGroup{items: []core.Item{{
		ID: "mail:cl:2", Channel: core.ChannelMail, Account: "cl", Subject: "Second",
		Unread: true, Timestamp: m.now().Add(-time.Minute),
	}}})
	if m.selected != 0 {
		t.Fatalf("selected = %d, want 0", m.selected)
	}
	// The second row's line 1 sits right after the first row's two lines
	// (header, rule, row0 line1, row0 line2, row1 line1 = index 4).
	updated, cmd := m.Update(tea.MouseMsg{Y: 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	got := updated.(Model)
	if got.selected != 1 {
		t.Fatalf("selected = %d, want 1 (clicking a non-selected row selects it)", got.selected)
	}
	if cmd != nil {
		t.Fatal("selecting a not-yet-selected row must not also open it")
	}
	if client.readCalls != 0 {
		t.Fatal("click sent a synchronous read")
	}
}

func TestMouseClickOnSelectedRowOpensIt(t *testing.T) {
	client := &inboxClient{readResult: core.Item{ID: "mail:cl:1", Body: "hi"}}
	m := mouseFixtureModel(client)
	m.selected = 0 // already selected: the fixture's only row

	updated, cmd := m.Update(tea.MouseMsg{Y: 3, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	got := updated.(Model)
	if !got.detail || !got.reading {
		t.Fatal("clicking the already-selected row did not open it")
	}
	if cmd == nil {
		t.Fatal("opening via click did not return a read command")
	}
	if client.readCalls != 0 {
		t.Fatal("read ran synchronously inside Update")
	}
}

func TestMouseClickNeverSendsOrMarks(t *testing.T) {
	client := &markClient{}
	m := mouseFixtureModel(client)
	m.selected = 0
	updated, _ := m.Update(tea.MouseMsg{Y: 3, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if got := updated.(Model); got.marking {
		t.Fatal("a click started the mark-read flow")
	}
	if len(client.calls) != 0 {
		t.Fatalf("click made an Organize/Reply call: %+v", client.calls)
	}
}

func TestMouseClickOnSectionHeaderFocusesIt(t *testing.T) {
	client := &inboxClient{}
	m := mouseFixtureModel(client)
	updated, _ := m.Update(tea.MouseMsg{Y: 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}) // WhatsApp header
	got := updated.(Model)
	if got.activeTab != 2 {
		t.Fatalf("activeTab = %d, want 2 (WhatsApp)", got.activeTab)
	}
	if ch, ok := got.currentChannelFilter(); !ok || ch != core.ChannelWhatsApp {
		t.Fatalf("currentChannelFilter = %v/%v, want WhatsApp", ch, ok)
	}
}

func TestMouseClickOnMoreLineFocusesItsSection(t *testing.T) {
	client := &inboxClient{}
	m := mouseFixtureModel(client)
	// Force truncation in the Mail section by loading more conversations
	// than its fair share of the fixture's height can show, then click
	// the resulting "+N más" line.
	base := m.now()
	for i := 1; i < 10; i++ {
		m.groups = append(m.groups, inboxGroup{items: []core.Item{{
			ID: "mail:cl:extra", Channel: core.ChannelMail, Account: "cl",
			Subject: "x", Unread: true, Timestamp: base.Add(time.Duration(-i) * time.Minute),
		}}})
	}

	hits := m.inboxHits()
	moreY := -1
	for y, h := range hits {
		if h.kind == hitFocus && h.tab == 1 {
			moreY = y
		}
	}
	if moreY < 2 {
		t.Fatalf("fixture did not produce a Mail hitFocus line beyond the header (hits=%+v)", hits)
	}

	updated, _ := m.Update(tea.MouseMsg{Y: moreY, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if got := updated.(Model); got.activeTab != 1 {
		t.Fatalf("activeTab = %d, want 1 (Mail, focused by the +N más click)", got.activeTab)
	}
}

func TestMouseWheelMovesSelectionWithoutReading(t *testing.T) {
	client := &inboxClient{}
	m := mouseFixtureModel(client)
	m.groups = append(m.groups, inboxGroup{items: []core.Item{{
		ID: "mail:cl:2", Channel: core.ChannelMail, Account: "cl", Subject: "Second",
		Unread: true, Timestamp: m.now().Add(-time.Minute),
	}}})

	updated, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	got := updated.(Model)
	if got.selected != 1 {
		t.Fatalf("selected after wheel down = %d, want 1", got.selected)
	}
	if cmd != nil || client.readCalls != 0 {
		t.Fatal("wheel scroll must not read anything")
	}

	updated, _ = got.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if got2 := updated.(Model); got2.selected != 0 {
		t.Fatalf("selected after wheel up = %d, want 0", got2.selected)
	}
}

func TestMouseIgnoredWhileComposingOrInDetail(t *testing.T) {
	client := &inboxClient{}
	m := mouseFixtureModel(client)
	m.detail = true
	m.readItem = core.Item{ID: "mail:cl:1"}
	updated, _ := m.Update(tea.MouseMsg{Y: 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if got := updated.(Model); got.activeTab != 0 {
		t.Fatalf("a click while in detail view changed activeTab to %d", got.activeTab)
	}
}
