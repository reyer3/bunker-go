package whatsapp

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/oggfixture"
)

// ownUploadResp is a fake upload result: the decryption material
// whatsmeow hands back for media WE upload, never a real secret.
var ownUploadResp = whatsmeow.UploadResponse{
	URL: "https://example/own", DirectPath: "/v/own/path", MediaKey: []byte("own-media-key"),
	FileEncSHA256: []byte("own-enc-sha"), FileSHA256: []byte("own-sha"), FileLength: 400,
}

// downloadOwn downloads item's attachment at index and asserts it went
// through the descriptor persisted when we uploaded it.
func downloadOwn(t *testing.T, a *Adapter, cli *fakeWAClient, item core.Item, index int, wantType whatsmeow.MediaType) {
	t.Helper()
	rc, err := a.DownloadAttachment(context.Background(), item, index)
	if err != nil {
		t.Fatalf("DownloadAttachment(%d) error = %v", index, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "own bytes" {
		t.Errorf("data = %q, want %q", data, "own bytes")
	}
	desc := cli.lastDownload
	if desc.GetDirectPath() != ownUploadResp.DirectPath || !bytes.Equal(desc.GetMediaKey(), ownUploadResp.MediaKey) ||
		!bytes.Equal(desc.GetFileSHA256(), ownUploadResp.FileSHA256) || !bytes.Equal(desc.GetFileEncSHA256(), ownUploadResp.FileEncSHA256) {
		t.Errorf("descriptor = %+v, want the upload's own key material", desc)
	}
	if mt, ok := desc.(whatsmeow.MediaTypeable); !ok || mt.GetMediaType() != wantType {
		t.Errorf("MediaType = %v, want %v", desc, wantType)
	}
}

func newOwnMediaAdapter(t *testing.T, msgID string) (*Adapter, *fakeWAClient, *spySink) {
	t.Helper()
	cli := newFakeWAClient()
	cli.uploadResp = ownUploadResp
	cli.downloadData = []byte("own bytes")
	cli.sendResp = whatsmeow.SendResponse{ID: types.MessageID(msgID), Timestamp: time.Unix(9000, 0)}
	sink := newSpySink()
	a := newTestAdapter("personal", cli, time.Millisecond)
	a.sink = sink
	return a, cli, sink
}

func TestDownloadAttachmentWorksForOwnVoiceNote(t *testing.T) {
	a, cli, _ := newOwnMediaAdapter(t, "OWN-PTT")
	path := filepath.Join(t.TempDir(), "nota.ogg")
	if err := oggfixture.Write(path, 3*time.Second); err != nil {
		t.Fatal(err)
	}

	receipt, err := a.SendVoice(context.Background(), core.Outgoing{
		To: []string{"1234@s.whatsapp.net"}, Attachments: []string{path}, Voice: true,
	})
	if err != nil {
		t.Fatalf("SendVoice() error = %v", err)
	}

	item := core.Item{ID: receipt.ID, Channel: core.ChannelWhatsApp, Account: "personal", FromMe: true,
		Attachments: []core.Attachment{{Name: "nota.ogg", MIME: "audio/ogg", Voice: true}}}
	downloadOwn(t, a, cli, item, 0, whatsmeow.MediaAudio)
}

// TestDownloadAttachmentWorksForEveryOwnAttachment covers a multi-file
// send: core stores ONE item under the receipt id carrying every
// attachment, so each index must resolve to its own upload's descriptor.
func TestDownloadAttachmentWorksForEveryOwnAttachment(t *testing.T) {
	a, cli, _ := newOwnMediaAdapter(t, "OWN-DOC")
	dir := t.TempDir()
	doc := writeFile(t, dir, "notas.txt", 32)
	img := writeFile(t, dir, "foto.jpg", 32)

	receipt, err := a.SendMedia(context.Background(), core.Outgoing{
		To: []string{"1234@s.whatsapp.net"}, Body: "adjunto", Attachments: []string{doc, img},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}

	item := core.Item{ID: receipt.ID, Channel: core.ChannelWhatsApp, Account: "personal", FromMe: true,
		Attachments: []core.Attachment{{Name: "notas.txt", MIME: "text/plain"}, {Name: "foto.jpg", MIME: "image/jpeg"}}}
	downloadOwn(t, a, cli, item, 0, whatsmeow.MediaDocument)
	downloadOwn(t, a, cli, item, 1, whatsmeow.MediaImage)
}

// TestOwnMediaEchoKeepsDescriptor: when our own message comes back
// through ingest (another device's echo, history sync) carrying media
// without key material, the re-store must not overwrite the descriptor
// kept at send time.
func TestOwnMediaEchoKeepsDescriptor(t *testing.T) {
	a, cli, sink := newOwnMediaAdapter(t, "OWN-ECHO")
	a.sink = nil
	cli.linked = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	doc := writeFile(t, t.TempDir(), "notas.txt", 32)
	receipt, err := a.SendMedia(context.Background(), core.Outgoing{
		To: []string{"1234@s.whatsapp.net"}, Attachments: []string{doc},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}

	chat := mustJID(t, "1234@s.whatsapp.net")
	cli.emit(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat, IsFromMe: true},
			ID:            "OWN-ECHO",
		},
		Message: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			Mimetype: strPtr("text/plain"), FileName: strPtr("notas.txt"), FileLength: uint64Ptr(32),
		}},
	})
	waitFor(t, func() bool { return len(sink.items()) == 1 })
	item := sink.items()[0]
	if item.ID != receipt.ID {
		t.Fatalf("echo item id = %q, want %q", item.ID, receipt.ID)
	}
	downloadOwn(t, a, cli, item, 0, whatsmeow.MediaDocument)
}
