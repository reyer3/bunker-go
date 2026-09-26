package mail

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

var _ core.MediaSender = (*Adapter)(nil)

func TestAttachmentPolicyAcceptsAnyTypeWithinMailLimits(t *testing.T) {
	p := (&Adapter{}).AttachmentPolicy()
	if got := p.MaxBytes[core.AnyMIME]; got != maxAttachmentBytes {
		t.Errorf("MaxBytes[%q] = %d, want %d", core.AnyMIME, got, maxAttachmentBytes)
	}
	if p.MaxTotalBytes != maxAttachmentBytes {
		t.Errorf("MaxTotalBytes = %d, want %d", p.MaxTotalBytes, maxAttachmentBytes)
	}
	// Base64 inflates ~4/3; the encoded total must stay under Gmail's 25 MB.
	if maxAttachmentBytes*4/3 >= 25<<20 {
		t.Errorf("maxAttachmentBytes %d encodes to >= 25 MB", maxAttachmentBytes)
	}
}

func TestAdapterSendMediaAttachesFiles(t *testing.T) {
	imapAddr, mem, _ := newMemIMAPServer(t)
	if err := mem.Create("INBOX/Sent", nil); err != nil {
		t.Fatalf("create INBOX/Sent: %v", err)
	}
	smtpAddr, backend := newTestSMTPServer(t)
	cfg := AccountConfig{Name: "cl", Username: "alice@example.cl", IMAPHost: "example.cl", FolderPrefix: "INBOX", FolderSeparator: '.'}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(imapAddr))
	adapter.smtpDial = testSMTPDialInsecure(smtpAddr)

	path := filepath.Join(t.TempDir(), "carrusel.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nfake"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"team@example.org"}, Subject: "Carrusel", Body: "Hola a todos", Attachments: []string{path}}
	receipt, err := adapter.SendMedia(context.Background(), out)
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	if receipt.ID == "" {
		t.Error("Receipt.ID is empty")
	}
	subs := backend.submissions()
	if len(subs) != 1 {
		t.Fatalf("len(submissions) = %d, want 1", len(subs))
	}
	data := string(subs[0].data)
	for _, want := range []string{"multipart/mixed", `filename=carrusel.png`, "image/png", "Hola a todos"} {
		if !strings.Contains(data, want) {
			t.Errorf("submitted message lacks %q", want)
		}
	}
	verifyMailboxHasMessages(t, imapAddr, "INBOX/Sent", 1)
}
