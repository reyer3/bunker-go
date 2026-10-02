package tui

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// mouseFixtureModel returns a loaded, sized model with one unread mail
// conversation from a distinct sender (and empty WhatsApp/Matrix
// sections) in the overview, so every test in this file can rely on the
// same line layout. Mail wraps every conversation under a collapsible
// sender row (mail-sender-groups.md), collapsed by default: a header line
// plus a dim preview line of its newest subject, both the same click
// target:
//
//	0  Mail (1) header       -> hitFocus tab=1
//	1  Mail rule             -> hitFocus tab=1
//	2  sender row (Alice)    -> hitRow row=0
//	3  sender preview        -> hitRow row=0
//	4  WhatsApp (0) header   -> hitFocus tab=2
//	5  WhatsApp rule         -> hitFocus tab=2
//	6  sin pendientes        -> none
//	7  Matrix (0) header     -> hitFocus tab=3
//	8  Matrix rule           -> hitFocus tab=3
//	9  sin pendientes        -> none
//	10 separator             -> none
//	11 footer                -> none
func mouseFixtureModel(client Client) Model {
	m := NewModel(client)
	m.loaded = true
	m.width, m.height = 60, 20
	m.now = func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) }
	m.groups = []inboxGroup{{items: []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Subject: "Hola",
		From:   core.Address{ID: "alice@example.com", Name: "Alice"},
		Unread: true, Timestamp: m.now(),
	}}}}
	m.counts = map[core.Channel]map[string]int{core.ChannelMail: {"cl": 1}}
	return m
}

// TestMouseClickOnRowSelectsIt covers clicking a row that is not yet
// selected: G2 requires that first click to select it, not open it (a
// second click on the now-selected row is what opens/toggles — see
// TestMouseClickOnSelectedRowOpensIt).
func TestMouseClickOnRowSelectsIt(t *testing.T) {
	client := &inboxClient{}
	m := mouseFixtureModel(client)
	// A second, not-yet-selected mail sender (older, so it lands second;
	// a distinct From address so it stays its own collapsed sender row
	// instead of merging with Alice's).
	m.groups = append(m.groups, inboxGroup{items: []core.Item{{
		ID: "mail:cl:2", Channel: core.ChannelMail, Account: "cl", Subject: "Second",
		From:   core.Address{ID: "bob@example.com", Name: "Bob"},
		Unread: true, Timestamp: m.now().Add(-time.Minute),
	}}})
	if m.selected != 0 {
		t.Fatalf("selected = %d, want 0", m.selected)
	}
	// The second sender row follows the first's two lines (header, rule,
	// sender0 = 2-3, sender1 = 4-5).
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

// TestMouseClickOnSelectedThreadRowOpensIt covers clicking an
// already-selected THREAD row (nested under an expanded sender): it
// opens the row, exactly like Enter (K6's mail thread view, not the old
// plain single-item detail). A click on a still-collapsed sender row is
// covered separately by TestMouseClickOnSelectedSenderRowTogglesIt.
func TestMouseClickOnSelectedThreadRowOpensIt(t *testing.T) {
	client := &inboxClient{}
	m := mouseFixtureModel(client)
	m = m.setSenderExpanded(senderKey(m.groups[0].items[0]), true)
	m.selected = 1 // the nested thread row, already expanded

	// Rows: header(0), rule(1), sender(2), thread line1(3), thread line2(4).
	updated, cmd := m.Update(tea.MouseMsg{Y: 3, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	got := updated.(Model)
	if !got.detail || !got.threadMode {
		t.Fatal("clicking the already-selected thread row did not open it")
	}
	if cmd == nil {
		t.Fatal("opening via click did not return a load command")
	}
	if client.readCalls != 0 {
		t.Fatal("read ran synchronously inside Update")
	}
}

// TestMouseClickOnSelectedSenderRowTogglesIt covers clicking an
// already-selected, still-collapsed Mail sender row: it toggles (expands)
// rather than opening anything, since a sender header has no single item
// of its own to open.
func TestMouseClickOnSelectedSenderRowTogglesIt(t *testing.T) {
	client := &inboxClient{}
	m := mouseFixtureModel(client)
	m.selected = 0 // already selected: the fixture's only (collapsed) row

	updated, cmd := m.Update(tea.MouseMsg{Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	got := updated.(Model)
	if got.detail {
		t.Fatal("clicking a sender row must never open a detail view")
	}
	if cmd != nil {
		t.Fatal("toggling a sender row must never return a load command")
	}
	if len(got.visibleRows()) != 2 {
		t.Fatalf("visibleRows after toggling by click = %d, want 2 (sender + its one thread)", len(got.visibleRows()))
	}
	if client.readCalls != 0 {
		t.Fatal("read ran synchronously inside Update")
	}
}

// TestMouseClickOnNotYetSelectedSenderRowTogglesItImmediately covers the
// doc's mouse nuance ("a click on the sender row toggles it"): unlike a
// thread row, a sender row does not need a first click to select it and
// a second to act — a single click toggles it right away, and also
// selects it (so the newly revealed threads are ready for further
// clicks/keys without an extra selection step).
func TestMouseClickOnNotYetSelectedSenderRowTogglesItImmediately(t *testing.T) {
	client := &inboxClient{}
	m := mouseFixtureModel(client)
	m.groups = append(m.groups, inboxGroup{items: []core.Item{{
		ID: "mail:cl:2", Channel: core.ChannelMail, Account: "cl", Subject: "Second",
		From:   core.Address{ID: "bob@example.com", Name: "Bob"},
		Unread: true, Timestamp: m.now().Add(-time.Minute),
	}}})
	// Rows: header(0), rule(1), sender Alice(2-3, selected by default),
	// sender Bob(4-5, not yet selected).
	if m.selected != 0 {
		t.Fatalf("selected = %d, want 0", m.selected)
	}

	updated, cmd := m.Update(tea.MouseMsg{Y: 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	got := updated.(Model)
	if cmd != nil {
		t.Fatal("toggling a sender row must never return a load command")
	}
	if got.selected != 1 {
		t.Fatalf("selected = %d, want 1 (the click also selects Bob's row)", got.selected)
	}
	if len(got.visibleRows()) != 3 {
		t.Fatalf("visibleRows after the click = %d, want 3 (Alice collapsed + Bob expanded with its thread)", len(got.visibleRows()))
	}
}

func TestMouseClickNeverSendsOrMarks(t *testing.T) {
	client := &markClient{}
	m := mouseFixtureModel(client)
	m.selected = 0
	updated, _ := m.Update(tea.MouseMsg{Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
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
	// Force truncation in the Mail section by loading more senders than
	// its fair share of the fixture's height can show, then click the
	// resulting "+N más" line. Each extra item is its own distinct
	// sender (a real address per row), so it stays its own collapsed
	// one-line row instead of merging into Alice's.
	base := m.now()
	for i := 1; i < 10; i++ {
		m.groups = append(m.groups, inboxGroup{items: []core.Item{{
			ID: fmt.Sprintf("mail:cl:extra%d", i), Channel: core.ChannelMail, Account: "cl",
			Subject: "x", From: core.Address{ID: fmt.Sprintf("extra%d@example.com", i)},
			Unread: true, Timestamp: base.Add(time.Duration(-i) * time.Minute),
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
		From:   core.Address{ID: "bob@example.com", Name: "Bob"},
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
	updated, _ := m.Update(tea.MouseMsg{Y: 3, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if got := updated.(Model); got.activeTab != 0 {
		t.Fatalf("a click while in detail view changed activeTab to %d", got.activeTab)
	}
}
