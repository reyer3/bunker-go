package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

const (
	relRoom  = id.RoomID("!rel:matrix.example.org")
	relBob   = id.UserID("@bob:matrix.example.org")
	relCarol = id.UserID("@carol:matrix.example.org")
	relSelf  = id.UserID("@alice:example.com") // newTestAdapter's own user
	sentinel = id.EventID("$sentinel")
)

// lockedBuffer is an io.Writer the adapter's sync goroutine writes log
// lines to while the test reads them.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs routes slog's default logger (core.LogSinkError) and, since
// slog.SetDefault also redirects the log package, the adapter's
// log.Printf lines into one buffer for the test's duration.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func textMsg(evtID id.EventID, sender id.UserID, body string) *event.Event {
	return &event.Event{
		ID:        evtID,
		Sender:    sender,
		Type:      event.EventMessage,
		Timestamp: 1700000000000,
		Content:   event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: body}},
	}
}

func editMsg(evtID id.EventID, sender id.UserID, replaces id.EventID, body string) *event.Event {
	return &event.Event{
		ID:        evtID,
		Sender:    sender,
		Type:      event.EventMessage,
		Timestamp: 1700000001000,
		Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType:    event.MsgText,
			Body:       "* " + body,
			NewContent: &event.MessageEventContent{MsgType: event.MsgText, Body: body},
			RelatesTo:  (&event.RelatesTo{}).SetReplace(replaces),
		}},
	}
}

func reactionEvt(evtID id.EventID, sender id.UserID, target id.EventID, key string) *event.Event {
	return &event.Event{
		ID:        evtID,
		Sender:    sender,
		Type:      event.EventReaction,
		Timestamp: 1700000002000,
		Content: event.Content{Parsed: &event.ReactionEventContent{
			RelatesTo: event.RelatesTo{Type: event.RelAnnotation, EventID: target, Key: key},
		}},
	}
}

func redactionEvt(evtID id.EventID, sender id.UserID, redacts id.EventID) *event.Event {
	return &event.Event{
		ID:        evtID,
		Sender:    sender,
		Type:      event.EventRedaction,
		Timestamp: 1700000003000,
		Redacts:   redacts,
		Content:   event.Content{Parsed: &event.RedactionEventContent{}},
	}
}

// syncRoom runs adapter over one /sync carrying events in relRoom,
// followed by a sentinel message: handlers run in timeline order on one
// goroutine, so once the sentinel is upserted every earlier event has
// been handled.
func syncRoom(t *testing.T, adapter *Adapter, sink core.Sink, upserts <-chan core.Item, events ...*event.Event) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	want := itemID("work", relRoom, sentinel)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case item := <-upserts:
			if item.ID == want {
				cancel()
				<-done
				return
			}
		case <-deadline:
			cancel()
			t.Fatal("timed out waiting for the sentinel message")
		}
	}
}

func relationSync(events ...*event.Event) []*mautrix.RespSync {
	events = append(events, textMsg(sentinel, relBob, "fin"))
	return []*mautrix.RespSync{{
		NextBatch: "s1",
		Rooms: mautrix.RespSyncRooms{Join: map[id.RoomID]*mautrix.SyncJoinedRoom{
			relRoom: {Timeline: mautrix.SyncTimeline{SyncEventsList: mautrix.SyncEventsList{Events: events}}},
		}},
	}}
}

// runRelations syncs events through a fresh adapter and memSink and
// returns the sink for inspection.
func runRelations(t *testing.T, events ...*event.Event) (*Adapter, *memSink) {
	t.Helper()
	srv, _ := newFakeHomeserver(t, relationSync(events...))
	adapter := newTestAdapter(t, srv, nil)
	sink := newMemSink()
	syncRoom(t, adapter, sink, sink.upserts)
	return adapter, sink
}

func (s *memSink) get(t *testing.T, evtID id.EventID) core.Item {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[itemID("work", relRoom, evtID)]
	if !ok {
		t.Fatalf("no stored item for %s", evtID)
	}
	return item
}

