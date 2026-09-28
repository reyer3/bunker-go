package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// senderFixtureModel builds a loaded, Mail-only overview model with one
// sender ("bob@example.com") owning two threads, newest first. Every
// fixture in this file starts from it, so the row layout is fixed:
//
//	row 0: the sender header (collapsed by default)
//	(once expanded) row 1: thread "t1" (newest), row 2: thread "t2"
func senderFixtureModel(client Client) Model {
	m := NewModel(client)
	m.loaded = true
	base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return base }
	m.groups = []inboxGroup{
		mailThread("mail:cl:t1", "cl", "t1", "bob@example.com", "Bob", base),
		mailThread("mail:cl:t2", "cl", "t2", "bob@example.com", "Bob", base.Add(-time.Hour)),
	}
	m.counts = map[core.Channel]map[string]int{core.ChannelMail: {"cl": 2}}
	return m
}

func pressSpecial(m Model, t tea.KeyType) Model {
	updated, _ := m.Update(tea.KeyMsg{Type: t})
	return updated.(Model)
}

// TestSenderRowCollapsedByDefaultWrapsBothThreads covers the doc's
// "Collapsed by default" and "a sender with exactly one thread still
// gets a sender row" (here: two threads collapse into exactly one row).
func TestSenderRowCollapsedByDefaultWrapsBothThreads(t *testing.T) {
	m := senderFixtureModel(&inboxClient{})
	rows := m.visibleRows()
	if len(rows) != 1 {
		t.Fatalf("visibleRows = %d, want 1 (the collapsed sender row)", len(rows))
	}
	if rows[0].kind != navSender || rows[0].sender.key != "bob@example.com" {
		t.Fatalf("rows[0] = %+v, want the bob@example.com sender row", rows[0])
	}
}

// TestEnterTogglesSenderRowExpandThenCollapse covers "Enter ... on a
// sender row toggles it" both directions, and that the two threads
// appear newest-first, indented, once expanded.
func TestEnterTogglesSenderRowExpandThenCollapse(t *testing.T) {
	m := senderFixtureModel(&inboxClient{})
	m = pressSpecial(m, tea.KeyEnter)
	rows := m.visibleRows()
	if len(rows) != 3 {
		t.Fatalf("visibleRows after Enter = %d, want 3 (sender + 2 threads)", len(rows))
	}
	if rows[1].kind != navThread || rows[1].thread.items[0].ID != "mail:cl:t1" || !rows[1].indent {
		t.Fatalf("rows[1] = %+v, want indented thread t1 (newest)", rows[1])
	}
	if rows[2].kind != navThread || rows[2].thread.items[0].ID != "mail:cl:t2" || !rows[2].indent {
		t.Fatalf("rows[2] = %+v, want indented thread t2", rows[2])
	}
	if rows[1].senderKey != "bob@example.com" {
		t.Fatalf("rows[1].senderKey = %q, want bob@example.com", rows[1].senderKey)
	}

	m = pressSpecial(m, tea.KeyEnter) // selection is still on the sender row: toggles back closed
	if rows := m.visibleRows(); len(rows) != 1 {
		t.Fatalf("visibleRows after second Enter = %d, want 1 (collapsed again)", len(rows))
	}
}

// TestRightArrowTogglesSenderRowButNeverOpensAThread covers "→ ... on a
// sender row toggles it" and the distinct rule that → (unlike Enter)
// never opens a thread row.
func TestRightArrowTogglesSenderRowButNeverOpensAThread(t *testing.T) {
	client := &inboxClient{}
	m := senderFixtureModel(client)
	m = pressSpecial(m, tea.KeyRight)
	if len(m.visibleRows()) != 3 {
		t.Fatalf("visibleRows after -> = %d, want 3 (expanded)", len(m.visibleRows()))
	}

	m.selected = 1 // now on the nested thread row t1
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	got := updated.(Model)
	if got.detail || cmd != nil {
		t.Fatal("-> on a thread row must never open it")
	}
	if len(got.visibleRows()) != 3 {
		t.Fatal("-> on a thread row must not change the sender's expand state")
	}
}

