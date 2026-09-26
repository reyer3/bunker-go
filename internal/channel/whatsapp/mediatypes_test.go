package whatsapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"

	"github.com/reyer3/bunker-go/internal/core"
)

// T16(a): WhatsApp media beyond images. buildMediaMessage classifies each
// attachment by its file extension and dispatches to the matching waE2E
// message type; SendMedia's per-attachment loop uses it instead of always
// building an ImageMessage.

func writeFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
	return path
}

func TestSendMediaVideoBuildsVideoMessage(t *testing.T) {
	cli := newFakeWAClient()
	cli.uploadResp = whatsmeow.UploadResponse{
		URL: "https://example/video", DirectPath: "/v/vid", MediaKey: []byte("key"),
		FileEncSHA256: []byte("enc"), FileSHA256: []byte("sha"), FileLength: 9,
	}
	cli.sendResp = whatsmeow.SendResponse{ID: "SENT-VID", Timestamp: time.Unix(5000, 0)}
	a := NewAdapter("personal", cli, time.Millisecond)

	path := writeFile(t, t.TempDir(), "clip.mp4", 9)

	receipt, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Body:        "mira el video",
		Attachments: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	if receipt.ID == "" {
		t.Fatal("Receipt.ID is empty")
	}
	if len(cli.sent) != 1 {
		t.Fatalf("sent = %d messages, want 1", len(cli.sent))
	}
	vid := cli.sent[0].message.GetVideoMessage()
	if vid == nil {
		t.Fatalf("sent message has no VideoMessage: %+v", cli.sent[0].message)
	}
	if vid.GetCaption() != "mira el video" {
		t.Errorf("Caption = %q, want %q", vid.GetCaption(), "mira el video")
	}
	if vid.GetMimetype() != "video/mp4" {
		t.Errorf("Mimetype = %q, want video/mp4", vid.GetMimetype())
	}
	if vid.GetURL() != "https://example/video" || vid.GetFileLength() != 9 {
		t.Errorf("VideoMessage = %+v, unexpected upload fields", vid)
	}
	if cli.uploadedType != whatsmeow.MediaVideo {
		t.Errorf("Upload media type = %q, want MediaVideo", cli.uploadedType)
	}
}

func TestSendMediaThreeGPBuildsVideoMessage(t *testing.T) {
	cli := newFakeWAClient()
	cli.uploadResp = whatsmeow.UploadResponse{FileLength: 3}
	a := NewAdapter("personal", cli, time.Millisecond)

	path := writeFile(t, t.TempDir(), "clip.3gp", 3)

	_, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Attachments: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	vid := cli.sent[0].message.GetVideoMessage()
	if vid == nil {
		t.Fatalf("sent message has no VideoMessage: %+v", cli.sent[0].message)
	}
	if vid.GetMimetype() != "video/3gpp" {
		t.Errorf("Mimetype = %q, want video/3gpp", vid.GetMimetype())
	}
}

func TestSendMediaAudioBuildsNonPTTAudioMessage(t *testing.T) {
	cli := newFakeWAClient()
	cli.uploadResp = whatsmeow.UploadResponse{
		URL: "https://example/audio", FileLength: 7,
	}
	cli.sendResp = whatsmeow.SendResponse{ID: "SENT-AUD", Timestamp: time.Unix(6000, 0)}
	a := NewAdapter("personal", cli, time.Millisecond)

	path := writeFile(t, t.TempDir(), "clip.ogg", 7)

	_, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Attachments: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	aud := cli.sent[0].message.GetAudioMessage()
	if aud == nil {
		t.Fatalf("sent message has no AudioMessage: %+v", cli.sent[0].message)
	}
	if aud.GetPTT() {
		t.Error("AudioMessage.PTT = true, want false (voice-note emulation is out of scope)")
	}
	if aud.GetMimetype() != "audio/ogg" {
		t.Errorf("Mimetype = %q, want audio/ogg", aud.GetMimetype())
	}
	if cli.uploadedType != whatsmeow.MediaAudio {
		t.Errorf("Upload media type = %q, want MediaAudio", cli.uploadedType)
	}
}