// assertNoItems fails if any of evtIDs was stored as a timeline item of
// its own: edits, reactions and redactions only modify other items.
func (s *memSink) assertNoItems(t *testing.T, evtIDs ...id.EventID) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, evtID := range evtIDs {
		if _, ok := s.items[itemID("work", relRoom, evtID)]; ok {
			t.Errorf("%s was stored as a timeline item, want it applied to its target only", evtID)
		}
	}
}

func TestEditUpdatesOriginalBody(t *testing.T) {
	adapter, sink := runRelations(t,
		textMsg("$m1", relBob, "hola mundo"),
		editMsg("$e1", relBob, "$m1", "hola, mundo"),
	)

	got := sink.get(t, "$m1")
	if got.Body != "hola, mundo" || !got.Edited {
		t.Errorf("original = {Body: %q, Edited: %v}, want {hola, mundo, true}", got.Body, got.Edited)
	}
	sink.assertNoItems(t, "$e1")

	fetched, err := adapter.Fetch(context.Background(), itemID("work", relRoom, "$m1"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fetched.Body != "hola, mundo" {
		t.Errorf("Fetch body = %q, want the edited body", fetched.Body)
	}
}

func TestEditFromAnotherSenderIsIgnored(t *testing.T) {
	logs := captureLogs(t)
	_, sink := runRelations(t,
		textMsg("$m1", relBob, "original"),
		editMsg("$e1", relCarol, "$m1", "falsificado"),
	)

	got := sink.get(t, "$m1")
	if got.Body != "original" || got.Edited {
		t.Errorf("original = {Body: %q, Edited: %v}, want it untouched", got.Body, got.Edited)
	}
	sink.assertNoItems(t, "$e1")
	if !strings.Contains(logs.String(), "is not the original sender") {
		t.Errorf("logs = %q, want the ignored edit logged", logs.String())
	}
}

func TestEditOfEditTargetsOriginal(t *testing.T) {
	_, sink := runRelations(t,
		textMsg("$m1", relBob, "v1"),
		editMsg("$e1", relBob, "$m1", "v2"),
		// A client that points the next edit at the previous edit rather
		// than the original: it still lands on $m1.
		editMsg("$e2", relBob, "$e1", "v3"),
	)

	if got := sink.get(t, "$m1"); got.Body != "v3" {
		t.Errorf("original body = %q, want v3", got.Body)
	}
	sink.assertNoItems(t, "$e1", "$e2")
}

// TestEditOfUncachedMessageFetchesOriginal covers an edit arriving after
// a restart, when the original is only in the store: the sender check
// and edit-of-edit resolution fall back to fetching the edited events.
func TestEditOfUncachedMessageFetchesOriginal(t *testing.T) {
	srv, state := newFakeHomeserver(t, relationSync(editMsg("$e2", relBob, "$e1", "v3")))
	mustJSON := func(evt *event.Event) json.RawMessage {
		evt.RoomID = relRoom
		raw, err := json.Marshal(evt)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return raw
	}
	state.getEventResponses = map[string]json.RawMessage{
		"$m1": mustJSON(textMsg("$m1", relBob, "v1")),
		"$e1": mustJSON(editMsg("$e1", relBob, "$m1", "v2")),
	}
	adapter := newTestAdapter(t, srv, nil)
	sink := newMemSink()
	id1 := itemID("work", relRoom, "$m1")
	sink.items[id1] = core.Item{ID: id1, Channel: core.ChannelMatrix, Account: "work", Body: "v2"}

	syncRoom(t, adapter, sink, sink.upserts)

	if got := sink.get(t, "$m1"); got.Body != "v3" {
		t.Errorf("original body = %q, want v3", got.Body)
	}
}

func TestReactionIsAdded(t *testing.T) {
	_, sink := runRelations(t,
		textMsg("$m1", relBob, "hola"),
		reactionEvt("$r1", relCarol, "$m1", "👍"),
		// Our own account reacting from another device is handled the
		// same way.
		reactionEvt("$r2", relSelf, "$m1", "🎉"),
	)

	got := sink.get(t, "$m1")
	want := map[string]string{relCarol.String(): "👍", relSelf.String(): "🎉"}
	if len(got.Reactions) != len(want) {
		t.Fatalf("Reactions = %+v, want %v", got.Reactions, want)
	}
	for _, r := range got.Reactions {
		if want[r.Sender] != r.Emoji {
			t.Errorf("reaction %+v, want %v", r, want)
		}
	}
	sink.assertNoItems(t, "$r1", "$r2")
}

func TestReactionRedactionRemovesIt(t *testing.T) {
	_, sink := runRelations(t,
		textMsg("$m1", relBob, "hola"),
		reactionEvt("$r1", relCarol, "$m1", "👍"),
		redactionEvt("$x1", relCarol, "$r1"),
	)

	got := sink.get(t, "$m1")
	if len(got.Reactions) != 0 {
		t.Errorf("Reactions = %+v, want none after the redaction", got.Reactions)
	}
	if got.Deleted || got.Body != "hola" {
		t.Errorf("message = {Body: %q, Deleted: %v}, want it untouched by a reaction's redaction", got.Body, got.Deleted)
	}
	sink.assertNoItems(t, "$r1", "$x1")
}

// TestReactionRedactionKeepsUsersOtherReaction covers a user with two
// live annotations on one message: core keeps one per sender, so
// redacting the shown one falls back to the other, and redacting the
// hidden one changes nothing.
func TestReactionRedactionKeepsUsersOtherReaction(t *testing.T) {
	_, sink := runRelations(t,
		textMsg("$m1", relBob, "hola"),
		reactionEvt("$r1", relCarol, "$m1", "👍"),
		reactionEvt("$r2", relCarol, "$m1", "❤️"),
		reactionEvt("$r3", relCarol, "$m1", "😂"),
		redactionEvt("$x1", relCarol, "$r1"), // hidden: 😂 stays
		redactionEvt("$x3", relCarol, "$r3"), // shown: falls back to ❤️
	)

	got := sink.get(t, "$m1")
	if len(got.Reactions) != 1 || got.Reactions[0] != (core.Reaction{Sender: relCarol.String(), Emoji: "❤️"}) {
		t.Errorf("Reactions = %+v, want only carol's ❤️", got.Reactions)
	}
}

// TestReactionRedactionAfterRestart proves the reaction mapping survives
// a restart: a fresh adapter (empty memory) sharing the sink's persisted
// cursors still knows which reaction a redaction removes.
func TestReactionRedactionAfterRestart(t *testing.T) {
	srv1, _ := newFakeHomeserver(t, relationSync(
		textMsg("$m1", relBob, "hola"),
		reactionEvt("$r1", relCarol, "$m1", "👍"),
	))
	sink := newMemSink()
	syncRoom(t, newTestAdapter(t, srv1, nil), sink, sink.upserts)
	if got := sink.get(t, "$m1"); len(got.Reactions) != 1 {
		t.Fatalf("Reactions before restart = %+v, want one", got.Reactions)
	}

	srv2, _ := newFakeHomeserver(t, relationSync(redactionEvt("$x1", relCarol, "$r1")))
	syncRoom(t, newTestAdapter(t, srv2, nil), sink, sink.upserts)

	got := sink.get(t, "$m1")
	if len(got.Reactions) != 0 {
		t.Errorf("Reactions after restart = %+v, want none", got.Reactions)
	}
	if got.Deleted {
		t.Error("message revoked: the reaction's redaction was mistaken for the message's")
	}
}

func TestMessageRedactionRevokesIt(t *testing.T) {
	v11 := redactionEvt("$x2", relBob, "")
	// Room v11 carries "redacts" in content instead of at the top level.
	v11.Content = event.Content{Parsed: &event.RedactionEventContent{Redacts: "$m2"}}

	_, sink := runRelations(t,
		textMsg("$m1", relBob, "borrame"),
		textMsg("$m2", relSelf, "yo también"),
		redactionEvt("$x1", relBob, "$m1"),
		v11,
	)

	for _, evtID := range []id.EventID{"$m1", "$m2"} {
		got := sink.get(t, evtID)
		if !got.Deleted || got.Body != "" {
			t.Errorf("%s = {Body: %q, Deleted: %v}, want revoked", evtID, got.Body, got.Deleted)
		}
	}
	sink.assertNoItems(t, "$x1", "$x2")
}

// failingSink fails the three relation writes so the test can prove the
// failures are logged rather than dropped.
type failingSink struct {
	*memSink
}

var errSinkDown = errors.New("disco lleno")

func (failingSink) EditItem(context.Context, string, string) error { return errSinkDown }
func (failingSink) RevokeItem(context.Context, string) error       { return errSinkDown }
func (failingSink) SetReaction(context.Context, string, core.Reaction) error {
	return errSinkDown
}

func TestRelationSinkErrorsAreLogged(t *testing.T) {
	logs := captureLogs(t)
	srv, _ := newFakeHomeserver(t, relationSync(
		textMsg("$m1", relBob, "hola"),
		editMsg("$e1", relBob, "$m1", "hola!"),
		reactionEvt("$r1", relCarol, "$m1", "👍"),
		redactionEvt("$x1", relBob, "$m1"),
	))
	mem := newMemSink()
	syncRoom(t, newTestAdapter(t, srv, nil), failingSink{mem}, mem.upserts)

	out := logs.String()
	for _, op := range []string{"op=edit_item", "op=set_reaction", "op=revoke_item"} {
		if !strings.Contains(out, op) {
			t.Errorf("logs lack %s: %s", op, out)
		}
	}
	if !strings.Contains(out, errSinkDown.Error()) || !strings.Contains(out, "$m1") {
		t.Errorf("logs lack the error and target item: %s", out)
	}
}

// sharedMegolm sets up an outbound megolm session whose inbound half is
// already in the receiving machine's store, the same shortcut
// crypto_roundtrip_test.go takes for the key-sharing dance.
func sharedMegolm(t *testing.T, sender id.UserID) (*crypto.OutboundGroupSession, *id.Device, *crypto.OlmMachine) {
	t.Helper()
	senderMach := newTestOlmMachine(t, sender)
	receiverMach := newTestOlmMachine(t, relSelf)
	ogs, err := crypto.NewOutboundGroupSession(relRoom, nil, nil)
	if err != nil {
		t.Fatalf("NewOutboundGroupSession: %v", err)
	}
	ogs.Shared = true
	share := ogs.ShareContent().Parsed.(*event.RoomKeyEventContent)
	identity := senderMach.OwnIdentity()
	igs, err := crypto.NewInboundGroupSession(identity.IdentityKey, identity.SigningKey, relRoom, share.SessionKey, 0, 0, nil, false)
	if err != nil {
		t.Fatalf("NewInboundGroupSession: %v", err)
	}
	if err := receiverMach.CryptoStore.PutGroupSession(context.Background(), igs); err != nil {
		t.Fatalf("PutGroupSession: %v", err)
	}
	return ogs, identity, receiverMach
}

// encrypt wraps a plaintext event of evtType in a genuine m.room.encrypted
// event.
func encrypt(t *testing.T, ogs *crypto.OutboundGroupSession, identity *id.Device, evtID id.EventID, sender id.UserID, evtType event.Type, content any) *event.Event {
	t.Helper()
	plaintext, err := json.Marshal(map[string]any{"room_id": relRoom, "type": evtType.Type, "content": content})
	if err != nil {
		t.Fatalf("marshal plaintext: %v", err)
	}
	ciphertext, err := ogs.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("ogs.Encrypt: %v", err)
	}
	return &event.Event{
		ID:        evtID,
		Sender:    sender,
		RoomID:    relRoom,
		Type:      event.EventEncrypted,
		Timestamp: 1700000001000,
		Content: event.Content{Parsed: &event.EncryptedEventContent{
			Algorithm:        id.AlgorithmMegolmV1,
			SenderKey:        identity.IdentityKey,
			SessionID:        ogs.ID(),
			MegolmCiphertext: ciphertext,
		}},
	}
}

func TestEncryptedEditAndReactionAreApplied(t *testing.T) {
	ogs, identity, receiver := sharedMegolm(t, relBob)
	encMsg := encrypt(t, ogs, identity, "$m1", relBob, event.EventMessage,
		map[string]any{"msgtype": "m.text", "body": "secreto"})
	encEdit := encrypt(t, ogs, identity, "$e1", relBob, event.EventMessage, map[string]any{
		"msgtype":       "m.text",
		"body":          "* secreto corregido",
		"m.new_content": map[string]any{"msgtype": "m.text", "body": "secreto corregido"},
		"m.relates_to":  map[string]any{"rel_type": "m.replace", "event_id": "$m1"},
	})
	encReaction := encrypt(t, ogs, identity, "$r1", relBob, event.EventReaction, map[string]any{
		"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": "$m1", "key": "🔒"},
	})

	srv, _ := newFakeHomeserver(t, relationSync(encMsg, encEdit, encReaction))
	adapter := newTestAdapter(t, srv, &machineCryptoHelper{mach: receiver})
	sink := newMemSink()
	syncRoom(t, adapter, sink, sink.upserts)

	got := sink.get(t, "$m1")
	if got.Body != "secreto corregido" || !got.Edited {
		t.Errorf("original = {Body: %q, Edited: %v}, want the decrypted edit applied", got.Body, got.Edited)
	}
	if len(got.Reactions) != 1 || got.Reactions[0] != (core.Reaction{Sender: relBob.String(), Emoji: "🔒"}) {
		t.Errorf("Reactions = %+v, want bob's 🔒", got.Reactions)
	}
	sink.assertNoItems(t, "$e1", "$r1")
}

