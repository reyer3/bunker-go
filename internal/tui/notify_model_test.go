package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// notifyReadyModel returns a model that is blurred (unfocused) and has
// notifications enabled into buf — the state every maybeNotify test
// starts from. oldGroups (the "previous poll" snapshot maybeNotify diffs
// against) is one already-known mail item.
func notifyReadyModel(buf *bytes.Buffer) Model {
	m := NewModel(nil)
	m.blurred = true
	m.notifyEnabled = true
	m.notifyWriter = buf
	m.now = func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) }
	m.groups = []inboxGroup{{items: []core.Item{{ID: "mail:a:1", Channel: core.ChannelMail, Unread: true}}}}
	return m
}

func TestNotifyNeverFiresOnTheInitialBacklog(t *testing.T) {
	var buf bytes.Buffer
	m := notifyReadyModel(&buf)
	oldGroups := m.groups
	newItems := []core.Item{
		{ID: "mail:a:1", Channel: core.ChannelMail, Unread: true},
		{ID: "mail:a:2", Channel: core.ChannelMail, Unread: true},
	}
	// wasLoaded=false: this is the very first load.
	_, cmd := m.maybeNotify(false, oldGroups, newItems)
	if cmd != nil {
		cmd()
	}
	if buf.Len() != 0 {
		t.Fatalf("initial backlog notified: %q", buf.String())
	}
}

func TestNotifyFiresForNewUnreadWhileBlurred(t *testing.T) {
	var buf bytes.Buffer
	m := notifyReadyModel(&buf)
	oldGroups := m.groups
	newItems := []core.Item{
		{ID: "mail:a:1", Channel: core.ChannelMail, Unread: true}, // already known
		{ID: "mail:a:2", Channel: core.ChannelMail, Unread: true, From: core.Address{Name: "Bob"}, Body: "new message"},
	}

	updated, cmd := m.maybeNotify(true, oldGroups, newItems)
	if cmd == nil {
		t.Fatal("no command returned")
	}
	cmd()
	if !strings.Contains(buf.String(), "\x1b]777;notify;Bob;new message\a") {
		t.Fatalf("notification not written: %q", buf.String())
	}
	if updated.lastNotifyAt.IsZero() {
		t.Fatal("lastNotifyAt was not recorded")
	}
}

func TestNotifyNeverFiresWhileFocused(t *testing.T) {
	var buf bytes.Buffer
	m := notifyReadyModel(&buf)
	m.blurred = false // focused
	oldGroups := m.groups
	newItems := []core.Item{
		{ID: "mail:a:1", Channel: core.ChannelMail, Unread: true},
		{ID: "mail:a:2", Channel: core.ChannelMail, Unread: true},
	}

	_, cmd := m.maybeNotify(true, oldGroups, newItems)
	if cmd != nil {
		cmd()
	}
	if buf.Len() != 0 {
		t.Fatalf("focused panel notified: %q", buf.String())
	}
}

func TestNotifyDisabledNeverWrites(t *testing.T) {
	var buf bytes.Buffer
	m := notifyReadyModel(&buf)
	m.notifyEnabled = false
	oldGroups := m.groups
	newItems := []core.Item{
		{ID: "mail:a:1", Channel: core.ChannelMail, Unread: true},
		{ID: "mail:a:2", Channel: core.ChannelMail, Unread: true},
	}

	_, cmd := m.maybeNotify(true, oldGroups, newItems)
	if cmd != nil {
		cmd()
	}
	if buf.Len() != 0 {
		t.Fatalf("disabled notify wrote anyway: %q", buf.String())
	}
}