func TestSendMediaAacBuildsAudioMessage(t *testing.T) {
	cli := newFakeWAClient()
	a := NewAdapter("personal", cli, time.Millisecond)
	path := writeFile(t, t.TempDir(), "voice.aac", 4)

	_, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Attachments: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	aud := cli.sent[0].message.GetAudioMessage()
	if aud == nil {
		t.Fatalf("sent message has no AudioMessage: %+v", cli.sent[0].message)
	}
	if aud.GetMimetype() != "audio/aac" {
		t.Errorf("Mimetype = %q, want audio/aac", aud.GetMimetype())
	}
}

func TestSendMediaUnknownTypeBuildsDocumentMessage(t *testing.T) {
	cli := newFakeWAClient()
	cli.uploadResp = whatsmeow.UploadResponse{
		URL: "https://example/doc", DirectPath: "/v/doc", MediaKey: []byte("key"),
		FileEncSHA256: []byte("enc"), FileSHA256: []byte("sha"), FileLength: 5,
	}
	cli.sendResp = whatsmeow.SendResponse{ID: "SENT-DOC", Timestamp: time.Unix(7000, 0)}
	a := NewAdapter("personal", cli, time.Millisecond)

	path := writeFile(t, t.TempDir(), "report.pdf", 5)

	receipt, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Body:        "el reporte",
		Attachments: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	if receipt.ID == "" {
		t.Fatal("Receipt.ID is empty")
	}
	doc := cli.sent[0].message.GetDocumentMessage()
	if doc == nil {
		t.Fatalf("sent message has no DocumentMessage: %+v", cli.sent[0].message)
	}
	if doc.GetFileName() != "report.pdf" {
		t.Errorf("FileName = %q, want report.pdf", doc.GetFileName())
	}
	if doc.GetTitle() != "report.pdf" {
		t.Errorf("Title = %q, want report.pdf", doc.GetTitle())
	}
	if doc.GetCaption() != "el reporte" {
		t.Errorf("Caption = %q, want %q", doc.GetCaption(), "el reporte")
	}
	if cli.uploadedType != whatsmeow.MediaDocument {
		t.Errorf("Upload media type = %q, want MediaDocument", cli.uploadedType)
	}
}

func TestSendMediaMixedTypesOnlyFirstIsCaptioned(t *testing.T) {
	cli := newFakeWAClient()
	a := NewAdapter("personal", cli, time.Millisecond)
	dir := t.TempDir()
	imgPath := writeFile(t, dir, "pic.png", 4)
	vidPath := writeFile(t, dir, "clip.mp4", 4)
	docPath := writeFile(t, dir, "notes.txt", 4)

	_, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Body:        "solo la primera",
		Attachments: []string{imgPath, vidPath, docPath},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	if len(cli.sent) != 3 {
		t.Fatalf("sent = %d, want 3", len(cli.sent))
	}
	if got := cli.sent[0].message.GetImageMessage().GetCaption(); got != "solo la primera" {
		t.Errorf("first (image) caption = %q, want the body", got)
	}
	if got := cli.sent[1].message.GetVideoMessage().GetCaption(); got != "" {
		t.Errorf("second (video) caption = %q, want empty", got)
	}
	if got := cli.sent[2].message.GetDocumentMessage().GetCaption(); got != "" {
		t.Errorf("third (document) caption = %q, want empty", got)
	}
}

func TestSendMediaRejectsOversizedVideo(t *testing.T) {
	cli := newFakeWAClient()
	a := NewAdapter("personal", cli, time.Millisecond)
	path := writeFile(t, t.TempDir(), "huge.mp4", 0)
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if err := f.Truncate(maxMediaBytes + 1); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	f.Close()

	_, err = a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Attachments: []string{path},
	})
	if err == nil {
		t.Fatal("SendMedia() error = nil, want an error for a video over the 16 MB limit")
	}
	if len(cli.sent) != 0 {
		t.Fatalf("sent = %d messages, want 0", len(cli.sent))
	}
}

func TestSendMediaRejectsOversizedDocument(t *testing.T) {
	cli := newFakeWAClient()
	a := NewAdapter("personal", cli, time.Millisecond)
	path := writeFile(t, t.TempDir(), "huge.bin", 0)
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if err := f.Truncate(maxDocumentBytes + 1); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	f.Close()

	_, err = a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Attachments: []string{path},
	})
	if err == nil {
		t.Fatal("SendMedia() error = nil, want an error for a document over the 100 MB limit")
	}
	if len(cli.sent) != 0 {
		t.Fatalf("sent = %d messages, want 0", len(cli.sent))
	}
}