// TestLeftArrowCollapsesTheSelectedSenderRow covers "← collapses" when
// the cursor is already on the sender row itself.
func TestLeftArrowCollapsesTheSelectedSenderRow(t *testing.T) {
	m := senderFixtureModel(&inboxClient{})
	m = pressSpecial(m, tea.KeyRight) // expand
	m = pressSpecial(m, tea.KeyLeft)  // selection still on the sender row
	if len(m.visibleRows()) != 1 {
		t.Fatalf("visibleRows after <- = %d, want 1 (collapsed)", len(m.visibleRows()))
	}
}

// TestLeftArrowOnNestedThreadJumpsToSenderAndCollapses covers "when the
// cursor is on a thread, ← jumps to its sender row and collapses it" —
// and, crucially, that the selection lands on the now-visible sender row
// rather than being left pointing at a row that just disappeared (the
// "selection always stays visible" guarantee).
func TestLeftArrowOnNestedThreadJumpsToSenderAndCollapses(t *testing.T) {
	m := senderFixtureModel(&inboxClient{})
	m = pressSpecial(m, tea.KeyRight) // expand: rows = [sender, t1, t2]
	m.selected = 2                    // move onto the second nested thread (t2)

	m = pressSpecial(m, tea.KeyLeft)

	rows := m.visibleRows()
	if len(rows) != 1 {
		t.Fatalf("visibleRows after <- from a nested thread = %d, want 1 (collapsed)", len(rows))
	}
	if m.selected != 0 {
		t.Fatalf("selected = %d, want 0 (the sender row, so the selection stays visible)", m.selected)
	}
	if rows[m.selected].kind != navSender {
		t.Fatalf("selection landed on %+v, want the sender row", rows[m.selected])
	}
}

// TestEnterOnNestedThreadStillOpensTheMailThreadView covers that
// expanding a sender does not change what Enter on an actual thread row
// does: it still opens the existing K6 mail thread view.
func TestEnterOnNestedThreadStillOpensTheMailThreadView(t *testing.T) {
	client := &inboxClient{}
	m := senderFixtureModel(client)
	m = pressSpecial(m, tea.KeyRight) // expand
	m.selected = 1                    // the nested t1 thread row

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if !got.detail || !got.threadMode {
		t.Fatal("Enter on a nested thread row did not open the mail thread view")
	}
	if cmd == nil {
		t.Fatal("opening via Enter did not return a load command")
	}
}

// TestSenderExpandStatePersistsAcrossPolls covers "the expand state
// survives polls (keyed by sender address)": a poll that reloads the
// exact same items must not silently re-collapse an expanded sender.
func TestSenderExpandStatePersistsAcrossPolls(t *testing.T) {
	client := &inboxClient{}
	m := senderFixtureModel(client)
	m = pressSpecial(m, tea.KeyRight) // expand
	if len(m.visibleRows()) != 3 {
		t.Fatal("fixture setup: sender did not expand")
	}

	m.polling, m.pollToken = true, 1
	items := []core.Item{
		m.groups[0].items[0],
		m.groups[1].items[0],
	}
	updated, _ := m.Update(inboxLoadedMsg{token: 1, items: items, counts: m.counts})
	got := updated.(Model)
	if len(got.visibleRows()) != 3 {
		t.Fatalf("visibleRows after a poll = %d, want 3 (still expanded)", len(got.visibleRows()))
	}
}

// TestSelectedItemIDIgnoresACollapsedSenderRow covers that "r"/"m" can
// never target a sender header — only an actual thread's item.
func TestSelectedItemIDIgnoresACollapsedSenderRow(t *testing.T) {
	m := senderFixtureModel(&inboxClient{})
	if _, ok := m.selectedItemID(); ok {
		t.Fatal("selectedItemID resolved an id from a collapsed sender row")
	}
	m = pressSpecial(m, tea.KeyRight)
	m.selected = 1
	id, ok := m.selectedItemID()
	if !ok || id != "mail:cl:t1" {
		t.Fatalf("selectedItemID = %q/%v, want mail:cl:t1/true once on a thread row", id, ok)
	}
}
