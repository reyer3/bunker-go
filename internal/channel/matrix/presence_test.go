package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// TestAdapterRunTracksTypingFromMTyping covers K3's "Matrix typing
// (receive m.typing)": an m.typing ephemeral event delivered through a
// real /sync response, listing another user, must make Presence report
// State "typing" with that user in Typers.
func TestAdapterRunTracksTypingFromMTyping(t *testing.T) {
	const room = id.RoomID("!abc:matrix.example.org")
	const other = id.UserID("@bob:matrix.example.org")

	typingEvt := &event.Event{
		Type:    event.EphemeralEventTyping,
		Content: event.Content{Parsed: &event.TypingEventContent{UserIDs: []id.UserID{other}}},
	}
	firstSync := &mautrix.RespSync{
		NextBatch: "s1",
		Rooms: mautrix.RespSyncRooms{
			Join: map[id.RoomID]*mautrix.SyncJoinedRoom{
				room: {Ephemeral: mautrix.SyncEventsList{Events: []*event.Event{typingEvt}}},
			},
		},
	}

	srv, state := newFakeHomeserver(t, []*mautrix.RespSync{firstSync})
	adapter := newTestAdapter(t, srv, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, newMemSink()) }()

	// The second /sync request means the first response (with the typing
	// event) was fully handled.
	select {
	case <-state.secondSync:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first /sync response to be processed")
	}
	got, err := adapter.Presence(context.Background(), string(room))
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() returned %v, want context.Canceled", err)
	}

	if got.State != "typing" || len(got.Typers) != 1 || got.Typers[0] != string(other) {
		t.Fatalf("Presence = %+v, want State=typing Typers=[%s]", got, other)
	}
}

// TestHandleTypingClearsWhenUserIDsEmpties covers the other half of
// K3's "receive m.typing": m.typing always carries the room's full
// current set (never an add/remove delta), so a later event with an
// empty user_ids list must clear a previously observed typer back to
// State "unknown". Calling handleTyping directly (as Run's syncer would)
// keeps this deterministic instead of racing two real /sync responses.
func TestHandleTypingClearsWhenUserIDsEmpties(t *testing.T) {
	const room = id.RoomID("!abc:matrix.example.org")
	const other = id.UserID("@bob:matrix.example.org")

	srv, _ := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)
	ctx := context.Background()

	adapter.handleTyping(ctx, &event.Event{
		RoomID:  room,
		Type:    event.EphemeralEventTyping,
		Content: event.Content{Parsed: &event.TypingEventContent{UserIDs: []id.UserID{other}}},
	})
	p, err := adapter.Presence(ctx, string(room))
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.State != "typing" || len(p.Typers) != 1 {
		t.Fatalf("Presence after composing = %+v, want State=typing with one typer", p)
	}

	adapter.handleTyping(ctx, &event.Event{
		RoomID:  room,
		Type:    event.EphemeralEventTyping,
		Content: event.Content{Parsed: &event.TypingEventContent{UserIDs: []id.UserID{}}},
	})
	p, err = adapter.Presence(ctx, string(room))
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.State != "unknown" {
		t.Fatalf("Presence after typing stopped = %+v, want State=unknown", p)
	}
}

// TestPresenceReportsUnknownForARoomWithNoTypingEver covers a room never
// mentioned by an m.typing event: Presence must report "unknown", never
// a zero-value guess.
func TestPresenceReportsUnknownForARoomWithNoTypingEver(t *testing.T) {
	srv, _ := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)

	p, err := adapter.Presence(context.Background(), "!never:matrix.example.org")
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.State != "unknown" {
		t.Fatalf("State = %q, want unknown", p.State)
	}
}

// TestSendTypingCallsUserTyping proves SendTyping forwards to the
// mautrix client's UserTyping with the right on/off state; the exact
// timeout is an implementation detail the TUI's own re-send throttling
// (every <=5s while composing) does not depend on.
func TestSendTypingCallsUserTyping(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)
	room := "!abc:matrix.example.org"

	if err := adapter.SendTyping(context.Background(), room, true); err != nil {
		t.Fatalf("SendTyping(composing): %v", err)
	}
	if err := adapter.SendTyping(context.Background(), room, false); err != nil {
		t.Fatalf("SendTyping(paused): %v", err)
	}

	state.mu.Lock()
	calls := append([]typingCall{}, state.typingCalls...)
	state.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("typingCalls = %+v, want 2 calls", calls)
	}
	if !decodeTyping(t, calls[0].body) {
		t.Errorf("first call typing = false, want true (composing)")
	}
	if decodeTyping(t, calls[1].body) {
		t.Errorf("second call typing = true, want false (paused)")
	}
}

func decodeTyping(t *testing.T, body []byte) bool {
	t.Helper()
	var req struct {
		Typing bool `json:"typing"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode typing request body %s: %v", body, err)
	}
	return req.Typing
}
