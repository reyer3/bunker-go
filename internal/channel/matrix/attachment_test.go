package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// M1: Matrix attachment download. toItem records an Attachment for
// incoming m.image/m.video/m.audio/m.file events (plain `url` or
// encrypted `file`), the adapter privately persists enough to download
// it later (mirroring WhatsApp's D2 descriptor, never a core.Item
// field), and DownloadAttachment implements core.AttachmentDownloader
// against it.

func TestAdapterIsAttachmentDownloader(t *testing.T) {
	var _ core.AttachmentDownloader = (*Adapter)(nil)
}

func TestToItemRecordsPlainImageAttachment(t *testing.T) {
	srv, _ := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)

	evt := &event.Event{
		ID:     "$img1",
		Sender: "@alice:matrix.example.org",
		RoomID: id.RoomID("!room:matrix.example.org"),
		Type:   event.EventMessage,
		Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType:  event.MsgImage,
			Body:     "photo.jpg",
			FileName: "photo.jpg",
			URL:      "mxc://matrix.example.org/plain123",
			Info:     &event.FileInfo{MimeType: "image/jpeg", Size: 2048},
		}},
	}

	item := adapter.toItem(evt)
	if len(item.Attachments) != 1 {
		t.Fatalf("len(item.Attachments) = %d, want 1", len(item.Attachments))
	}
	att := item.Attachments[0]
	if att.Name != "photo.jpg" {
		t.Errorf("Name = %q, want photo.jpg", att.Name)
	}
	if att.MIME != "image/jpeg" {
		t.Errorf("MIME = %q, want image/jpeg", att.MIME)
	}
	if att.Size != 2048 {
		t.Errorf("Size = %d, want 2048", att.Size)
	}
	if att.Ref != "mxc://matrix.example.org/plain123" {
		t.Errorf("Ref = %q, want the mxc URL", att.Ref)
	}
}

func TestToItemIgnoresPlainTextEvent(t *testing.T) {
	srv, _ := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)

	evt := &event.Event{
		ID:     "$txt1",
		Sender: "@alice:matrix.example.org",
		RoomID: id.RoomID("!room:matrix.example.org"),
		Type:   event.EventMessage,
		Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    "hola",
		}},
	}

	item := adapter.toItem(evt)
	if len(item.Attachments) != 0 {
		t.Fatalf("len(item.Attachments) = %d, want 0 for a text event", len(item.Attachments))
	}
}

func TestDownloadAttachmentPlainImageMatchesUploadedBytes(t *testing.T) {
	testDownloadPlainImage(t, false)
}

// A homeserver without authenticated media (no v1.11) answers the v1
// download with M_UNRECOGNIZED; DownloadAttachment falls back to the
// legacy /_matrix/media/v3/download path.
func TestDownloadAttachmentPlainImageLegacyMediaFallback(t *testing.T) {
	testDownloadPlainImage(t, true)
}

func testDownloadPlainImage(t *testing.T, legacyOnly bool) {
	t.Helper()
	const room = id.RoomID("!plain:matrix.example.org")
	const sender = id.UserID("@alice:matrix.example.org")
	mxc := id.ContentURI{Homeserver: "matrix.example.org", FileID: "plain-file-1"}
	plaintext := []byte("plain matrix image bytes")

	imgEvt := &event.Event{
		ID:     "$img-plain",
		Sender: sender,
		Type:   event.EventMessage,
		Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType:  event.MsgImage,
			Body:     "photo.jpg",
			FileName: "photo.jpg",
			URL:      mxc.CUString(),
			Info:     &event.FileInfo{MimeType: "image/jpeg", Size: len(plaintext)},
		}},
	}
	firstSync := &mautrix.RespSync{
		NextBatch: "s1",
		Rooms: mautrix.RespSyncRooms{
			Join: map[id.RoomID]*mautrix.SyncJoinedRoom{
				room: {Timeline: mautrix.SyncTimeline{SyncEventsList: mautrix.SyncEventsList{Events: []*event.Event{imgEvt}}}},
			},
		},
	}

	srv, state := newFakeHomeserver(t, []*mautrix.RespSync{firstSync})
	state.setMediaFile(mxc, plaintext)
	state.legacyMediaOnly = legacyOnly
	adapter := newTestAdapter(t, srv, nil)
	sink := newMemSink()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	var item core.Item
	select {
	case item = <-sink.upserts:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the image item")
	}
	cancel()
	<-done

	rc, err := adapter.DownloadAttachment(context.Background(), item, 0)
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != string(plaintext) {
		t.Errorf("data = %q, want %q", data, plaintext)
	}
}

