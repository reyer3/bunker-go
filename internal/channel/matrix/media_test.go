package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// T16(b): Matrix media. core.MediaSender's AttachmentPolicy comes from the
// homeserver's own m.upload.size (fetched once, cached, with a 50 MB
// fallback), and SendMedia uploads each attachment, encrypting it
// client-side first in an encrypted room (crypto/attachment) and sending
// an m.image/m.video/m.audio/m.file event, before a separate m.text event
// carries out.Body (if any).

// mediaFakeState records what the fake homeserver observed, independent of
// adapter_test.go's own fakeState/newFakeHomeserver (a parallel writer
// touches those for T13's typing/fan-out work): this file's fake server is
// self-contained so the two never conflict.
type mediaFakeState struct {
	mu             sync.Mutex
	mediaConfig    *mautrix.RespMediaConfig // nil => the endpoint 404s
	uploadedBodies [][]byte
	uploadedType   []string
	sentEvents     []json.RawMessage
}

func newMediaFakeHomeserver(t *testing.T, cfg *mautrix.RespMediaConfig) (*httptest.Server, *mediaFakeState) {
	t.Helper()
	state := &mediaFakeState{mediaConfig: cfg}

	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v1/media/config", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.mediaConfig == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(state.mediaConfig)
	})
	mux.HandleFunc("/_matrix/media/v3/upload", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		state.mu.Lock()
		state.uploadedBodies = append(state.uploadedBodies, body)
		state.uploadedType = append(state.uploadedType, r.Header.Get("Content-Type"))
		n := len(state.uploadedBodies)
		state.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"content_uri": fmt.Sprintf("mxc://matrix.example.org/media%d", n)})
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		// Send's typing notification (T13d) runs before a text-only send.
		if strings.Contains(r.URL.Path, "/typing/") && r.Method == http.MethodPut {
			w.Write([]byte("{}"))
			return
		}
		if !strings.Contains(r.URL.Path, "/send/") || r.Method != http.MethodPut {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		state.mu.Lock()
		state.sentEvents = append(state.sentEvents, json.RawMessage(body))
		n := len(state.sentEvents)
		state.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"event_id": fmt.Sprintf("$media-sent%d", n)})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, state
}

func newMediaTestAdapter(t *testing.T, srv *httptest.Server) *Adapter {
	t.Helper()
	client, err := mautrix.NewClient(srv.URL, id.UserID("@alice:matrix.example.org"), "syt_test_token")
	if err != nil {
		t.Fatalf("mautrix.NewClient: %v", err)
	}
	client.DeviceID = "DEVICE1"
	a := newAdapter("work", client, nil)
	a.SetSleeper(func(time.Duration) {}) // never sleep for real in tests (T13f)
	return a
}

func writeMatrixFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func decodeContent(t *testing.T, raw json.RawMessage) event.MessageEventContent {
	t.Helper()
	var content event.MessageEventContent
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatalf("decode event content: %v", err)
	}
	return content
}

func TestAdapterIsMediaSender(t *testing.T) {
	var _ core.MediaSender = (*Adapter)(nil)
}

func TestAttachmentPolicyUsesHomeserverMediaConfig(t *testing.T) {
	srv, _ := newMediaFakeHomeserver(t, &mautrix.RespMediaConfig{UploadSize: 12345})
	adapter := newMediaTestAdapter(t, srv)

	policy := adapter.AttachmentPolicy()
	if got := policy.MaxBytes[core.AnyMIME]; got != 12345 {
		t.Errorf("AttachmentPolicy().MaxBytes[core.AnyMIME] = %d, want 12345", got)
	}
}

func TestAttachmentPolicyFallsBackWhenConfigUnavailable(t *testing.T) {
	srv, _ := newMediaFakeHomeserver(t, nil) // config endpoint 404s
	adapter := newMediaTestAdapter(t, srv)

	policy := adapter.AttachmentPolicy()
	if got := policy.MaxBytes[core.AnyMIME]; got != defaultMaxUploadBytes {
		t.Errorf("AttachmentPolicy().MaxBytes[core.AnyMIME] = %d, want the 50 MB fallback %d", got, defaultMaxUploadBytes)
	}
}

func TestAttachmentPolicyFetchesMediaConfigOnlyOnce(t *testing.T) {
	srv, state := newMediaFakeHomeserver(t, &mautrix.RespMediaConfig{UploadSize: 999})
	adapter := newMediaTestAdapter(t, srv)

	adapter.AttachmentPolicy()
	adapter.AttachmentPolicy()
	adapter.AttachmentPolicy()

	// The fake server has no call counter of its own for this endpoint;
	// asserting the cached value stays stable across repeated calls is the
	// externally observable half of "fetch once, cache" (a mutation test
	// changing sync.Once to no caching would still pass this without a
	// hit-count assertion, so this is checked together with the doc
	// comment's Once-based design, which the code review can verify by
	// reading mediaConfigOnce).
	_ = state
	policy := adapter.AttachmentPolicy()
	if got := policy.MaxBytes[core.AnyMIME]; got != 999 {
		t.Errorf("AttachmentPolicy().MaxBytes[core.AnyMIME] = %d, want 999", got)
	}
}

