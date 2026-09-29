package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// reportClient is an inboxClient that answers HealthReport.
type reportClient struct {
	inboxClient
	update core.UpdateStatus
}

func (c *reportClient) HealthReport(context.Context) (core.HealthReport, error) {
	return core.HealthReport{Update: c.update}, nil
}

const wantNotice = "nueva versión v0.13.0 disponible · bunker update"

func TestUpdateNoticeOncePerSession(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	client := &reportClient{}
	m := NewModel(client)
	m.now = func() time.Time { return now }
	m.width, m.height = 100, 20

	m = pollOnce(t, m)
	if line, ok := m.statusLine(); ok {
		t.Fatalf("no update yet, status = %q", line)
	}

	client.update = core.UpdateStatus{Available: true, Latest: "0.13.0"}
	m = pollOnce(t, m)
	if line, ok := m.statusLine(); !ok || line != wantNotice {
		t.Fatalf("status = %q, want %q", line, wantNotice)
	}
	if !strings.Contains(m.View(), wantNotice) {
		t.Fatalf("view lacks the notice:\n%s", m.View())
	}

	now = now.Add(updateNoticeDuration + time.Second)
	m = pollOnce(t, m)
	if line, ok := m.statusLine(); ok {
		t.Fatalf("the notice should expire, status = %q", line)
	}
	// Later polls keep reporting the update; the session already saw it.
	now = now.Add(time.Hour)
	m = pollOnce(t, m)
	if line, ok := m.statusLine(); ok {
		t.Fatalf("the notice came back, status = %q", line)
	}
}

func TestUpdateNoticeInSidebar(t *testing.T) {
	client := &reportClient{update: core.UpdateStatus{Available: true, Latest: "0.13.0"}}
	m := NewModel(client, WithSidebar()).withGlyphs(nil)
	m.width, m.height = 60, 16
	m = pollOnce(t, m)
	if !strings.Contains(m.View(), wantNotice) {
		t.Fatalf("sidebar lacks the notice:\n%s", m.View())
	}
}

func TestUpdateNoticeNotifiesHerdrOnce(t *testing.T) {
	var bodies []string
	m := NewModel(&inboxClient{}, WithMessageNotifier(func(body string) error {
		bodies = append(bodies, body)
		return nil
	}))
	for range 3 {
		var cmd func() tea.Msg
		m, cmd = m.noteUpdate(core.UpdateStatus{Available: true, Latest: "0.13.0"})
		if cmd != nil {
			if msg, ok := cmd().(messageNotifiedMsg); !ok || msg.err != nil {
				t.Fatalf("notify cmd = %#v", msg)
			}
		}
	}
	if len(bodies) != 1 || bodies[0] != wantNotice {
		t.Fatalf("herdr notifications = %q, want one %q", bodies, wantNotice)
	}
}

func TestNoUpdateNoticeWithoutReport(t *testing.T) {
	// A daemon (or client) without HealthReport still shows adapter
	// health and never an update.
	client := &healthClient{health: []core.AdapterHealth{{Channel: core.ChannelWhatsApp, Account: "wa", State: core.AdapterBackoff}}}
	m := NewModel(client)
	m.width, m.height = 100, 20
	m = pollOnce(t, m)
	line, _ := m.statusLine()
	if !strings.Contains(line, "WhatsApp/wa reconectando") || strings.Contains(line, "nueva versión") {
		t.Fatalf("status = %q", line)
	}
}
