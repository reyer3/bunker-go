package matrix

import (
	"context"
	"encoding/json"
	"testing"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// buildEncryptedEventJSON builds the raw JSON body client.GetEvent would
// receive for a real megolm-encrypted event, sharing history the same way
// crypto_roundtrip_test.go does: the plaintext is genuinely encrypted by
// ogs and can only be recovered by a machine holding the matching inbound
// session -- there is no shortcut standing in for the crypto here.
func buildEncryptedEventJSON(t *testing.T, room id.RoomID, sender id.UserID, eventID id.EventID, ogs *crypto.OutboundGroupSession, senderIdentity *id.Device, body string) []byte {
	t.Helper()
	plaintext, err := json.Marshal(map[string]any{
		"room_id": room,
		"type":    "m.room.message",
		"content": map[string]any{"msgtype": "m.text", "body": body},
	})
	if err != nil {
		t.Fatalf("marshal plaintext: %v", err)
	}
	ciphertext, err := ogs.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("ogs.Encrypt: %v", err)
	}
	evt := &event.Event{
		ID:     eventID,
		Sender: sender,
		RoomID: room,
		Type:   event.EventEncrypted,
		Content: event.Content{Parsed: &event.EncryptedEventContent{
			Algorithm:        id.AlgorithmMegolmV1,
			SenderKey:        senderIdentity.IdentityKey,
			SessionID:        ogs.ID(),
			MegolmCiphertext: ciphertext,
		}},
	}
	raw, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return raw
}

func TestRetryUndecryptableRefetchesAndDecryptsStoredItems(t *testing.T) {
	const room = id.RoomID("!enc:matrix.example.org")
	const sender = id.UserID("@alice:matrix.example.org")
	const eventID = id.EventID("$enc1")

	senderMach := newTestOlmMachine(t, sender)
	receiverMach := newTestOlmMachine(t, "@alice:example.com")

	ogs, err := crypto.NewOutboundGroupSession(room, nil, nil)
	if err != nil {
		t.Fatalf("NewOutboundGroupSession: %v", err)
	}
	ogs.Shared = true
	shareContent, ok := ogs.ShareContent().Parsed.(*event.RoomKeyEventContent)
	if !ok {
		t.Fatalf("ShareContent().Parsed is %T, want *event.RoomKeyEventContent", ogs.ShareContent().Parsed)
	}
	senderIdentity := senderMach.OwnIdentity()
	igs, err := crypto.NewInboundGroupSession(senderIdentity.IdentityKey, senderIdentity.SigningKey, room, shareContent.SessionKey, 0, 0, nil, false)
	if err != nil {
		t.Fatalf("NewInboundGroupSession: %v", err)
	}
	// This is the moment a recovery-key/key-export import represents: the
	// megolm session becomes available in the crypto store AFTER the
	// event was first seen (and stored undecryptable).
	if err := receiverMach.CryptoStore.PutGroupSession(context.Background(), igs); err != nil {
		t.Fatalf("PutGroupSession: %v", err)
	}

	rawEvt := buildEncryptedEventJSON(t, room, sender, eventID, ogs, senderIdentity, "recuperado")

	srv, state := newFakeHomeserver(t, nil)
	state.getEventResponses = map[string]json.RawMessage{string(eventID): rawEvt}

	adapter := newTestAdapter(t, srv, &machineCryptoHelper{mach: receiverMach})
	store := newMemSink()
	stillDecryptableID := itemID("work", room, eventID)
	store.items[stillDecryptableID] = core.Item{
		ID:      stillDecryptableID,
		Channel: core.ChannelMatrix,
		Account: "work",
		Meta:    map[string]string{"undecryptable": "true"},
	}
	// A decoy item that must be left alone: not undecryptable, so
	// RetryUndecryptable must never even try to refetch it.
	decoyID := itemID("work", room, "$plain1")
	store.items[decoyID] = core.Item{ID: decoyID, Channel: core.ChannelMatrix, Account: "work", Body: "already fine"}

	if err := adapter.RetryUndecryptable(context.Background(), store); err != nil {
		t.Fatalf("RetryUndecryptable: %v", err)
	}

	got, err := store.Get(context.Background(), stillDecryptableID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Body != "recuperado" {
		t.Errorf("Body = %q, want %q (decrypted)", got.Body, "recuperado")
	}
	if got.Meta["undecryptable"] == "true" {
		t.Errorf("Meta[undecryptable] still true, want it cleared after a successful decrypt")
	}

	decoy, err := store.Get(context.Background(), decoyID)
	if err != nil {
		t.Fatalf("Get(decoy): %v", err)
	}
	if decoy.Body != "already fine" {
		t.Errorf("decoy item was touched: Body = %q", decoy.Body)
	}
}

func TestRetryUndecryptableLeavesItemAloneWhenStillUndecryptable(t *testing.T) {
	const room = id.RoomID("!enc:matrix.example.org")
	const sender = id.UserID("@alice:matrix.example.org")
	const eventID = id.EventID("$enc1")

	// No crypto session shared this time: the fetched event still cannot
	// be decrypted, so the item must stay marked undecryptable rather
	// than being upserted with an empty body.
	senderMach := newTestOlmMachine(t, sender)
	receiverMach := newTestOlmMachine(t, "@alice:example.com")
	ogs, err := crypto.NewOutboundGroupSession(room, nil, nil)
	if err != nil {
		t.Fatalf("NewOutboundGroupSession: %v", err)
	}
	ogs.Shared = true
	senderIdentity := senderMach.OwnIdentity()
	rawEvt := buildEncryptedEventJSON(t, room, sender, eventID, ogs, senderIdentity, "still secret")

	srv, state := newFakeHomeserver(t, nil)
	state.getEventResponses = map[string]json.RawMessage{string(eventID): rawEvt}

	adapter := newTestAdapter(t, srv, &machineCryptoHelper{mach: receiverMach})
	store := newMemSink()
	targetID := itemID("work", room, eventID)
	store.items[targetID] = core.Item{ID: targetID, Channel: core.ChannelMatrix, Account: "work", Meta: map[string]string{"undecryptable": "true"}}

	if err := adapter.RetryUndecryptable(context.Background(), store); err != nil {
		t.Fatalf("RetryUndecryptable: %v", err)
	}

	got, err := store.Get(context.Background(), targetID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Meta["undecryptable"] != "true" {
		t.Errorf("Meta[undecryptable] = %q, want it to stay true (no session to decrypt with)", got.Meta["undecryptable"])
	}
}

func TestRetryUndecryptableNoOpWithoutCryptoHelper(t *testing.T) {
	srv, _ := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil) // no crypto helper wired
	store := newMemSink()
	store.items["matrix:work:!r:$e"] = core.Item{ID: "matrix:work:!r:$e", Channel: core.ChannelMatrix, Account: "work", Meta: map[string]string{"undecryptable": "true"}}

	if err := adapter.RetryUndecryptable(context.Background(), store); err != nil {
		t.Fatalf("RetryUndecryptable: %v", err)
	}
}