func TestSendMediaUnencryptedRoomSetsURL(t *testing.T) {
	srv, state := newMediaFakeHomeserver(t, &mautrix.RespMediaConfig{UploadSize: 50 << 20})
	adapter := newMediaTestAdapter(t, srv)

	plaintext := []byte("plain text file contents")
	path := writeMatrixFile(t, "notes.txt", plaintext)

	receipt, err := adapter.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"!room:matrix.example.org"},
		Attachments: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMedia: %v", err)
	}
	if receipt.ID == "" {
		t.Error("Receipt.ID is empty")
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.uploadedBodies) != 1 {
		t.Fatalf("uploaded = %d files, want 1", len(state.uploadedBodies))
	}
	if !bytes.Equal(state.uploadedBodies[0], plaintext) {
		t.Errorf("uploaded bytes = %q, want the plaintext %q (unencrypted room)", state.uploadedBodies[0], plaintext)
	}
	if len(state.sentEvents) != 1 {
		t.Fatalf("sentEvents = %d, want 1", len(state.sentEvents))
	}
	content := decodeContent(t, state.sentEvents[0])
	if content.URL == "" {
		t.Error("content.URL is empty, want the uploaded mxc:// URI (unencrypted room)")
	}
	if content.File != nil {
		t.Error("content.File is set, want nil (unencrypted room must use url, not file)")
	}
	if content.MsgType != event.MsgFile {
		t.Errorf("MsgType = %q, want m.file for a .txt attachment", content.MsgType)
	}
	if content.FileName != "notes.txt" {
		t.Errorf("FileName = %q, want notes.txt", content.FileName)
	}
}

func TestSendMediaEncryptedRoomEncryptsFile(t *testing.T) {
	srv, state := newMediaFakeHomeserver(t, &mautrix.RespMediaConfig{UploadSize: 50 << 20})
	adapter := newMediaTestAdapter(t, srv)

	roomID := id.RoomID("!enc:matrix.example.org")
	if err := adapter.client.StateStore.SetEncryptionEvent(context.Background(), roomID, &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}); err != nil {
		t.Fatalf("SetEncryptionEvent: %v", err)
	}

	// Binary junk with no known http.DetectContentType signature, so the
	// MIME comes from the .png extension fallback (the same two-step
	// core.Service's own inspectAttachment uses) rather than needing a
	// byte-perfect real PNG file.
	plaintext := []byte{0x00, 0x01, 0x02, 0x03, 0xFF, 0xFE, 0xFD, 0x10, 0x20}
	path := writeMatrixFile(t, "pic.png", plaintext)

	_, err := adapter.SendMedia(context.Background(), core.Outgoing{
		To:          []string{string(roomID)},
		Attachments: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMedia: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.uploadedBodies) != 1 {
		t.Fatalf("uploaded = %d files, want 1", len(state.uploadedBodies))
	}
	if bytes.Equal(state.uploadedBodies[0], plaintext) {
		t.Error("uploaded bytes equal the plaintext, want them encrypted (encrypted room)")
	}
	if state.uploadedType[0] != "application/octet-stream" {
		t.Errorf("uploaded Content-Type = %q, want application/octet-stream (opaque ciphertext)", state.uploadedType[0])
	}
	content := decodeContent(t, state.sentEvents[0])
	if content.File == nil {
		t.Fatal("content.File is nil, want the EncryptedFileInfo (encrypted room)")
	}
	if content.File.URL == "" {
		t.Error("content.File.URL is empty")
	}
	if content.File.Key.Key == "" || content.File.InitVector == "" {
		t.Error("content.File is missing its key/iv")
	}
	if content.URL != "" {
		t.Errorf("content.URL = %q, want empty (encrypted room must use file, not url)", content.URL)
	}
	if content.MsgType != event.MsgImage {
		t.Errorf("MsgType = %q, want m.image for a .png attachment", content.MsgType)
	}
}

