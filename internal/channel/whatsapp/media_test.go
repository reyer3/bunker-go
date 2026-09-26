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

// TestSendMediaRejectsMultipleRecipients covers T12(d)'s SendMedia half:
// resolveTarget's recipient guard applies to media sends too, not just
// plain text.
func TestSendMediaRejectsMultipleRecipients(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, time.Millisecond)

	path := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(path, []byte{0x89, 'P', 'N', 'G'}, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1111@s.whatsapp.net", "2222@s.whatsapp.net"},
		Attachments: []string{path},
	})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if len(cli.sent) != 0 {
		t.Fatalf("sent messages = %+v, want 0", cli.sent)
	}
}

func TestSendMediaSingleImageCaptionsIt(t *testing.T) {
	cli := newFakeWAClient()
	cli.uploadResp = whatsmeow.UploadResponse{
		URL: "https://example/media", DirectPath: "/v/abc", MediaKey: []byte("key"),
		FileEncSHA256: []byte("enc"), FileSHA256: []byte("sha"), FileLength: 4,
	}
	cli.sendResp = whatsmeow.SendResponse{ID: "SENT-IMG1", Timestamp: time.Unix(4000, 0)}
	a := newTestAdapter("personal", cli, time.Millisecond)

	dir := t.TempDir()
	path := filepath.Join(dir, "pic.jpg")
	if err := os.WriteFile(path, []byte("fake-jpeg-bytes"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	receipt, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Body:        "mira esto",
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
	img := cli.sent[0].message.GetImageMessage()
	if img == nil {
		t.Fatalf("sent message has no ImageMessage: %+v", cli.sent[0].message)
	}
	if img.GetCaption() != "mira esto" {
		t.Errorf("Caption = %q, want %q", img.GetCaption(), "mira esto")
	}
	if img.GetURL() != "https://example/media" || img.GetDirectPath() != "/v/abc" || img.GetFileLength() != 4 {
		t.Errorf("ImageMessage = %+v, unexpected upload fields", img)
	}
}

func TestSendMediaMultipleImagesOnlyFirstIsCaptioned(t *testing.T) {
	cli := newFakeWAClient()
	cli.uploadResp = whatsmeow.UploadResponse{URL: "https://example/media", FileLength: 1}
	a := newTestAdapter("personal", cli, time.Millisecond)

	dir := t.TempDir()
	path1 := filepath.Join(dir, "one.png")
	path2 := filepath.Join(dir, "two.png")
	if err := os.WriteFile(path1, []byte("\x89PNG-one"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(path2, []byte("\x89PNG-two"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Body:        "caption va en la primera",
		Attachments: []string{path1, path2},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	if len(cli.sent) != 2 {
		t.Fatalf("sent = %d messages, want 2 (one per attachment)", len(cli.sent))
	}
	first := cli.sent[0].message.GetImageMessage()
	second := cli.sent[1].message.GetImageMessage()
	if first == nil || second == nil {
		t.Fatalf("expected both sent messages to be ImageMessage: %+v / %+v", first, second)
	}
	if first.GetCaption() != "caption va en la primera" {
		t.Errorf("first caption = %q, want the body text", first.GetCaption())
	}
	if second.GetCaption() != "" {
		t.Errorf("second caption = %q, want empty (only the first image carries the caption)", second.GetCaption())
	}
}

// TestSendMediaRejectsOversizedImage covers the WhatsApp image upload
// limit (maxImageBytes): a file over it must be rejected before Upload is
// ever called, so no bytes get sent for something WhatsApp would refuse
// anyway. The file is created sparse (Truncate, not written) so the test
// doesn't actually allocate 16MB+1 of real disk content.
func TestSendMediaRejectsOversizedImage(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, time.Millisecond)

	dir := t.TempDir()
	path := filepath.Join(dir, "huge.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Truncate(maxImageBytes + 1); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Attachments: []string{path},
	})
	if err == nil {
		t.Fatal("SendMedia() error = nil, want an error for an image over the WhatsApp size limit")
	}
	if len(cli.sent) != 0 {
		t.Fatalf("sent = %d messages, want 0 (oversized image must never reach SendMessage)", len(cli.sent))
	}
}

func TestSendMediaMissingFileErrors(t *testing.T) {
	a := newTestAdapter("personal", newFakeWAClient(), time.Millisecond)
	_, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Attachments: []string{"/no/such/file.jpg"},
	})
	if err == nil {
		t.Fatal("SendMedia() error = nil, want an error for a missing attachment")
	}
	if len(newFakeWAClient().sent) != 0 {
		t.Fatal("no message should have been sent")
	}
}

// TestSendMediaReplyQuotesContextInfoOnFirstImageOnly covers T11b(5): a
// reply carrying attachments quotes the original message the same way a
// plain text reply does, via ContextInfo on the first image only —
// repeating the quote on every image would be as redundant as repeating
// the caption.
func TestSendMediaReplyQuotesContextInfoOnFirstImageOnly(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	a := newTestAdapter("personal", cli, time.Millisecond)
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

	dir := t.TempDir()
	path1 := filepath.Join(dir, "one.png")
	path2 := filepath.Join(dir, "two.png")
	if err := os.WriteFile(path1, []byte("\x89PNG-one"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(path2, []byte("\x89PNG-two"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := a.SendMedia(context.Background(), core.Outgoing{
		Thread:      "1234@s.whatsapp.net",
		ReplyTo:     origID,
		Body:        "mira esto",
		Attachments: []string{path1, path2},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	if len(cli.sent) != 2 {
		t.Fatalf("sent = %d, want 2", len(cli.sent))
	}
	firstCtx := cli.sent[0].message.GetImageMessage().GetContextInfo()
	if firstCtx.GetStanzaID() != "M1" {
		t.Errorf("first image ContextInfo.StanzaID = %q, want M1", firstCtx.GetStanzaID())
	}
	if firstCtx.GetParticipant() != sender.String() {
		t.Errorf("first image ContextInfo.Participant = %q, want %q", firstCtx.GetParticipant(), sender.String())
	}
	if secondCtx := cli.sent[1].message.GetImageMessage().GetContextInfo(); secondCtx != nil {
		t.Errorf("second image ContextInfo = %+v, want nil (only the first image quotes the original)", secondCtx)
	}
}

// TestAttachmentPolicyAcceptsImagesUpToWhatsAppLimit covers T11b(1)/(3):
// the adapter's core.AttachmentPolicy is the single source of the 16 MB
// WhatsApp image limit that core.Service validates against before ever
// calling SendMedia, and it must agree with maxImageBytes (the same
// constant buildImageMessage's own on-disk-size check uses). It no longer
// asserts video is absent from the policy: T16 (mediatypes_test.go,
// TestAttachmentPolicyCoversAllMediaTypes) added it.
func TestAttachmentPolicyAcceptsImagesUpToWhatsAppLimit(t *testing.T) {
	a := newTestAdapter("personal", newFakeWAClient(), time.Millisecond)
	policy := a.AttachmentPolicy()

	for _, mime := range []string{"image/png", "image/jpeg"} {
		got, ok := policy.MaxBytes[mime]
		if !ok {
			t.Fatalf("AttachmentPolicy().MaxBytes[%q] missing, want %d", mime, maxImageBytes)
		}
		if got != maxImageBytes {
			t.Errorf("AttachmentPolicy().MaxBytes[%q] = %d, want %d (maxImageBytes)", mime, got, maxImageBytes)
		}
	}
}

func TestSendMediaPacesEachImage(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, 30*time.Millisecond)

	dir := t.TempDir()
	path1 := filepath.Join(dir, "one.jpg")
	path2 := filepath.Join(dir, "two.jpg")
	os.WriteFile(path1, []byte("one"), 0o600)
	os.WriteFile(path2, []byte("two"), 0o600)

	start := time.Now()
	_, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Attachments: []string{path1, path2},
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("two image sends took %v, want at least the 30ms pacing interval", elapsed)
	}
}