// TestRetryUndecryptableAppliesLateRelation covers an encrypted reaction
// first stored as an undecryptable placeholder: once its session arrives,
// the retry applies it to its target and removes the placeholder.
func TestRetryUndecryptableAppliesLateRelation(t *testing.T) {
	ogs, identity, receiver := sharedMegolm(t, relBob)
	encReaction := encrypt(t, ogs, identity, "$r1", relBob, event.EventReaction, map[string]any{
		"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": "$m1", "key": "👀"},
	})
	raw, err := json.Marshal(encReaction)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	srv, state := newFakeHomeserver(t, nil)
	state.getEventResponses = map[string]json.RawMessage{"$r1": raw}
	adapter := newTestAdapter(t, srv, &machineCryptoHelper{mach: receiver})
	store := newMemSink()
	msgID := itemID("work", relRoom, "$m1")
	placeholderID := itemID("work", relRoom, "$r1")
	store.items[msgID] = core.Item{ID: msgID, Channel: core.ChannelMatrix, Account: "work", Body: "hola"}
	store.items[placeholderID] = core.Item{ID: placeholderID, Channel: core.ChannelMatrix, Account: "work", Meta: map[string]string{"undecryptable": "true"}}

	if err := adapter.RetryUndecryptable(context.Background(), store); err != nil {
		t.Fatalf("RetryUndecryptable: %v", err)
	}

	store.assertNoItems(t, "$r1")
	if got := store.get(t, "$m1"); len(got.Reactions) != 1 || got.Reactions[0].Emoji != "👀" {
		t.Errorf("Reactions = %+v, want bob's 👀", got.Reactions)
	}
}
