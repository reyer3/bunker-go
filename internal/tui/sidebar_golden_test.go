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

// TestSidebarViewGolden goldens the compact herdr panel (issue #81) at 32
// columns: the channel list with unread counts, then every conversation
// of the overview with a dim preview line under it, a long name truncated before its
// badge, and the short hint line. The clock and color profile are pinned
// so the golden is deterministic.
func TestSidebarViewGolden(t *testing.T) {
	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	m := NewModel(nil, WithSidebar()).withGlyphs(nil)
	m.render = r
	m.now = func() time.Time { return at }
	m.loaded = true
	m.width, m.height = 32, 16
	m.groups = []inboxGroup{
		{items: []core.Item{{
			ID: "mail:cl:t1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
			From:    core.Address{ID: "alice@example.com", Name: "Alice Doe"},
			Subject: "Reunión de mañana", Unread: true, Timestamp: at,
		}}},
		{items: []core.Item{
			{ID: "whatsapp:personal:2", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "g1",
				ThreadName: "Equipo de producto y diseño", From: core.Address{Name: "Bob"}, Body: "¿Revisamos el diseño?\nAhora", Unread: true, Timestamp: at},
			{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "g1",
				ThreadName: "Equipo de producto y diseño", From: core.Address{Name: "Carol"}, Unread: true, Timestamp: at.Add(-time.Minute)},
		}},
		{items: []core.Item{{
			ID: "whatsapp:personal:3", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "c1",
			From: core.Address{Name: "Dana"}, Unread: true, Timestamp: at.Add(-time.Hour),
			Attachments: []core.Attachment{{Name: "audio", MIME: "audio/ogg", Voice: true, Duration: 12}},
		}}},
		{items: []core.Item{{
			ID: "matrix:home:4", Channel: core.ChannelMatrix, Account: "home", Thread: "!room:example.org",
			ThreadName: "Soporte", From: core.Address{Name: "Erin"}, Body: "Ya está resuelto, gracias por esperar", Unread: true, Timestamp: at,
		}}},
	}
	m.counts = map[core.Channel]map[string]int{
		core.ChannelMail:     {"cl": 1},
		core.ChannelWhatsApp: {"personal": 3},
		core.ChannelMatrix:   {"home": 1},
	}
	m.selected = 1

	teatest.RequireEqualOutput(t, []byte(m.View()))
}
