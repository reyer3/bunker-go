package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/reyer3/bunker-go/internal/core"
)

// testMediaKey and friends are fake decryption material, never real
// WhatsApp secrets, used only to prove they round-trip and are never
// exposed outside the adapter.
var (
	testMediaKey      = []byte("test-media-key-bytes")
	testFileSHA256    = []byte("test-file-sha256")
	testFileEncSHA256 = []byte("test-file-enc-sha256")
)

func imageMessageEvent(chat types.JID, msgID string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            types.MessageID(msgID),
		},
		Message: &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				Mimetype:      strPtr("image/jpeg"),
				FileLength:    uint64Ptr(2048),
				DirectPath:    strPtr("/v/t62.7118-24/manual"),
				MediaKey:      testMediaKey,
				FileSHA256:    testFileSHA256,
				FileEncSHA256: testFileEncSHA256,
			},
		},
	}
}

func TestDownloadAttachmentUsesDescriptorPersistedOnReceive(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	cli.downloadData = []byte("decrypted image bytes")
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "1234@s.whatsapp.net")
	cli.emit(imageMessageEvent(chat, "IMG1"))
	waitFor(t, func() bool { return len(sink.items()) == 1 })
	item := sink.items()[0]

	rc, err := a.DownloadAttachment(context.Background(), item, 0)
	if err != nil {
		t.Fatalf("DownloadAttachment() error = %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "decrypted image bytes" {
		t.Errorf("data = %q, want %q", data, "decrypted image bytes")
	}

	desc := cli.lastDownload
	if desc == nil {
		t.Fatal("cli.Download was never called")
	}
	if desc.GetDirectPath() != "/v/t62.7118-24/manual" {
		t.Errorf("DirectPath = %q, want the persisted DirectPath", desc.GetDirectPath())
	}
	if !bytes.Equal(desc.GetMediaKey(), testMediaKey) {
		t.Errorf("MediaKey = %x, want %x", desc.GetMediaKey(), testMediaKey)
	}
	if !bytes.Equal(desc.GetFileSHA256(), testFileSHA256) {
		t.Errorf("FileSHA256 mismatch")
	}
	if !bytes.Equal(desc.GetFileEncSHA256(), testFileEncSHA256) {
		t.Errorf("FileEncSHA256 mismatch")
	}
	if mt, ok := desc.(whatsmeow.MediaTypeable); !ok || mt.GetMediaType() != whatsmeow.MediaImage {
		t.Errorf("MediaType = %v, want MediaImage", desc)
	}
}

func TestDownloadAttachmentUsesDescriptorPersistedOnHistorySync(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	cli.downloadData = []byte("decrypted history image bytes")
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	conv := &waHistorySync.Conversation{
		ID:          strPtr("1234@s.whatsapp.net"),
		UnreadCount: uint32Ptr(1),
		Messages: []*waHistorySync.HistorySyncMsg{{
			Message: &waWeb.WebMessageInfo{
				Key: &waCommon.MessageKey{ID: strPtr("HIMG1")},
				Message: &waE2E.Message{
					ImageMessage: &waE2E.ImageMessage{
						Mimetype:      strPtr("image/png"),
						FileLength:    uint64Ptr(4096),
						DirectPath:    strPtr("/v/t62.7118-24/history"),
						MediaKey:      testMediaKey,
						FileSHA256:    testFileSHA256,
						FileEncSHA256: testFileEncSHA256,
					},
				},
				MessageTimestamp: uint64Ptrx(1),
			},
		}},
	}
	emitHistorySync(cli, conv)
	waitFor(t, func() bool { return len(sink.items()) == 1 })
	item := sink.items()[0]

	rc, err := a.DownloadAttachment(context.Background(), item, 0)
	if err != nil {
		t.Fatalf("DownloadAttachment() error = %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "decrypted history image bytes" {
		t.Errorf("data = %q, want %q", data, "decrypted history image bytes")
	}
}

func TestDownloadAttachmentReturnsErrNoMediaKeyForPreD2Items(t *testing.T) {
	sink := newSpySink()
	a := newTestAdapter("personal", newFakeWAClient())
	a.sink = sink // adapter is "running" without ever having persisted a descriptor

	item := core.Item{
		ID:          "whatsapp:personal:1234@s.whatsapp.net/OLD1",
		Channel:     core.ChannelWhatsApp,
		Account:     "personal",
		Attachments: []core.Attachment{{Name: "document", MIME: "application/pdf", Size: 1024, Ref: "/v/old/path"}},
	}

	_, err := a.DownloadAttachment(context.Background(), item, 0)
	if !errors.Is(err, ErrNoMediaKey) {
		t.Fatalf("DownloadAttachment() error = %v, want ErrNoMediaKey", err)
	}
}

func TestDownloadAttachmentRejectsOutOfRangeIndex(t *testing.T) {
	sink := newSpySink()
	a := newTestAdapter("personal", newFakeWAClient())
	a.sink = sink

	item := core.Item{
		ID:          "whatsapp:personal:1234@s.whatsapp.net/M1",
		Channel:     core.ChannelWhatsApp,
		Account:     "personal",
		Attachments: []core.Attachment{{Name: "image", MIME: "image/jpeg", Size: 1}},
	}

	_, err := a.DownloadAttachment(context.Background(), item, 3)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("DownloadAttachment() error = %v, want ErrNotFound", err)
	}
}

// TestMediaKeyNeverAppearsInItemOrErrors is the constraint's own test:
// the download descriptor's key material must never surface through the
// Item a caller marshals to JSON (list/read output), nor in an error
// string, even when a download fails.
func TestMediaKeyNeverAppearsInItemOrErrors(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	cli.downloadErr = errors.New("network unreachable")
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "1234@s.whatsapp.net")
	cli.emit(imageMessageEvent(chat, "IMG2"))
	waitFor(t, func() bool { return len(sink.items()) == 1 })
	item := sink.items()[0]

	itemJSON, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Marshal(item): %v", err)
	}
	if bytes.Contains(itemJSON, testMediaKey) || bytes.Contains(itemJSON, []byte("MediaKey")) {
		t.Errorf("item JSON leaked key material: %s", itemJSON)
	}

	_, downloadErr := a.DownloadAttachment(context.Background(), item, 0)
	if downloadErr == nil {
		t.Fatal("expected a download error")
	}
	if bytes.Contains([]byte(downloadErr.Error()), testMediaKey) {
		t.Errorf("download error leaked key material: %v", downloadErr)
	}
}
