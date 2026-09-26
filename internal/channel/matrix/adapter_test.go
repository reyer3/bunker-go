package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// memSink is a minimal in-memory core.Sink for tests, independent of
// internal/store so this package never has to import it.
type memSink struct {
	mu      sync.Mutex
	items   map[string]core.Item
	cursors map[string]string
	upserts chan core.Item
}

func newMemSink() *memSink {
	return &memSink{
		items:   make(map[string]core.Item),
		cursors: make(map[string]string),
		upserts: make(chan core.Item, 16),
	}
}

func (s *memSink) Upsert(_ context.Context, item core.Item) error {
	s.mu.Lock()
	s.items[item.ID] = item
	s.mu.Unlock()
	select {
	case s.upserts <- item:
	default:
	}
	return nil
}

func (s *memSink) MarkRead(context.Context, string, bool) error { return nil }

func (s *memSink) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.items, id)
	s.mu.Unlock()
	return nil
}

func (s *memSink) Cursor(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[key], nil
}

func (s *memSink) SetCursor(_ context.Context, key, val string) error {
	s.mu.Lock()
	s.cursors[key] = val
	s.mu.Unlock()
	return nil
}

// Get, List and Counts round out core.Store on top of core.Sink, so
// memSink can stand in for the daemon's real store in tests that need
// RetryUndecryptable's read access (an Adapter only ever gets a Sink from
// Run, never the full Store).
func (s *memSink) Get(_ context.Context, id string) (core.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return core.Item{}, core.ErrNotFound
	}
	return item, nil
}

func (s *memSink) List(_ context.Context, filter core.Filter) ([]core.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.Item
	for _, item := range s.items {
		if filter.Channel != "" && item.Channel != filter.Channel {
			continue
		}
		if filter.Account != "" && item.Account != filter.Account {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *memSink) Counts(context.Context) (map[core.Channel]map[string]int, error) {
	return nil, nil
}

var _ core.Store = (*memSink)(nil)

// fakeState records what a fakeHomeserver observed.
type fakeState struct {
	mu          sync.Mutex
	filterBody  []byte
	sentEvents  []sentEvent
	readMarkers [][]byte
	typingCalls []typingCall

	// getEventResponses serves GET .../rooms/<roomID>/event/<eventID>
	// (client.GetEvent), keyed by the raw eventID string, for
	// RetryUndecryptable's tests.
	getEventResponses map[string]json.RawMessage
}

type sentEvent struct {
	path string
	body []byte
}

// typingCall records one PUT .../rooms/{roomID}/typing/{userID} request
// (mautrix.Client.UserTyping), in the order the fake homeserver received
// them.
type typingCall struct {
	roomID string
	body   []byte
}

// newFakeHomeserver serves just enough of the Matrix client-server API for
// filter upload, /sync, sending events and read markers.
func newFakeHomeserver(t *testing.T, syncSeq []*mautrix.RespSync) (*httptest.Server, *fakeState) {
	t.Helper()
	state := &fakeState{}
	var mu sync.Mutex
	idx := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/user/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/filter") || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		state.mu.Lock()
		state.filterBody = body
		state.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"filter_id": "f1"})
	})
	mux.HandleFunc("/_matrix/client/v3/sync", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		var resp *mautrix.RespSync
		if idx < len(syncSeq) {
			resp = syncSeq[idx]
		} else {
			resp = &mautrix.RespSync{NextBatch: fmt.Sprintf("tail-%d", idx)}
		}
		idx++
		mu.Unlock()
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/send/") && r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			state.mu.Lock()
			state.sentEvents = append(state.sentEvents, sentEvent{path: r.URL.Path, body: body})
			n := len(state.sentEvents)
			state.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]string{"event_id": fmt.Sprintf("$sent%d", n)})
		case strings.Contains(r.URL.Path, "/typing/") && r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			roomID := r.URL.Path
			if i := strings.Index(roomID, "/typing/"); i >= 0 {
				roomID = roomID[strings.LastIndex(roomID[:i], "/")+1 : i]
			}
			state.mu.Lock()
			state.typingCalls = append(state.typingCalls, typingCall{roomID: roomID, body: body})
			state.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]string{})
		case strings.HasSuffix(r.URL.Path, "/read_markers") && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			state.mu.Lock()
			state.readMarkers = append(state.readMarkers, body)
			state.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]string{})
		case strings.Contains(r.URL.Path, "/event/") && r.Method == http.MethodGet:
			raw := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			eventID, err := url.PathUnescape(raw)
			if err != nil {
				eventID = raw
			}
			state.mu.Lock()
			resp, ok := state.getEventResponses[eventID]
			state.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, state
}