func TestNotifyRateLimitsAndCoalesces(t *testing.T) {
	var buf bytes.Buffer
	m := notifyReadyModel(&buf)
	oldGroups := m.groups

	// First new arrival: fires immediately.
	updated, cmd := m.maybeNotify(true, oldGroups, []core.Item{
		{ID: "mail:a:1", Channel: core.ChannelMail, Unread: true},
		{ID: "mail:a:2", Channel: core.ChannelMail, Unread: true, Body: "one"},
	})
	cmd()
	m = updated
	oldGroups = append(oldGroups, inboxGroup{items: []core.Item{{ID: "mail:a:2", Channel: core.ChannelMail, Unread: true}}})
	if firstCount := strings.Count(buf.String(), "\x1b]777;notify;"); firstCount != 1 {
		t.Fatalf("first arrival wrote %d notifications, want 1", firstCount)
	}

	// A second arrival 2s later (still within the 10s window) must not
	// write a second notification, only accumulate pendingNotify.
	m.now = func() time.Time { return time.Date(2026, 9, 26, 12, 0, 2, 0, time.UTC) }
	updated, cmd = m.maybeNotify(true, oldGroups, []core.Item{
		{ID: "mail:a:1", Channel: core.ChannelMail, Unread: true},
		{ID: "mail:a:2", Channel: core.ChannelMail, Unread: true, Body: "one"},
		{ID: "mail:a:3", Channel: core.ChannelMail, Unread: true, Body: "two"},
	})
	if cmd != nil {
		cmd()
	}
	m = updated
	oldGroups = append(oldGroups, inboxGroup{items: []core.Item{{ID: "mail:a:3", Channel: core.ChannelMail, Unread: true}}})
	if got := strings.Count(buf.String(), "\x1b]777;notify;"); got != 1 {
		t.Fatalf("coalesced arrival wrote %d notifications total, want still 1", got)
	}
	if m.pendingNotify != 1 {
		t.Fatalf("pendingNotify = %d, want 1 (item 3 coalesced)", m.pendingNotify)
	}

	// A third arrival after the 10s window flushes the coalesced count.
	m.now = func() time.Time { return time.Date(2026, 9, 26, 12, 0, 15, 0, time.UTC) }
	updated, cmd = m.maybeNotify(true, oldGroups, []core.Item{
		{ID: "mail:a:1", Channel: core.ChannelMail, Unread: true},
		{ID: "mail:a:2", Channel: core.ChannelMail, Unread: true, Body: "one"},
		{ID: "mail:a:3", Channel: core.ChannelMail, Unread: true, Body: "two"},
		{ID: "mail:a:4", Channel: core.ChannelMail, Unread: true, Body: "three"},
	})
	if cmd == nil {
		t.Fatal("flush after the window did not return a command")
	}
	cmd()
	_ = updated
	if got := strings.Count(buf.String(), "\x1b]777;notify;"); got != 2 {
		t.Fatalf("after the flush, wrote %d notifications total, want 2", got)
	}
	if !strings.Contains(buf.String(), "bunker;2 new") {
		t.Fatalf("flushed notification missing coalesced count: %q", buf.String())
	}
}

func TestNotifyWrapsTmuxPassthroughWhenSet(t *testing.T) {
	var buf bytes.Buffer
	m := notifyReadyModel(&buf)
	m.tmuxPassthrough = true
	oldGroups := m.groups
	newItems := []core.Item{
		{ID: "mail:a:1", Channel: core.ChannelMail, Unread: true},
		{ID: "mail:a:2", Channel: core.ChannelMail, Unread: true, From: core.Address{Name: "Bob"}, Body: "hi"},
	}

	_, cmd := m.maybeNotify(true, oldGroups, newItems)
	cmd()
	got := buf.String()
	if !strings.HasPrefix(got, "\x1bPtmux;") || !strings.HasSuffix(got, "\x1b\\") {
		t.Fatalf("notification not tmux-wrapped: %q", got)
	}
	if !strings.Contains(got, "\x1b\x1b]777;notify;Bob;hi\a") {
		t.Fatalf("wrapped payload missing doubled-ESC inner sequence: %q", got)
	}
}
