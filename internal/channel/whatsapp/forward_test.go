package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/oggfixture"
)

// assertForwarded checks the native "Forwarded" label WhatsApp clients
// render: IsForwarded plus a forwarding score of 1 (bunker does not know
// whether the original was itself a forward, so it never claims more).
func assertForwarded(t *testing.T, what string, ci *waE2E.ContextInfo) {
	t.Helper()
	if ci == nil {
		t.Fatalf("%s has no ContextInfo, want a forwarded one", what)
	}
	if !ci.GetIsForwarded() {
		t.Errorf("%s ContextInfo.IsForwarded = false, want true", what)
	}
	if ci.GetForwardingScore() != 1 {
		t.Errorf("%s ContextInfo.ForwardingScore = %d, want 1", what, ci.GetForwardingScore())
	}
}

func TestSendForwardMarksTextAsForwarded(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, time.Millisecond)

	_, err := a.Send(context.Background(), core.Outgoing{
		To:      []string{"1234@s.whatsapp.net"},
		Body:    "mensaje reenviado",
		Forward: true,
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(cli.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(cli.sent))
	}
	ext := cli.sent[0].message.GetExtendedTextMessage()
	if ext == nil {
		t.Fatalf("a forward must be an ExtendedTextMessage to carry ContextInfo: %+v", cli.sent[0].message)
	}
	if ext.GetText() != "mensaje reenviado" {
		t.Errorf("text = %q", ext.GetText())
	}
	assertForwarded(t, "text", ext.GetContextInfo())
}

func TestSendWithoutForwardStaysAPlainConversation(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, time.Millisecond)

	if _, err := a.Send(context.Background(), core.Outgoing{To: []string{"1234@s.whatsapp.net"}, Body: "hola"}); err != nil {
		t.Fatal(err)
	}
	if cli.sent[0].message.GetExtendedTextMessage() != nil {
		t.Errorf("a plain send became an ExtendedTextMessage: %+v", cli.sent[0].message)
	}
}

// TestSendMediaForwardMarksEveryAttachment: each attachment of a forward
// is forwarded content, so every media message carries the label (unlike
// a reply's quote, which only the first carries).
func TestSendMediaForwardMarksEveryAttachment(t *testing.T) {
	cli := newFakeWAClient()
	cli.uploadResp = whatsmeow.UploadResponse{URL: "https://example/media", DirectPath: "/v/abc", FileLength: 4}
	a := newTestAdapter("personal", cli, time.Millisecond)

	dir := t.TempDir()
	img := filepath.Join(dir, "pic.png")
	doc := filepath.Join(dir, "informe.pdf")
	for _, p := range []string{img, doc} {
		if err := os.WriteFile(p, []byte("\x89PNG-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, err := a.SendMedia(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Body:        "mira",
		Attachments: []string{img, doc},
		Forward:     true,
	})
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	if len(cli.sent) != 2 {
		t.Fatalf("sent = %d, want 2", len(cli.sent))
	}
	assertForwarded(t, "image", cli.sent[0].message.GetImageMessage().GetContextInfo())
	assertForwarded(t, "document", cli.sent[1].message.GetDocumentMessage().GetContextInfo())
}

func TestSendVoiceForwardMarksTheVoiceNote(t *testing.T) {
	cli := newFakeWAClient()
	cli.uploadResp = whatsmeow.UploadResponse{URL: "https://example/voice", DirectPath: "/v/x", FileLength: 400}
	a := NewAdapter("personal", cli, time.Millisecond)

	path := filepath.Join(t.TempDir(), "nota.ogg")
	if err := oggfixture.Write(path, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	_, err := a.SendVoice(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Attachments: []string{path},
		Voice:       true,
		Forward:     true,
	})
	if err != nil {
		t.Fatalf("SendVoice() error = %v", err)
	}
	aud := cli.sent[0].message.GetAudioMessage()
	if !aud.GetPTT() {
		t.Error("PTT = false, want a voice note")
	}
	assertForwarded(t, "voice note", aud.GetContextInfo())
}