func newTestAdapter(t *testing.T, srv *httptest.Server, cryptoHelper mautrix.CryptoHelper) *Adapter {
	t.Helper()
	client, err := mautrix.NewClient(srv.URL, id.UserID("@alice:example.com"), "syt_test_token")
	if err != nil {
		t.Fatalf("mautrix.NewClient: %v", err)
	}
	client.DeviceID = "DEVICE1"
	a := newAdapter("work", client, cryptoHelper)
	// Send's typing-notification wait (T13d) defaults to a real sleep; a
	// test that does not care about its exact duration never blocks on
	// it. A test that DOES care overrides SetSleeper itself afterwards.
	a.SetSleeper(func(time.Duration) {})
	return a
}

func strPtr(s string) *string { return &s }

func TestAdapterRunUploadsCriticalFilterSyncsMessagesAndTracksUnread(t *testing.T) {
	const room = id.RoomID("!abc:matrix.example.org")
	const sender = id.UserID("@alice:matrix.example.org")

	nameEvt := &event.Event{
		ID:       "$name1",
		Sender:   sender,
		Type:     event.StateRoomName,
		StateKey: strPtr(""),
		Content:  event.Content{Parsed: &event.RoomNameEventContent{Name: "General"}},
	}
	msgEvt := &event.Event{
		ID:        "$evt1",
		Sender:    sender,
		Type:      event.EventMessage,
		Timestamp: 1700000000000,
		Content:   event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "hello alice"}},
	}
	firstSync := &mautrix.RespSync{
		NextBatch: "s1",
		Rooms: mautrix.RespSyncRooms{
			Join: map[id.RoomID]*mautrix.SyncJoinedRoom{
				room: {
					State:               mautrix.SyncEventsList{Events: []*event.Event{nameEvt}},
					Timeline:            mautrix.SyncTimeline{SyncEventsList: mautrix.SyncEventsList{Events: []*event.Event{msgEvt}}},
					UnreadNotifications: &mautrix.UnreadNotificationCounts{NotificationCount: 1},
				},
			},
		},
	}

	srv, state := newFakeHomeserver(t, []*mautrix.RespSync{firstSync})
	adapter := newTestAdapter(t, srv, nil)
	sink := newMemSink()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	var item core.Item
	select {
	case item = <-sink.upserts:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the adapter to upsert the synced message")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run to return after cancel")
	}

	state.mu.Lock()
	filterBody := string(state.filterBody)
	state.mu.Unlock()
	const wantFilter = `{"presence":{},"room":{"state":{"lazy_load_members":true},"timeline":{"limit":50}}}`
	if filterBody != wantFilter {
		t.Errorf("uploaded filter = %s, want %s", filterBody, wantFilter)
	}

	wantID := itemID("work", room, "$evt1")
	if item.ID != wantID {
		t.Errorf("item.ID = %q, want %q", item.ID, wantID)
	}
	if item.Body != "hello alice" {
		t.Errorf("item.Body = %q, want %q", item.Body, "hello alice")
	}
	if item.ThreadName != "General" {
		t.Errorf("item.ThreadName = %q, want General", item.ThreadName)
	}
	if item.Thread != string(room) {
		t.Errorf("item.Thread = %q, want %q", item.Thread, room)
	}
	if !item.Unread {
		t.Error("item.Unread = false, want true (room has unread_notifications.notification_count=1)")
	}
	if item.Channel != core.ChannelMatrix {
		t.Errorf("item.Channel = %q, want matrix", item.Channel)
	}

	fetched, err := adapter.Fetch(context.Background(), wantID)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fetched.ID != wantID {
		t.Errorf("Fetch id = %q, want %q", fetched.ID, wantID)
	}
}

func TestAdapterRunStoresUndecryptableEventWithoutCrashing(t *testing.T) {
	const room = id.RoomID("!enc:matrix.example.org")
	const sender = id.UserID("@alice:matrix.example.org")

	encEvt := &event.Event{
		ID:      "$enc1",
		Sender:  sender,
		Type:    event.EventEncrypted,
		Content: event.Content{Parsed: &event.EncryptedEventContent{Algorithm: id.AlgorithmMegolmV1, SessionID: "sess1"}},
	}
	firstSync := &mautrix.RespSync{
		NextBatch: "s1",
		Rooms: mautrix.RespSyncRooms{
			Join: map[id.RoomID]*mautrix.SyncJoinedRoom{
				room: {Timeline: mautrix.SyncTimeline{SyncEventsList: mautrix.SyncEventsList{Events: []*event.Event{encEvt}}}},
			},
		},
	}

	srv, _ := newFakeHomeserver(t, []*mautrix.RespSync{firstSync})
	// No crypto helper wired: every m.room.encrypted event must degrade to
	// an undecryptable Item, never a crash or a dropped event.
	adapter := newTestAdapter(t, srv, nil)
	sink := newMemSink()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	var item core.Item
	select {
	case item = <-sink.upserts:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the undecryptable item")
	}
	cancel()
	<-done

	if item.Meta["undecryptable"] != "true" {
		t.Errorf("item.Meta[undecryptable] = %q, want true", item.Meta["undecryptable"])
	}
	if item.Body != "" {
		t.Errorf("item.Body = %q, want empty for an undecryptable event", item.Body)
	}
}

