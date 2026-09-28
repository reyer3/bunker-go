package tui

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// healthClient is an inboxClient that also reports adapter health.
type healthClient struct {
	inboxClient
	health []core.AdapterHealth
}

func (c *healthClient) Health(context.Context) ([]core.AdapterHealth, error) { return c.health, nil }

func pollOnce(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.startPoll()
	m = updated.(Model)
	msg := loadInbox(m.client, m.pollToken)()
	updated, _ = m.Update(msg)
	return updated.(Model)
}

func TestStatusLineDaemonDownAndRecovery(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	client := &healthClient{inboxClient: inboxClient{items: []core.Item{{
		ID: "whatsapp:wa:1", Channel: core.ChannelWhatsApp, Account: "wa", Thread: "t", ThreadName: "Ana", Unread: true, Timestamp: now,
	}}}}
	m := NewModel(client)
	m.now = func() time.Time { return now }
	m.width, m.height = 100, 20
	m = pollOnce(t, m)
	if _, ok := m.statusLine(); ok {
		t.Fatalf("a healthy inbox should have no status line:\n%s", m.View())
	}

	client.listErr = fmt.Errorf("rpc: send request: write unix @->/run/x.sock: %w", syscall.EPIPE)
	now = now.Add(3 * time.Minute)
	m = pollOnce(t, m)
	view := m.View()
	for _, want := range []string{"sin conexión con el daemon", "reintentando", "datos de hace 3 min", "Ana"} {
		if !strings.Contains(view, want) {
			t.Errorf("daemon down: view lacks %q:\n%s", want, view)
		}
	}

	client.listErr = nil
	m = pollOnce(t, m)
	if _, ok := m.statusLine(); ok {
		t.Fatalf("the status line should clear once the daemon is back:\n%s", m.View())
	}
}

func TestStatusLineNamesDisconnectedAccounts(t *testing.T) {
	client := &healthClient{health: []core.AdapterHealth{
		{Channel: core.ChannelMail, Account: "cl", State: core.AdapterConnected},
		{Channel: core.ChannelWhatsApp, Account: "wa", State: core.AdapterBackoff},
		{Channel: core.ChannelMatrix, Account: "work", State: core.AdapterStopped},
	}}
	m := NewModel(client)
	m.width, m.height = 100, 20
	m = pollOnce(t, m)
	line, ok := m.statusLine()
	if !ok || !strings.Contains(line, "WhatsApp/wa reconectando") || !strings.Contains(line, "Matrix/work detenido") || strings.Contains(line, "Mail") {
		t.Fatalf("status = %q", line)
	}
}

func TestFormatAge(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "hace 1s", 42 * time.Second: "hace 42s", 5 * time.Minute: "hace 5 min", 3 * time.Hour: "hace 3 h"} {
		if got := formatAge(d); got != want {
			t.Errorf("formatAge(%v) = %q, want %q", d, got, want)
		}
	}
}

var _ tea.Model = Model{}