func TestSendMediaReplyQuotesFirstNonImageAttachment(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	a := NewAdapter("personal", cli, time.Millisecond)
	sink := newSpySink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "1234@s.whatsapp.net")
	sender := mustJID(t, "9999@s.whatsapp.net")
	cli.emit(quotedMessageEvent(chat, sender, "M1", "mensaje original"))
	origID := itemID("personal", "1234@s.whatsapp.net", "M1")
	waitFor(t, func() bool { _, ok := a.cachedItem(origID); return ok })

	path := writeFile(t, t.TempDir(), "clip.mp4", 4)

	_, err := a.SendMedia(context.Background(), core.Outgoing{
		Thread:      "1234@s.whatsapp.net",
		ReplyTo:     origID,
		Attachments: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	vidCtx := cli.sent[0].message.GetVideoMessage().GetContextInfo()
	if vidCtx.GetStanzaID() != "M1" {
		t.Errorf("VideoMessage ContextInfo.StanzaID = %q, want M1", vidCtx.GetStanzaID())
	}
}

// TestAttachmentPolicyCoversAllMediaTypes covers T16(a): the policy grows
// video/audio entries at the 16 MB image/video/audio limit, plus AnyMIME
// at the 100 MB document limit for everything else, without removing the
// existing image entries.
func TestAttachmentPolicyCoversAllMediaTypes(t *testing.T) {
	a := NewAdapter("personal", newFakeWAClient())
	policy := a.AttachmentPolicy()

	wantLimited := map[string]int64{
		"image/png":  maxMediaBytes,
		"image/jpeg": maxMediaBytes,
		"image/webp": maxMediaBytes,
		"video/mp4":  maxMediaBytes,
		"video/3gpp": maxMediaBytes,
		"audio/ogg":  maxMediaBytes,
		"audio/mpeg": maxMediaBytes,
		"audio/mp4":  maxMediaBytes,
		"audio/aac":  maxMediaBytes,
	}
	for mime, want := range wantLimited {
		got, ok := policy.MaxBytes[mime]
		if !ok {
			t.Errorf("AttachmentPolicy().MaxBytes[%q] missing", mime)
			continue
		}
		if got != want {
			t.Errorf("AttachmentPolicy().MaxBytes[%q] = %d, want %d", mime, got, want)
		}
	}
	if got, ok := policy.MaxBytes[core.AnyMIME]; !ok || got != maxDocumentBytes {
		t.Errorf("AttachmentPolicy().MaxBytes[core.AnyMIME] = (%d, %v), want (%d, true)", got, ok, maxDocumentBytes)
	}
}

func TestSendMediaMissingFileErrorsForNonImage(t *testing.T) {
	a := NewAdapter("personal", newFakeWAClient(), time.Millisecond)
	_, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Attachments: []string{"/no/such/file.mp4"},
	})
	if err == nil {
		t.Fatal("SendMedia() error = nil, want an error for a missing attachment")
	}
	if errors.Is(err, core.ErrUnsupported) {
		t.Fatal("a missing file must not be reported as ErrUnsupported")
	}
}

// TestSendMediaAudioWithBodySendsTextSeparately: AudioMessage has no Caption,
// so a body must go out as its own text message instead of being dropped.
func TestSendMediaAudioWithBodySendsTextSeparately(t *testing.T) {
	cli := newFakeWAClient()
	cli.uploadResp = whatsmeow.UploadResponse{URL: "https://example/audio", FileLength: 7}
	cli.sendResp = whatsmeow.SendResponse{ID: "SENT-AUD", Timestamp: time.Unix(6000, 0)}
	a := NewAdapter("personal", cli, time.Millisecond)

	path := writeFile(t, t.TempDir(), "clip.ogg", 7)
	_, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Body:        "escucha esto",
		Attachments: []string{path},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	if len(cli.sent) != 2 {
		t.Fatalf("sent %d messages, want 2 (audio + text)", len(cli.sent))
	}
	if cli.sent[0].message.GetAudioMessage() == nil {
		t.Fatalf("first message is not audio: %+v", cli.sent[0].message)
	}
	if got := cli.sent[1].message.GetConversation(); got != "escucha esto" {
		t.Errorf("second message text = %q, want the body", got)
	}
}