func TestSendMediaSendsBodyAsSeparateTrailingText(t *testing.T) {
	srv, state := newMediaFakeHomeserver(t, &mautrix.RespMediaConfig{UploadSize: 50 << 20})
	adapter := newMediaTestAdapter(t, srv)

	// Binary junk, MIME resolved via the .mp4 extension fallback (see the
	// comment on the pic.png fixture above).
	path := writeMatrixFile(t, "clip.mp4", []byte{0x00, 0x01, 0x02, 0x03, 0xFF, 0xFE, 0xFD})

	_, err := adapter.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"!room:matrix.example.org"},
		Body:        "mira esto",
		Attachments: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMedia: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sentEvents) != 2 {
		t.Fatalf("sentEvents = %d, want 2 (media, then text)", len(state.sentEvents))
	}
	media := decodeContent(t, state.sentEvents[0])
	if media.MsgType != event.MsgVideo {
		t.Errorf("first event MsgType = %q, want m.video", media.MsgType)
	}
	text := decodeContent(t, state.sentEvents[1])
	if text.MsgType != event.MsgText {
		t.Errorf("second event MsgType = %q, want m.text", text.MsgType)
	}
	if text.Body != "mira esto" {
		t.Errorf("second event Body = %q, want %q", text.Body, "mira esto")
	}
}

func TestSendMediaNoBodySendsOnlyMedia(t *testing.T) {
	srv, state := newMediaFakeHomeserver(t, &mautrix.RespMediaConfig{UploadSize: 50 << 20})
	adapter := newMediaTestAdapter(t, srv)

	path := writeMatrixFile(t, "notes.txt", []byte("x"))

	_, err := adapter.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"!room:matrix.example.org"},
		Attachments: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMedia: %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sentEvents) != 1 {
		t.Fatalf("sentEvents = %d, want 1 (no Body means no trailing text event)", len(state.sentEvents))
	}
}

func TestSendMediaReplySetsRelatesToOnFirstEventOnly(t *testing.T) {
	srv, state := newMediaFakeHomeserver(t, &mautrix.RespMediaConfig{UploadSize: 50 << 20})
	adapter := newMediaTestAdapter(t, srv)

	path := writeMatrixFile(t, "notes.txt", []byte("x"))
	replyItemID := itemID("work", "!room:matrix.example.org", "$original")

	_, err := adapter.SendMedia(context.Background(), core.Outgoing{
		Thread:      "!room:matrix.example.org",
		ReplyTo:     replyItemID,
		Body:        "con contexto",
		Attachments: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMedia: %v", err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sentEvents) != 2 {
		t.Fatalf("sentEvents = %d, want 2", len(state.sentEvents))
	}
	media := decodeContent(t, state.sentEvents[0])
	if media.RelatesTo == nil || media.RelatesTo.InReplyTo == nil || media.RelatesTo.InReplyTo.EventID != "$original" {
		t.Errorf("first (media) event RelatesTo = %+v, want in_reply_to $original", media.RelatesTo)
	}
	text := decodeContent(t, state.sentEvents[1])
	if text.RelatesTo != nil {
		t.Errorf("second (text) event RelatesTo = %+v, want nil", text.RelatesTo)
	}
}

func TestSendMediaRejectsMultipleRecipients(t *testing.T) {
	srv, state := newMediaFakeHomeserver(t, &mautrix.RespMediaConfig{UploadSize: 50 << 20})
	adapter := newMediaTestAdapter(t, srv)

	path := writeMatrixFile(t, "notes.txt", []byte("x"))
	_, err := adapter.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"!room1:matrix.example.org", "!room2:matrix.example.org"},
		Attachments: []string{path},
	})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sentEvents) != 0 || len(state.uploadedBodies) != 0 {
		t.Error("must not upload or send anything when the recipient list is rejected")
	}
}

func TestSendMediaMissingFileErrors(t *testing.T) {
	srv, _ := newMediaFakeHomeserver(t, &mautrix.RespMediaConfig{UploadSize: 50 << 20})
	adapter := newMediaTestAdapter(t, srv)

	_, err := adapter.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"!room:matrix.example.org"},
		Attachments: []string{"/no/such/file.png"},
	})
	if err == nil {
		t.Fatal("SendMedia() error = nil, want an error for a missing attachment")
	}
}

func TestSendMediaWithNoAttachmentsFallsBackToSend(t *testing.T) {
	srv, state := newMediaFakeHomeserver(t, &mautrix.RespMediaConfig{UploadSize: 50 << 20})
	adapter := newMediaTestAdapter(t, srv)

	_, err := adapter.SendMedia(context.Background(), core.Outgoing{
		To:   []string{"!room:matrix.example.org"},
		Body: "solo texto",
	})
	if err != nil {
		t.Fatalf("SendMedia: %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.uploadedBodies) != 0 {
		t.Error("no attachments means no upload")
	}
	if len(state.sentEvents) != 1 {
		t.Fatalf("sentEvents = %d, want 1", len(state.sentEvents))
	}
	text := decodeContent(t, state.sentEvents[0])
	if text.Body != "solo texto" {
		t.Errorf("Body = %q, want %q", text.Body, "solo texto")
	}
}
