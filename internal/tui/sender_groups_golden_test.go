package tui

import (
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"
	"github.com/reyer3/bunker-go/internal/core"
)

// senderGoldenModel builds a loaded, sized Mail-focused model with two
// fictional senders (one with two threads, one with a single thread —
// mail-sender-groups.md's "a sender with exactly one thread still gets a
// sender row" consistency rule), at a pinned clock and a forced
// TrueColor profile so the golden is deterministic regardless of the
// real wall-clock date or the terminal's actual color capability.
func senderGoldenModel() Model {
	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	m := NewModel(nil).withGlyphs(nil)
	m.render = r
	m.now = func() time.Time { return at }
	m.loaded = true
	m.width, m.height = 62, 20
	m.groups = []inboxGroup{
		{items: []core.Item{{
			ID: "mail:cl:t1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
			From:    core.Address{ID: "alice@example.com", Name: "Alice Doe"},
			Subject: "Reunión de mañana", Body: "¿Seguimos con el horario de siempre?",
			Unread: true, Timestamp: at,
		}}},
		{items: []core.Item{{
			ID: "mail:cl:t2", Channel: core.ChannelMail, Account: "cl", Thread: "t2",
			From:    core.Address{ID: "alice@example.com", Name: "Alice Doe"},
			Subject: "Borrador del informe", Body: "Te dejo el borrador para revisar.",
			Unread: true, Timestamp: at.Add(-time.Hour),
		}}},
		{items: []core.Item{{
			ID: "mail:cl:t3", Channel: core.ChannelMail, Account: "cl", Thread: "t3",
			From:    core.Address{ID: "carol@example.com", Name: "Carol Diaz"},
			Subject: "Factura de septiembre", Body: "Adjunto la factura del mes.",
			Unread: true, Timestamp: at.Add(-2 * time.Hour),
		}}},
	}
	m.counts = map[core.Channel]map[string]int{core.ChannelMail: {"cl": 3}}
	m = m.switchTab(1) // focus the Mail section alone, per S2's "focus mode"
	return m
}

// TestSenderGroupsCollapsedGolden goldens the Mail section collapsed:
// two sender rows (Alice's two threads folded under one chevron, Carol's
// single thread still getting its own row), neither expanded.
func TestSenderGroupsCollapsedGolden(t *testing.T) {
	m := senderGoldenModel()
	teatest.RequireEqualOutput(t, []byte(m.View()))
}

// TestSenderGroupsExpandedGolden goldens the same Mail section after →
// expands Alice's sender row: her two threads render indented, newest
// first, with the existing two-line row design; Carol's row is
// unaffected.
func TestSenderGroupsExpandedGolden(t *testing.T) {
	m := senderGoldenModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(Model)
	teatest.RequireEqualOutput(t, []byte(m.View()))
}
