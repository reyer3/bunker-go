package matrix

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// machineCryptoHelper is a minimal mautrix.CryptoHelper backed directly by
// a *crypto.OlmMachine, standing in for cryptohelper.CryptoHelper in
// tests. It skips real device discovery/key-claiming (which needs a live
// homeserver's /keys endpoints): Encrypt uses an OutboundGroupSession the
// test already shares out-of-band with the "other" machine's CryptoStore,
// exactly the "two goolm olm machines in memory" shape the feature doc
// asks for the encrypted paths to be tested with.
type machineCryptoHelper struct {
	mach     *crypto.OlmMachine
	outbound *crypto.OutboundGroupSession // set only on the sending side
}

func (m *machineCryptoHelper) Encrypt(_ context.Context, roomID id.RoomID, evtType event.Type, content any) (*event.EncryptedEventContent, error) {
	plaintext, err := json.Marshal(map[string]any{
		"room_id": roomID,
		"type":    evtType.Type,
		"content": content,
	})
	if err != nil {
		return nil, err
	}
	ciphertext, err := m.outbound.Encrypt(plaintext)
	if err != nil {
		return nil, err
	}
	own := m.mach.OwnIdentity()
	return &event.EncryptedEventContent{
		Algorithm:        id.AlgorithmMegolmV1,
		SenderKey:        own.IdentityKey,
		SessionID:        m.outbound.ID(),
		MegolmCiphertext: ciphertext,
	}, nil
}

func (m *machineCryptoHelper) Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error) {
	return m.mach.DecryptMegolmEvent(ctx, evt)
}

func (m *machineCryptoHelper) WaitForSession(context.Context, id.RoomID, id.SenderKey, id.SessionID, time.Duration) bool {
	return false
}

func (m *machineCryptoHelper) RequestSession(context.Context, id.RoomID, id.SenderKey, id.SessionID, id.UserID, id.DeviceID) {
}

func (m *machineCryptoHelper) Init(context.Context) error { return nil }

var _ mautrix.CryptoHelper = (*machineCryptoHelper)(nil)

// olmMachineStateStore is the smallest crypto.StateStore that lets an
// OlmMachine load and decrypt/encrypt in memory; it is not the same
// interface as mautrix.StateStore (used by the Adapter's client), so it
// only exists for the raw *crypto.OlmMachine test fixtures below.
type olmMachineStateStore struct{}

func (olmMachineStateStore) IsEncrypted(context.Context, id.RoomID) (bool, error) { return true, nil }
func (olmMachineStateStore) GetEncryptionEvent(context.Context, id.RoomID) (*event.EncryptionEventContent, error) {
	return &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}, nil
}
func (olmMachineStateStore) GetHistoryVisibility(context.Context, id.RoomID) (*event.HistoryVisibilityEventContent, error) {
	return &event.HistoryVisibilityEventContent{HistoryVisibility: event.HistoryVisibilityShared}, nil
}
func (olmMachineStateStore) FindSharedRooms(context.Context, id.UserID) ([]id.RoomID, error) {
	return nil, nil
}

func newTestOlmMachine(t *testing.T, userID id.UserID) *crypto.OlmMachine {
	t.Helper()
	cli, err := mautrix.NewClient("http://localhost", userID, "token")
	if err != nil {
		t.Fatalf("mautrix.NewClient: %v", err)
	}
	cli.DeviceID = "DEVICE1"
	mach := crypto.NewOlmMachine(cli, nil, crypto.NewMemoryStore(nil), olmMachineStateStore{})
	if err := mach.Load(context.Background()); err != nil {
		t.Fatalf("mach.Load: %v", err)
	}
	return mach
}