func TestDownloadAttachmentEncryptedImageDecryptsByteIdentical(t *testing.T) {
	testDownloadEncryptedImage(t, false)
}

// The legacy-media fallback hands its ciphertext to the same decryption.
func TestDownloadAttachmentEncryptedImageLegacyMediaFallback(t *testing.T) {
	testDownloadEncryptedImage(t, true)
}

func testDownloadEncryptedImage(t *testing.T, legacyOnly bool) {
	t.Helper()
	const room = id.RoomID("!enc-dl:matrix.example.org")
	const sender = id.UserID("@alice:matrix.example.org")
	mxc := id.ContentURI{Homeserver: "matrix.example.org", FileID: "enc-file-1"}

	senderMach := newTestOlmMachine(t, sender)
	receiverMach := newTestOlmMachine(t, "@alice:example.com")

	ogs, err := crypto.NewOutboundGroupSession(room, nil, nil)
	if err != nil {
		t.Fatalf("NewOutboundGroupSession: %v", err)
	}
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

	original := []byte{0x89, 0x50, 0x4e, 0x47, 0x01, 0x02, 0x03, 0x04, 0x05, 0xff, 0xfe}
	ciphertext := append([]byte(nil), original...)
	ef := attachment.NewEncryptedFile()
	ef.EncryptInPlace(ciphertext)

	imgContent := &event.MessageEventContent{
		MsgType:  event.MsgImage,
		Body:     "pic.png",
		FileName: "pic.png",
		Info:     &event.FileInfo{MimeType: "image/png", Size: len(original)},
		File:     &event.EncryptedFileInfo{EncryptedFile: *ef, URL: mxc.CUString()},
	}
	plaintextEnvelope, err := json.Marshal(map[string]any{
		"room_id": room,
		"type":    "m.room.message",
		"content": imgContent,
	})
	if err != nil {
		t.Fatalf("marshal plaintext envelope: %v", err)
	}
	megolmCiphertext, err := ogs.Encrypt(plaintextEnvelope)
	if err != nil {
		t.Fatalf("ogs.Encrypt: %v", err)
	}

	encEvt := &event.Event{
		ID:     "$enc-img",
		Sender: sender,
		Type:   event.EventEncrypted,
		Content: event.Content{Parsed: &event.EncryptedEventContent{
			Algorithm:        id.AlgorithmMegolmV1,
			SenderKey:        senderIdentity.IdentityKey,
			SessionID:        ogs.ID(),
			MegolmCiphertext: megolmCiphertext,
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

	srv, state := newFakeHomeserver(t, []*mautrix.RespSync{firstSync})
	state.setMediaFile(mxc, ciphertext)
	state.legacyMediaOnly = legacyOnly
	adapter := newTestAdapter(t, srv, &machineCryptoHelper{mach: receiverMach})
	sink := newMemSink()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	var item core.Item
	select {
	case item = <-sink.upserts:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the decrypted image item")
	}
	cancel()
	<-done

	if item.Meta["undecryptable"] == "true" {
		t.Fatal("item marked undecryptable, want a successful decrypt")
	}
	if len(item.Attachments) != 1 {
		t.Fatalf("len(item.Attachments) = %d, want 1", len(item.Attachments))
	}

	rc, err := adapter.DownloadAttachment(context.Background(), item, 0)
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != string(original) {
		t.Errorf("decrypted data = %x, want %x (byte-identical to the original)", data, original)
	}
}

func TestDownloadAttachmentBadHashErrors(t *testing.T) {
	const room = id.RoomID("!enc-bad:matrix.example.org")
	const sender = id.UserID("@alice:matrix.example.org")
	mxc := id.ContentURI{Homeserver: "matrix.example.org", FileID: "enc-file-bad"}

	senderMach := newTestOlmMachine(t, sender)
	receiverMach := newTestOlmMachine(t, "@alice:example.com")

	ogs, err := crypto.NewOutboundGroupSession(room, nil, nil)
	if err != nil {
		t.Fatalf("NewOutboundGroupSession: %v", err)
	}
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

	original := []byte("some bytes that will be tampered with after encryption")
	ciphertext := append([]byte(nil), original...)
	ef := attachment.NewEncryptedFile()
	ef.EncryptInPlace(ciphertext)
	// Tamper with the ciphertext the media repo serves, without touching
	// the descriptor's hash: DecryptInPlace must reject this instead of
	// silently returning corrupted bytes.
	tampered := append([]byte(nil), ciphertext...)
	tampered[0] ^= 0xFF

	imgContent := &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    "pic.png",
		Info:    &event.FileInfo{MimeType: "image/png", Size: len(original)},
		File:    &event.EncryptedFileInfo{EncryptedFile: *ef, URL: mxc.CUString()},
	}
	plaintextEnvelope, err := json.Marshal(map[string]any{
		"room_id": room,
		"type":    "m.room.message",
		"content": imgContent,
	})
	if err != nil {
		t.Fatalf("marshal plaintext envelope: %v", err)
	}
	megolmCiphertext, err := ogs.Encrypt(plaintextEnvelope)
	if err != nil {
		t.Fatalf("ogs.Encrypt: %v", err)
	}

	encEvt := &event.Event{
		ID:     "$enc-img-bad",
		Sender: sender,
		Type:   event.EventEncrypted,
		Content: event.Content{Parsed: &event.EncryptedEventContent{
			Algorithm:        id.AlgorithmMegolmV1,
			SenderKey:        senderIdentity.IdentityKey,
			SessionID:        ogs.ID(),
			MegolmCiphertext: megolmCiphertext,
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

	srv, state := newFakeHomeserver(t, []*mautrix.RespSync{firstSync})
	state.setMediaFile(mxc, tampered)
	adapter := newTestAdapter(t, srv, &machineCryptoHelper{mach: receiverMach})
	sink := newMemSink()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	var item core.Item
	select {
	case item = <-sink.upserts:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the decrypted image item")
	}
	cancel()
	<-done

	_, err = adapter.DownloadAttachment(context.Background(), item, 0)
	if !errors.Is(err, attachment.ErrHashMismatch) {
		t.Fatalf("DownloadAttachment() error = %v, want ErrHashMismatch", err)
	}
}

func TestDownloadAttachmentRejectsOutOfRangeIndex(t *testing.T) {
	srv, _ := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)
	adapter.sink = newMemSink()

	item := core.Item{
		ID:          "matrix:work:!room:matrix.example.org/$evt1",
		Channel:     core.ChannelMatrix,
		Account:     "work",
		Attachments: []core.Attachment{{Name: "photo.jpg", MIME: "image/jpeg", Size: 1}},
	}

	_, err := adapter.DownloadAttachment(context.Background(), item, 3)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("DownloadAttachment() error = %v, want ErrNotFound", err)
	}
}

func TestDownloadAttachmentNoDescriptorErrors(t *testing.T) {
	srv, _ := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil)
	adapter.sink = newMemSink() // running, but nothing was ever persisted

	item := core.Item{
		ID:          "matrix:work:!room:matrix.example.org/$evt-old",
		Channel:     core.ChannelMatrix,
		Account:     "work",
		Attachments: []core.Attachment{{Name: "photo.jpg", MIME: "image/jpeg", Size: 1}},
	}

	_, err := adapter.DownloadAttachment(context.Background(), item, 0)
	if !errors.Is(err, ErrNoMediaDescriptor) {
		t.Fatalf("DownloadAttachment() error = %v, want ErrNoMediaDescriptor", err)
	}
}

func TestDownloadAttachmentAdapterNotRunningErrors(t *testing.T) {
	srv, _ := newFakeHomeserver(t, nil)
	adapter := newTestAdapter(t, srv, nil) // Run never called: sink is nil

	item := core.Item{
		ID:          "matrix:work:!room:matrix.example.org/$evt1",
		Channel:     core.ChannelMatrix,
		Account:     "work",
		Attachments: []core.Attachment{{Name: "photo.jpg", MIME: "image/jpeg", Size: 1}},
	}

	_, err := adapter.DownloadAttachment(context.Background(), item, 0)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("DownloadAttachment() error = %v, want ErrUnsupported", err)
	}
}
