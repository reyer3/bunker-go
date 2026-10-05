package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
)

// TestInboxFitsSidePanel covers the live side-panel failure: in a real
// tmux split, long subjects wrapped onto a second line and the list grew
// past the pane, pushing content off screen. The sectioned overview must
// truncate every line to the width, never exceed the pane height, keep
// every section header visible, and scroll the section holding the
// selection so it stays visible — truncating the rest with a dim
// "+N más" notice rather than hiding it silently.
func TestInboxFitsSidePanel(t *testing.T) {
	const width, height = 40, 20
	var items []core.Item
	base := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		items = append(items, core.Item{
			ID:      fmt.Sprintf("mail:cl:%d", i),
			Channel: core.ChannelMail,
			Account: "cl",
			Thread:  fmt.Sprintf("t%d", i),
			Subject: fmt.Sprintf("Subject %02d with a very long title that cannot fit in a side panel", i),
			// A distinct sender per item (mail-sender-groups.md merges by
			// From address): 30 separately collapsed sender rows, each
			// one line, instead of one merged sender absorbing every
			// thread — reproducing the same "too many rows for the pane"
			// scenario at the new sender-row granularity.
			From:      core.Address{ID: fmt.Sprintf("sender%02d@example.com", i), Name: fmt.Sprintf("Sender %02d", i)},
			Unread:    true,
			Timestamp: base.Add(-time.Duration(i) * time.Minute),
		})
	}
	var model tea.Model = NewModel(nil)
	m := model.(Model)
	m.polling, m.pollToken = true, 1
	// Pin the clock to the fixture's own day: relativeTime's output
	// length ("HH:MM" vs "ayer" vs "dd-mmm") feeds this test's width
	// math, so it must not depend on the real wall-clock date.
	m.now = func() time.Time { return base }
	model = m
	model, _ = model.Update(inboxLoadedMsg{token: 1, items: items, counts: map[core.Channel]map[string]int{core.ChannelMail: {"cl": 30}}})
	model, _ = model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	for i := 0; i < 20; i++ {
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}

	raw := model.View()
	// A trailing newline also takes a terminal row: a view of exactly
	// height lines plus "\n" scrolls content off the top.
	if rows := strings.Count(raw, "\n") + 1; rows > height {
		t.Errorf("view takes %d terminal rows, want at most %d (the pane height)", rows, height)
	}
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		if w := runewidth.StringWidth(line); w > width {
			t.Errorf("line %q is %d cells wide, want at most %d", line, w, width)
		}
	}
	if !strings.Contains(raw, "Mail (30)") {
		t.Error("the Mail section header scrolled off the panel")
	}
	if !strings.Contains(raw, "WhatsApp (0)") || !strings.Contains(raw, "Matrix (0)") {
		t.Error("an empty section's header is missing (no section may be pushed off screen)")
	}
	if !strings.Contains(raw, "sin no leídos") {
		t.Error("an empty section is missing its placeholder line")
	}
	if !strings.Contains(raw, "Sender 20") {
		t.Errorf("selected row (Sender 20) is not visible:\n%s", raw)
	}
	if !strings.Contains(raw, "más") {
		t.Error("the truncated Mail section is missing its \"+N más\" notice")
	}
}