// TestMegolmEncryptDecryptRoundTrip is the pure-crypto proof: two
// independent, in-memory olm machines (built with -tags goolm, so this
// exercises the pure-Go megolm implementation, never libolm/CGO) share an
// outbound/inbound megolm session pair directly and confirm the ciphertext
// one produces is exactly what the other decrypts back to plaintext.
func TestMegolmEncryptDecryptRoundTrip(t *testing.T) {
	const room = id.RoomID("!crypto:matrix.example.org")
	sender := newTestOlmMachine(t, "@sender:matrix.example.org")
	receiver := newTestOlmMachine(t, "@receiver:matrix.example.org")

	ogs, err := crypto.NewOutboundGroupSession(room, nil, nil)
	if err != nil {
		t.Fatalf("NewOutboundGroupSession: %v", err)
	}
	// The real key-sharing dance (to-device olm messages via /keys/claim
	// and /sendToDevice) needs a live homeserver's device endpoints; here
	// the test stands in for "sharing completed" directly, as documented
	// in this file's deviations.
	ogs.Shared = true
	shareContent, ok := ogs.ShareContent().Parsed.(*event.RoomKeyEventContent)
	if !ok {
		t.Fatalf("ShareContent().Parsed is %T, want *event.RoomKeyEventContent", ogs.ShareContent().Parsed)
	}

	senderIdentity := sender.OwnIdentity()
	igs, err := crypto.NewInboundGroupSession(senderIdentity.IdentityKey, senderIdentity.SigningKey, room, shareContent.SessionKey, 0, 0, nil, false)
	if err != nil {
		t.Fatalf("NewInboundGroupSession: %v", err)
	}
	if err := receiver.CryptoStore.PutGroupSession(context.Background(), igs); err != nil {
		t.Fatalf("PutGroupSession: %v", err)
	}

	plaintext, err := json.Marshal(map[string]any{
		"room_id": room,
		"type":    "m.room.message",
		"content": map[string]any{"msgtype": "m.text", "body": "hola cifrado"},
	})
	if err != nil {
		t.Fatalf("marshal plaintext: %v", err)
	}
	ciphertext, err := ogs.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("ogs.Encrypt: %v", err)
	}

	encEvt := &event.Event{
		ID:     "$evt1",
		Sender: "@sender:matrix.example.org",
		RoomID: room,
		Type:   event.EventEncrypted,
		Content: event.Content{Parsed: &event.EncryptedEventContent{
			Algorithm:        id.AlgorithmMegolmV1,
			SenderKey:        senderIdentity.IdentityKey,
			SessionID:        ogs.ID(),
			MegolmCiphertext: ciphertext,
		}},
	}

	decrypted, err := receiver.DecryptMegolmEvent(context.Background(), encEvt)
	if err != nil {
		t.Fatalf("DecryptMegolmEvent: %v", err)
	}
	if decrypted.Type != event.EventMessage {
		t.Errorf("decrypted.Type = %v, want m.room.message", decrypted.Type)
	}
	if body := decrypted.Content.AsMessage().Body; body != "hola cifrado" {
		t.Errorf("decrypted body = %q, want %q", body, "hola cifrado")
	}
}

// TestAdapterDecryptsRealMegolmEvent exercises the adapter's actual
// receive path (Run -> encryptedHandler -> a.crypto.Decrypt) against a
// real megolm-encrypted event produced the same way the pure-crypto test
// above does, proving the wiring (not just the raw crypto library) works.
func TestAdapterDecryptsRealMegolmEvent(t *testing.T) {
	const room = id.RoomID("!crypto:matrix.example.org")
	const sender = id.UserID("@alice:matrix.example.org")

	senderMach := newTestOlmMachine(t, sender)
	receiverMach := newTestOlmMachine(t, "@alice:example.com")

	ogs, err := crypto.NewOutboundGroupSession(room, nil, nil)
	if err != nil {
		t.Fatalf("NewOutboundGroupSession: %v", err)
	}
	// The real key-sharing dance (to-device olm messages via /keys/claim
	// and /sendToDevice) needs a live homeserver's device endpoints; here
	// the test stands in for "sharing completed" directly, as documented
	// in this file's deviations.
	ogs.Shared = true
	shareContent := ogs.ShareContent().Parsed.(*event.RoomKeyEventContent)
	senderIdentity := senderMach.OwnIdentity()
	igs, err := crypto.NewInboundGroupSession(senderIdentity.IdentityKey, senderIdentity.SigningKey, room, shareContent.SessionKey, 0, 0, nil, false)
	if err != nil {
		t.Fatalf("NewInboundGroupSession: %v", err)
	}
	if err := receiverMach.CryptoStore.PutGroupSession(context.Background(), igs); err != nil {
		t.Fatalf("PutGroupSession: %v", err)
	}

	plaintext, _ := json.Marshal(map[string]any{
		"room_id": room,
		"type":    "m.room.message",
		"content": map[string]any{"msgtype": "m.text", "body": "mensaje cifrado real"},
	})
	ciphertext, err := ogs.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("ogs.Encrypt: %v", err)
	}

	encEvt := &event.Event{
		ID:     "$enc-real",
		Sender: sender,
		Type:   event.EventEncrypted,
		Content: event.Content{Parsed: &event.EncryptedEventContent{
			Algorithm:        id.AlgorithmMegolmV1,
			SenderKey:        senderIdentity.IdentityKey,
			SessionID:        ogs.ID(),
			MegolmCiphertext: ciphertext,
		}},
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
	adapter := newTestAdapter(t, srv, &machineCryptoHelper{mach: receiverMach})
	sink := newMemSink()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	var item core.Item
	select {
	case item = <-sink.upserts:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the decrypted item")
	}
	cancel()
	<-done

	if item.Meta["undecryptable"] == "true" {
		t.Fatal("item marked undecryptable, want a successful decrypt")
	}
	if item.Body != "mensaje cifrado real" {
		t.Errorf("item.Body = %q, want %q", item.Body, "mensaje cifrado real")
	}
}

