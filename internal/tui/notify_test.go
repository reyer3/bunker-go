package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestNotifyPayloadIsOSC777AndSanitized(t *testing.T) {
	got := notifyPayload("Carlos\x1b[31m", "abran porfa\x1b]0;x\x07 ya")
	want := "\x1b]777;notify;Carlos;abran porfa ya\a"
	if got != want {
		t.Errorf("notifyPayload = %q, want %q", got, want)
	}
	for _, bad := range []string{"\x1b[31m", "\x1b]0;x"} {
		if strings.Contains(got, bad) {
			t.Errorf("notifyPayload leaked unsanitized escape %q: %q", bad, got)
		}
	}
}

func TestNotifyPayloadTruncatesBodyTo60Cells(t *testing.T) {
	body := strings.Repeat("x", 100)
	got := notifyPayload("Alice", body)
	// Everything between the 3rd ";" and the trailing BEL is the body.
	i := strings.LastIndex(got, ";")
	payloadBody := strings.TrimSuffix(got[i+1:], "\a")
	if len([]rune(payloadBody)) > 61 { // 60 chars + "…"
		t.Errorf("body %q is %d runes, want <= 61 (60 + ellipsis)", payloadBody, len([]rune(payloadBody)))
	}
	if !strings.HasSuffix(payloadBody, "…") {
		t.Errorf("truncated body %q missing ellipsis", payloadBody)
	}
}

func TestTmuxPassthroughWrapsAndDoublesInnerEscapes(t *testing.T) {
	payload := "\x1b]777;notify;A;B\a"
	got := tmuxPassthrough(payload)
	want := "\x1bPtmux;\x1b\x1b]777;notify;A;B\a\x1b\\"
	if got != want {
		t.Errorf("tmuxPassthrough = %q, want %q", got, want)
	}
}

func TestNewUnreadItemsOnlyReturnsIDsAbsentFromOldGroups(t *testing.T) {
	old := []inboxGroup{{items: []core.Item{{ID: "a", Unread: true}}}}
	newItems := []core.Item{
		{ID: "a", Unread: true},  // already known: not new
		{ID: "b", Unread: true},  // new
		{ID: "c", Unread: false}, // read: never notify
	}
	got := newUnreadItems(old, newItems)
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("newUnreadItems = %+v, want just [b]", got)
	}
}

func TestNotificationTextSingleItemUsesSenderAndBody(t *testing.T) {
	fresh := []core.Item{{From: core.Address{Name: "Alice"}, Body: "hello there"}}
	title, body := notificationText(fresh, 0)
	if title != "Alice" || body != "hello there" {
		t.Errorf("title/body = %q/%q, want Alice/hello there", title, body)
	}
}

func TestNotificationTextCoalescesMultipleIntoNNew(t *testing.T) {
	fresh := []core.Item{{Body: "a"}, {Body: "b"}}
	title, body := notificationText(fresh, 3) // 2 fresh + 3 pending = 5
	if title != "bunker" || body != "5 new" {
		t.Errorf("title/body = %q/%q, want bunker/5 new", title, body)
	}
}

func TestNotificationTextNeverLeaksARawSender(t *testing.T) {
	fresh := []core.Item{{Channel: core.ChannelWhatsApp, From: core.Address{Name: "34600112233@s.whatsapp.net"}, Body: "hi"}}
	title, _ := notificationText(fresh, 0)
	if strings.Contains(title, "@") {
		t.Errorf("title %q leaked a raw JID", title)
	}
}

func TestResolveNotifyEnabled(t *testing.T) {
	falseVal := false
	trueVal := true
	env := func(v string) func(string) string { return func(string) string { return v } }
	tests := []struct {
		name   string
		cfg    *bool
		envVal string
		want   bool
	}{
		{"default enabled (no config, no env)", nil, "", true},
		{"explicit config true stays enabled", &trueVal, "", true},
		{"config false disables", &falseVal, "", false},
		{"env BUNKER_TUI_NOTIFY=0 disables", nil, "0", false},
		{"env disables even if config says true", &trueVal, "0", false},
		{"unrelated env value does not disable", nil, "1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveNotifyEnabled(tt.cfg, env(tt.envVal)); got != tt.want {
				t.Errorf("resolveNotifyEnabled(%v, %q) = %v, want %v", tt.cfg, tt.envVal, got, tt.want)
			}
		})
	}
}

func TestShouldNotifyDecidesRateLimitAndCoalescing(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	// First qualifying arrival (1 fresh item) always fires: lastNotifyAt
	// is zero, so it is not "within" any rate-limit window.
	fire, pending := shouldNotify(time.Time{}, 0, 1, now)
	if !fire || pending != 0 {
		t.Fatalf("first arrival: fire=%v pending=%d, want true/0 (sent, nothing left pending)", fire, pending)
	}

	// A second arrival (1 more item) 2s later, within the 10s window
	// since the last actual send, is coalesced rather than sent.
	fire, pending = shouldNotify(now, 0, 1, now.Add(2*time.Second))
	if fire {
		t.Fatal("arrival within the 10s window fired instead of coalescing")
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want 1 (this arrival's item accumulated)", pending)
	}

	// A third arrival (1 more item) after the window closes flushes the
	// accumulated pending count together with this batch.
	fire, pending = shouldNotify(now, 1, 1, now.Add(11*time.Second))
	if !fire {
		t.Fatal("arrival after the 10s window did not fire")
	}
	if pending != 0 {
		t.Fatalf("pending after a flush = %d, want 0", pending)
	}
}
