package matrix

import (
	"context"
	"strings"
	"testing"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// Issue #114: Send used to return the raw event id, so the optimistic item
// core.Service stored under it could not be edited, deleted or reacted to
// by id, and the sync echo landed as a second row. These tests drive the
// real adapter behind core.Service against the fake homeserver.

func newSendIDService(t *testing.T, a *Adapter) (*core.Service, *memSink) {
	t.Helper()
	sink := newMemSink()
	// What Run does first; reactions persist their records through it.
	a.sink = sink
	reg := core.NewRegistry()
	reg.Register(a)
	return core.NewService(sink, reg), sink
}

func (s *memSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

func ownEcho(evtID id.EventID, body string) *event.Event {
	return &event.Event{
		ID: evtID, RoomID: relRoom, Sender: relSelf, Type: event.EventMessage, Timestamp: 1700000000000,
		Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: body}},
	}
}

func TestSendReturnsItemIDThatEditDeleteAndReactAccept(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	a := newTestAdapter(t, srv, nil)
	svc, sink := newSendIDService(t, a)
	ctx := context.Background()

	_, receipt, err := svc.Send(ctx, core.Outgoing{Channel: core.ChannelMatrix, Account: "work", To: []string{relRoom.String()}, Body: "hola"}, false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if want := itemID("work", relRoom, "$sent1"); receipt.ID != want {
		t.Fatalf("receipt.ID = %q, want the item id %q", receipt.ID, want)
	}
	if got := sink.get(t, "$sent1"); !got.FromMe || got.Body != "hola" {
		t.Fatalf("stored sent item = %+v", got)
	}

	if _, _, err := svc.EditMessage(ctx, receipt.ID, "hola, corregido", false); err != nil {
		t.Fatalf("EditMessage(receipt.ID): %v", err)
	}
	if _, _, err := svc.React(ctx, receipt.ID, "👍", false); err != nil {
		t.Fatalf("React(receipt.ID): %v", err)
	}
	if _, _, err := svc.DeleteMessage(ctx, receipt.ID, false); err != nil {
		t.Fatalf("DeleteMessage(receipt.ID): %v", err)
	}

	_, edit := sentContent(t, state, 1)
	if rel, _ := edit["m.relates_to"].(map[string]any); rel["event_id"] != "$sent1" {
		t.Errorf("edit relates to %v, want $sent1", edit["m.relates_to"])
	}
	_, reaction := sentContent(t, state, 2)
	if rel, _ := reaction["m.relates_to"].(map[string]any); rel["event_id"] != "$sent1" {
		t.Errorf("reaction relates to %v, want $sent1", reaction["m.relates_to"])
	}
	if got := redactions(state); len(got) != 1 || !strings.Contains(got[0], "/redact/$sent1/") {
		t.Errorf("redactions = %v, want one of $sent1", got)
	}
	if got := sink.get(t, "$sent1"); !got.Deleted {
		t.Errorf("after delete = %+v, want Deleted", got)
	}
	if n := sink.count(); n != 1 {
		t.Errorf("store has %d items, want 1", n)
	}
}

func TestReplyReturnsItemID(t *testing.T) {
	srv, _ := newFakeHomeserver(t, nil)
	a := newTestAdapter(t, srv, nil)
	svc, sink := newSendIDService(t, a)
	ctx := context.Background()
	original := core.Item{ID: itemID("work", relRoom, "$b1"), Channel: core.ChannelMatrix, Account: "work", Thread: relRoom.String(), From: core.Address{ID: relBob.String()}, Body: "hola"}
	if err := sink.Upsert(ctx, original); err != nil {
		t.Fatal(err)
	}

	_, receipt, err := svc.Reply(ctx, original.ID, "qué tal", nil, nil, false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if want := itemID("work", relRoom, "$sent1"); receipt.ID != want {
		t.Fatalf("receipt.ID = %q, want %q", receipt.ID, want)
	}
	if _, _, err := svc.EditMessage(ctx, receipt.ID, "qué tal?", false); err != nil {
		t.Errorf("EditMessage(reply receipt): %v", err)
	}
}

func TestSendMediaReturnsItemID(t *testing.T) {
	srv, _ := newMediaFakeHomeserver(t, nil)
	a := newMediaTestAdapter(t, srv)
	path := writeMatrixFile(t, "nota.txt", []byte("hola"))
	receipt, err := a.SendMedia(context.Background(), core.Outgoing{Channel: core.ChannelMatrix, Account: "work", To: []string{relRoom.String()}, Attachments: []string{path}, Body: "pie"})
	if err != nil {
		t.Fatalf("SendMedia: %v", err)
	}
	// The trailing text event is the last one sent, so it names the send.
	if want := itemID("work", relRoom, "$media-sent2"); receipt.ID != want {
		t.Errorf("receipt.ID = %q, want %q", receipt.ID, want)
	}
}

// TestSendEchoIsTheSameItem feeds our own sync echo back after a send: it
// lands on the sent item's row, never a second one.
func TestSendEchoIsTheSameItem(t *testing.T) {
	srv, _ := newFakeHomeserver(t, nil)
	a := newTestAdapter(t, srv, nil)
	svc, sink := newSendIDService(t, a)
	ctx := context.Background()

	_, receipt, err := svc.Send(ctx, core.Outgoing{Channel: core.ChannelMatrix, Account: "work", To: []string{relRoom.String()}, Body: "hola"}, false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	a.messageHandler(sink)(ctx, ownEcho("$sent1", "hola"))

	if n := sink.count(); n != 1 {
		t.Fatalf("store has %d items after the echo, want 1", n)
	}
	got := sink.get(t, "$sent1")
	if got.ID != receipt.ID || !got.FromMe || got.From.ID != relSelf.String() {
		t.Errorf("item after echo = %+v, want our message under %q", got, receipt.ID)
	}
	if _, _, err := svc.EditMessage(ctx, receipt.ID, "hola, corregido", false); err != nil {
		t.Errorf("EditMessage after echo: %v", err)
	}
}

// TestSendEchoInEncryptedRoomIsTheSameItem sends into an encrypted room,
// then feeds back the exact ciphertext that went on the wire as our own
// m.room.encrypted echo: decrypted, it keys to the same item.
func TestSendEchoInEncryptedRoomIsTheSameItem(t *testing.T) {
	ctx := context.Background()
	mach := newTestOlmMachine(t, relSelf)
	ogs, err := crypto.NewOutboundGroupSession(relRoom, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ogs.Shared = true
	// Our own device keeps the inbound half of its outbound session, as
	// the real crypto machine does, so it can read its own echo.
	share := ogs.ShareContent().Parsed.(*event.RoomKeyEventContent)
	identity := mach.OwnIdentity()
	igs, err := crypto.NewInboundGroupSession(identity.IdentityKey, identity.SigningKey, relRoom, share.SessionKey, 0, 0, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := mach.CryptoStore.PutGroupSession(ctx, igs); err != nil {
		t.Fatal(err)
	}

	srv, state := newFakeHomeserver(t, nil)
	a := newTestAdapter(t, srv, &machineCryptoHelper{mach: mach, outbound: ogs})
	if err := a.client.StateStore.SetEncryptionEvent(ctx, relRoom, &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}); err != nil {
		t.Fatal(err)
	}
	svc, sink := newSendIDService(t, a)

	_, receipt, err := svc.Send(ctx, core.Outgoing{Channel: core.ChannelMatrix, Account: "work", To: []string{relRoom.String()}, Body: "secreto"}, false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if want := itemID("work", relRoom, "$sent1"); receipt.ID != want {
		t.Fatalf("receipt.ID = %q, want %q", receipt.ID, want)
	}

	path, _ := sentContent(t, state, 0)
	if !strings.Contains(path, "/send/m.room.encrypted/") {
		t.Fatalf("sent path = %q, want it encrypted", path)
	}
	state.mu.Lock()
	var enc event.EncryptedEventContent
	err = enc.UnmarshalJSON(state.sentEvents[0].body)
	state.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	a.encryptedHandler(sink)(ctx, &event.Event{
		ID: "$sent1", RoomID: relRoom, Sender: relSelf, Type: event.EventEncrypted, Timestamp: 1700000000000,
		Content: event.Content{Parsed: &enc},
	})

	if n := sink.count(); n != 1 {
		t.Fatalf("store has %d items after the encrypted echo, want 1", n)
	}
	got := sink.get(t, "$sent1")
	if got.Meta["undecryptable"] == "true" || got.Body != "secreto" || !got.FromMe {
		t.Errorf("item after encrypted echo = %+v, want our decrypted message", got)
	}
	if _, _, err := svc.React(ctx, receipt.ID, "🔒", false); err != nil {
		t.Errorf("React after encrypted echo: %v", err)
	}
}