// TestAdapterSendEncryptsWithMegolm exercises the adapter's send path
// (Send -> client.SendMessageEvent -> cli.Crypto.Encrypt) for an
// encrypted room, then proves the ciphertext it put on the wire really
// decrypts back to the outgoing text using a second, independent olm
// machine holding the paired inbound session.
func TestAdapterSendEncryptsWithMegolm(t *testing.T) {
	const room = id.RoomID("!crypto-send:matrix.example.org")

	senderMach := newTestOlmMachine(t, "@alice:example.com")
	receiverMach := newTestOlmMachine(t, "@alice:matrix.example.org")

	ogs, err := crypto.NewOutboundGroupSession(room, nil, nil)
	if err != nil {
		t.Fatalf("NewOutboundGroupSession: %v", err)
	}
	// The real key-sharing dance (to-device olm messages via /keys/claim
	// and /sendToDevice) needs a live homeserver's device endpoints; here
	// the test stands in for "sharing completed" directly, as documented
	// in this file's deviations.
	ogs.Shared = true
	shareContent := ogs.ShareContent().Parsed.(*event.RoomKeyEventContent)
	senderIdentity := senderMach.OwnIdentity()
	igs, err := crypto.NewInboundGroupSession(senderIdentity.IdentityKey, senderIdentity.SigningKey, room, shareContent.SessionKey, 0, 0, nil, false)
	if err != nil {
		t.Fatalf("NewInboundGroupSession: %v", err)
	}
	if err := receiverMach.CryptoStore.PutGroupSession(context.Background(), igs); err != nil {
		t.Fatalf("PutGroupSession: %v", err)
	}

	srv, state := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, &machineCryptoHelper{mach: senderMach, outbound: ogs})
	if err := adapter.client.StateStore.SetEncryptionEvent(context.Background(), room, &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}); err != nil {
		t.Fatalf("SetEncryptionEvent: %v", err)
	}

	_, err = adapter.Send(context.Background(), core.Outgoing{
		Channel: core.ChannelMatrix,
		Account: "work",
		To:      []string{string(room)},
		Body:    "texto secreto",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	state.mu.Lock()
	if len(state.sentEvents) != 1 {
		state.mu.Unlock()
		t.Fatalf("sentEvents = %d, want 1", len(state.sentEvents))
	}
	sent := state.sentEvents[0]
	state.mu.Unlock()

	if wantPath := "/send/m.room.encrypted/"; !strings.Contains(sent.path, wantPath) {
		t.Fatalf("sent path = %q, want it to contain %q (the room is encrypted)", sent.path, wantPath)
	}

	var encContent event.EncryptedEventContent
	if err := json.Unmarshal(sent.body, &encContent); err != nil {
		t.Fatalf("decode sent encrypted content: %v", err)
	}

	decrypted, err := receiverMach.DecryptMegolmEvent(context.Background(), &event.Event{
		ID:      "$sent1",
		Sender:  "@alice:example.com",
		RoomID:  room,
		Type:    event.EventEncrypted,
		Content: event.Content{Parsed: &encContent},
	})
	if err != nil {
		t.Fatalf("DecryptMegolmEvent(sent ciphertext): %v", err)
	}
	if body := decrypted.Content.AsMessage().Body; body != "texto secreto" {
		t.Errorf("decrypted sent body = %q, want %q", body, "texto secreto")
	}
}
