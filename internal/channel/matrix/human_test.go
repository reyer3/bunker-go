package matrix

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"maunium.net/go/mautrix"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestTypingDurationProportionalToBodyLength covers T13(d): a typing
// notification proportional to the text at ~7 chars/second, clamped to
// [2s,15s]. Unlike WhatsApp's composingDuration, Matrix's typing window
// carries no jitter.
func TestTypingDurationProportionalToBodyLength(t *testing.T) {
	if got := typingDuration(70); got != 10*time.Second { // 70/7 = 10s
		t.Fatalf("typingDuration(70) = %v, want 10s", got)
	}
	if got := typingDuration(1); got != 2*time.Second {
		t.Fatalf("typingDuration(1) = %v, want the 2s floor", got)
	}
	if got := typingDuration(1000); got != 15*time.Second {
		t.Fatalf("typingDuration(1000) = %v, want the 15s ceiling", got)
	}
}

// TestAdapterSendShowsTypingBeforeSending covers T13(d): Send shows
// UserTyping(true), waits, UserTyping(false), then the m.room.message
// send — no presence, unlike WhatsApp.
func TestAdapterSendShowsTypingBeforeSending(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)
	var sleeps []time.Duration
	adapter.SetSleeper(func(d time.Duration) { sleeps = append(sleeps, d) })

	body := make([]byte, 70) // 70/7cps = 10s
	for i := range body {
		body[i] = 'x'
	}
	_, err := adapter.Send(context.Background(), core.Outgoing{
		Channel: core.ChannelMatrix, Account: "work",
		To: []string{"!room:matrix.example.org"}, Body: string(body),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(sleeps) != 1 || sleeps[0] != 10*time.Second {
		t.Fatalf("sleeps = %+v, want [10s]", sleeps)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.typingCalls) != 2 {
		t.Fatalf("typingCalls = %+v, want 2 (true then false)", state.typingCalls)
	}
	if state.typingCalls[0].roomID != "!room:matrix.example.org" {
		t.Errorf("typingCalls[0].roomID = %q, want !room:matrix.example.org", state.typingCalls[0].roomID)
	}
	var first, second mautrix.ReqTyping
	if err := json.Unmarshal(state.typingCalls[0].body, &first); err != nil {
		t.Fatalf("decode typingCalls[0]: %v", err)
	}
	if err := json.Unmarshal(state.typingCalls[1].body, &second); err != nil {
		t.Fatalf("decode typingCalls[1]: %v", err)
	}
	if !first.Typing {
		t.Errorf("typingCalls[0].Typing = false, want true")
	}
	if second.Typing {
		t.Errorf("typingCalls[1].Typing = true, want false")
	}
	if len(state.sentEvents) != 1 {
		t.Fatalf("sentEvents = %d, want 1 (the message itself, after typing)", len(state.sentEvents))
	}
}

// TestAdapterMarkReadSendsReadAndFullyReadMarkers covers T13(d): `bunker
// read` on Matrix sends an m.read receipt (and updates the fully-read
// marker), with no presence — Matrix has no presence concept here.
func TestAdapterMarkReadSendsReadAndFullyReadMarkers(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)

	msgItemID := itemID("work", "!room:matrix.example.org", "$event1")
	if err := adapter.MarkRead(context.Background(), msgItemID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.readMarkers) != 1 {
		t.Fatalf("readMarkers = %d, want 1", len(state.readMarkers))
	}
	var marker mautrix.ReqSetReadMarkers
	if err := json.Unmarshal(state.readMarkers[0], &marker); err != nil {
		t.Fatalf("decode read marker: %v", err)
	}
	if marker.Read != "$event1" || marker.FullyRead != "$event1" {
		t.Errorf("marker = %+v, want Read=FullyRead=$event1", marker)
	}
}

// TestAdapterIsReadMarkerCapability is a static assertion.
func TestAdapterIsReadMarkerCapability(t *testing.T) {
	var a Adapter
	var _ core.ReadMarker = &a
}

// TestAdapterMarkThreadReadSendsOneReceiptOnTheNewestEvent covers K9
// (conversation-view.md's read-thread fix): Matrix's read/fully_read
// markers already cover every earlier event in the room, so
// MarkThreadRead needs only one receipt, on ids' newest (last) entry.
func TestAdapterMarkThreadReadSendsOneReceiptOnTheNewestEvent(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)

	ids := []string{
		itemID("work", "!room:matrix.example.org", "$event1"),
		itemID("work", "!room:matrix.example.org", "$event2"),
		itemID("work", "!room:matrix.example.org", "$event3"),
	}
	if err := adapter.MarkThreadRead(context.Background(), ids); err != nil {
		t.Fatalf("MarkThreadRead: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.readMarkers) != 1 {
		t.Fatalf("readMarkers = %d, want 1 (a single receipt on the newest event)", len(state.readMarkers))
	}
	var marker mautrix.ReqSetReadMarkers
	if err := json.Unmarshal(state.readMarkers[0], &marker); err != nil {
		t.Fatalf("decode read marker: %v", err)
	}
	if marker.Read != "$event3" || marker.FullyRead != "$event3" {
		t.Errorf("marker = %+v, want Read=FullyRead=$event3 (the newest id)", marker)
	}
}

// TestAdapterMarkThreadReadEmptyIsNoOp covers the idempotent-with-
// nothing-unread case: no ids means no receipt at all.
func TestAdapterMarkThreadReadEmptyIsNoOp(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)

	if err := adapter.MarkThreadRead(context.Background(), nil); err != nil {
		t.Fatalf("MarkThreadRead: %v, want nil for an empty batch", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.readMarkers) != 0 {
		t.Fatalf("readMarkers = %d, want 0 for an empty batch", len(state.readMarkers))
	}
}

// TestAdapterIsThreadReaderCapability is a static assertion that Adapter
// satisfies core.ThreadReader.
func TestAdapterIsThreadReaderCapability(t *testing.T) {
	var a Adapter
	var _ core.ThreadReader = &a
}
