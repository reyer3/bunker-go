package tui

import (
	"io"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"
	"github.com/reyer3/bunker-go/internal/core"
)

// TestFooterKeepsQVisibleAtNarrowWidthGolden goldens the plain inbox
// overview at a 40-column terminal width, at a pinned clock and a forced
// TrueColor profile so the golden is deterministic: bunker-tui.md's
// follow-up gap was that the footer keymap hint's ellipsis truncation
// silently dropped "q to quit" at a narrow width — this golden pins that
// "q" now always stays visible there.
func TestFooterKeepsQVisibleAtNarrowWidthGolden(t *testing.T) {
	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	m := NewModel(nil).withGlyphs(nil)
	m.render = r
	m.now = func() time.Time { return at }
	m.loaded = true
	m.width, m.height = 40, 20
	m.groups = []inboxGroup{{items: []core.Item{{
		ID: "mail:cl:t1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
		From:    core.Address{ID: "alice@example.com", Name: "Alice Doe"},
		Subject: "Reunión de mañana", Body: "¿Seguimos con el horario de siempre?",
		Unread: true, Timestamp: at,
	}}}}
	m.counts = map[core.Channel]map[string]int{core.ChannelMail: {"cl": 1}}

	teatest.RequireEqualOutput(t, []byte(m.View()))
}
