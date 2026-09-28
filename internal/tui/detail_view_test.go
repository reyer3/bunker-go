package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// longDetailItem returns a plain single-item detail fixture with a body
// long enough to force scrolling in a 20-row pane (mirroring the mail
// thread/chat views' own long-body fixtures).
func longDetailItem() core.Item {
	var bodyLines []string
	for i := 0; i < 40; i++ {
		bodyLines = append(bodyLines, fmt.Sprintf("linea %d del cuerpo", i))
	}
	return core.Item{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl",
		Subject: "un asunto largo", From: core.Address{Name: "Bob"},
		Body: strings.Join(bodyLines, "\n"),
	}
}

// detailFixtureModel opens the plain single-item detail view directly
// (the way mouse.go's openItem fallback does, bypassing the network) with
// a fixed pane height, mirroring how mail_thread_view_test.go bypasses
// openThread's own load round trip.
func detailFixtureModel(item core.Item) Model {
	m := NewModel(nil)
	m.width, m.height = 62, 20
	m.detail = true
	m.reading = false
	m.readErr = nil
	m.readItem = item
	return m
}

// TestDetailViewFitsPaneHeightWithLongBody mirrors the chat/thread views'
// own live-reported height-overflow bug for the plain single-item detail
// view: a long body must scroll WITHIN the view instead of pushing the
// Subject header or the footer hint off screen.
func TestDetailViewFitsPaneHeightWithLongBody(t *testing.T) {
	m := detailFixtureModel(longDetailItem())

	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > m.height {
		t.Fatalf("detail view has %d lines, want <= pane height %d", len(lines), m.height)
	}
	if !strings.Contains(lines[0], "un asunto largo") {
		t.Fatalf("line 0 = %q, want the Subject header to stay the first, always-visible line", lines[0])
	}
	if !strings.Contains(view, "Esc volver") || !strings.Contains(view, "q salir") {
		t.Fatalf("view = %q, want the footer hint still visible", view)
	}
}

// TestDetailPgDownScrollsLongBodyThenPgUpBack pins PgUp/PgDown scrolling
// a long body within the view, and back.
func TestDetailPgDownScrollsLongBodyThenPgUpBack(t *testing.T) {
	m := detailFixtureModel(longDetailItem())
	if !strings.Contains(m.View(), "linea 0 del cuerpo") {
		t.Fatalf("view = %q, want the body's start visible by default", m.View())
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = updated.(Model)
	if strings.Contains(m.View(), "linea 0 del cuerpo") {
		t.Fatalf("view after PgDown = %q, want the body's start scrolled out of view", m.View())
	}
	if !strings.Contains(m.View(), "un asunto largo") {
		t.Fatalf("view after PgDown = %q, want the Subject header still visible", m.View())
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = updated.(Model)
	if !strings.Contains(m.View(), "linea 0 del cuerpo") {
		t.Fatalf("view after PgDown then PgUp = %q, want the body's start visible again", m.View())
	}
}

// TestDetailJKAndArrowsScrollOneLineAtATime pins j/k and the plain
// up/down arrows as one-line scroll steps, distinct from PgUp/PgDown's
// full-page jump.
func TestDetailJKAndArrowsScrollOneLineAtATime(t *testing.T) {
	m := detailFixtureModel(longDetailItem())

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)
	if m.detailScroll != 1 {
		t.Fatalf("detailScroll after one \"j\" = %d, want 1", m.detailScroll)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.detailScroll != 2 {
		t.Fatalf("detailScroll after \"j\" then down = %d, want 2", m.detailScroll)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = updated.(Model)
	if m.detailScroll != 1 {
		t.Fatalf("detailScroll after \"k\" = %d, want 1", m.detailScroll)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.detailScroll != 0 {
		t.Fatalf("detailScroll after up = %d, want 0", m.detailScroll)
	}
}

// TestDetailCapitalGGoesToBottomLowercaseGStillRefreshes pins the g/G
// split this view needs: lowercase "g" already means "refresh the inbox"
// everywhere, including while a detail view is open (existing, tested
// behavior), so it must keep that meaning here too rather than being
// repurposed as "scroll to top" — only the free "G" binds "scroll to
// bottom".
func TestDetailCapitalGGoesToBottomLowercaseGStillRefreshes(t *testing.T) {
	m := detailFixtureModel(longDetailItem())

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(Model)
	if strings.Contains(m.View(), "linea 0 del cuerpo") {
		t.Fatalf("view after \"G\" = %q, want scrolled away from the body's start", m.View())
	}
	if !strings.Contains(m.View(), "linea 39 del cuerpo") {
		t.Fatalf("view after \"G\" = %q, want the body's last line visible", m.View())
	}

	// client is nil, so "g" (refresh) returns immediately without
	// touching detailScroll: this confirms "g" was never hijacked into a
	// scroll-to-top binding here.
	before := m.detailScroll
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("\"g\" with no client unexpectedly returned a command")
	}
	if m.detailScroll != before {
		t.Fatalf("detailScroll changed after \"g\" (%d -> %d), want \"g\" to stay the refresh key, untouched here", before, m.detailScroll)
	}
}

// TestDetailMouseWheelScrolls pins the mouse wheel scrolling the plain
// detail view's body, the same contract the chat/thread views already
// have (TestMouseWheelMovesSelectionWithoutReading covers the inbox's own
// wheel contract; TestMouseIgnoredWhileComposingOrInDetail covers that a
// CLICK in detail stays a no-op).
func TestDetailMouseWheelScrolls(t *testing.T) {
	m := detailFixtureModel(longDetailItem())

	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m = updated.(Model)
	if m.detailScroll == 0 {
		t.Fatal("wheel down did not scroll the detail body")
	}
	scrolledDown := m.detailScroll

	updated, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = updated.(Model)
	if m.detailScroll >= scrolledDown {
		t.Fatalf("detailScroll after wheel up = %d, want less than %d", m.detailScroll, scrolledDown)
	}
}