func TestAdapterSendText(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)

	receipt, err := adapter.Send(context.Background(), core.Outgoing{
		Channel: core.ChannelMatrix,
		Account: "work",
		To:      []string{"!room:matrix.example.org"},
		Body:    "hola",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if receipt.ID == "" {
		t.Error("Receipt.ID is empty")
	}
	if receipt.Channel != core.ChannelMatrix {
		t.Errorf("Receipt.Channel = %q, want matrix", receipt.Channel)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sentEvents) != 1 {
		t.Fatalf("sentEvents = %d, want 1", len(state.sentEvents))
	}
	if !strings.Contains(state.sentEvents[0].path, "/send/m.room.message/") {
		t.Errorf("sent path = %q, want a m.room.message send", state.sentEvents[0].path)
	}
	var content event.MessageEventContent
	if err := json.Unmarshal(state.sentEvents[0].body, &content); err != nil {
		t.Fatalf("decode sent content: %v", err)
	}
	if content.Body != "hola" {
		t.Errorf("sent body = %q, want hola", content.Body)
	}
}

// TestAdapterSendRejectsMultipleRecipients covers T12(d): Matrix targets
// exactly one room. Send must report ErrUnsupported instead of silently
// using out.To[0] and dropping the rest.
func TestAdapterSendRejectsMultipleRecipients(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)

	_, err := adapter.Send(context.Background(), core.Outgoing{
		Channel: core.ChannelMatrix,
		Account: "work",
		To:      []string{"!room1:matrix.example.org", "!room2:matrix.example.org"},
		Body:    "hola",
	})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sentEvents) != 0 {
		t.Fatalf("sentEvents = %d, want 0 (must not send to only the first room)", len(state.sentEvents))
	}
}

// TestAdapterSendRejectsCc covers T12(d): Matrix has no Cc concept. Send
// must report ErrUnsupported instead of silently dropping it.
func TestAdapterSendRejectsCc(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)

	_, err := adapter.Send(context.Background(), core.Outgoing{
		Channel: core.ChannelMatrix,
		Account: "work",
		To:      []string{"!room:matrix.example.org"},
		Cc:      []string{"!room2:matrix.example.org"},
		Body:    "hola",
	})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sentEvents) != 0 {
		t.Fatalf("sentEvents = %d, want 0 (must not silently drop Cc)", len(state.sentEvents))
	}
}

func TestAdapterSendReplySetsInReplyTo(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)

	replyItemID := itemID("work", "!room:matrix.example.org", "$original")
	_, err := adapter.Send(context.Background(), core.Outgoing{
		Channel: core.ChannelMatrix,
		Account: "work",
		Thread:  "!room:matrix.example.org",
		ReplyTo: replyItemID,
		Body:    "reply body",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	var content event.MessageEventContent
	if err := json.Unmarshal(state.sentEvents[0].body, &content); err != nil {
		t.Fatalf("decode sent content: %v", err)
	}
	if content.RelatesTo == nil || content.RelatesTo.InReplyTo == nil {
		t.Fatal("sent content has no m.relates_to/m.in_reply_to")
	}
	if content.RelatesTo.InReplyTo.EventID != "$original" {
		t.Errorf("in_reply_to.event_id = %q, want $original", content.RelatesTo.InReplyTo.EventID)
	}
}

func TestAdapterOrganizeMarksRoomRead(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)

	seen := true
	target := itemID("work", "!room:matrix.example.org", "$evt1")
	if err := adapter.Organize(context.Background(), target, core.OrganizeOp{Seen: &seen}); err != nil {
		t.Fatalf("Organize: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.readMarkers) != 1 {
		t.Fatalf("readMarkers = %d, want 1", len(state.readMarkers))
	}
}

func TestAdapterOrganizeRejectsLabels(t *testing.T) {
	srv, _ := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)

	target := itemID("work", "!room:matrix.example.org", "$evt1")
	err := adapter.Organize(context.Background(), target, core.OrganizeOp{AddLabels: []string{"important"}})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("Organize with labels: err = %v, want core.ErrUnsupported", err)
	}
}
