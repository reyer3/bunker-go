package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestPostStatusText(t *testing.T) {
	cli := newFakeWAClient()
	cli.sendResp = whatsmeow.SendResponse{ID: "STATUS1", Timestamp: time.Unix(2000, 0)}
	a := newTestAdapter("personal", cli, time.Millisecond)

	receipt, err := a.PostStatus(context.Background(), core.Status{Text: "buen dia", Background: "#112233"})
	if err != nil {
		t.Fatalf("PostStatus() error = %v", err)
	}
	if want := itemID("personal", types.StatusBroadcastJID.String(), "STATUS1"); receipt.ID != want {
		t.Errorf("Receipt.ID = %q, want %q", receipt.ID, want)
	}
	if len(cli.sent) != 1 || cli.sent[0].to != types.StatusBroadcastJID {
		t.Fatalf("sent = %+v, want one message to the status broadcast JID", cli.sent)
	}
	ext := cli.sent[0].message.GetExtendedTextMessage()
	if ext == nil || ext.GetText() != "buen dia" {
		t.Fatalf("sent message = %+v, want ExtendedTextMessage text 'buen dia'", cli.sent[0].message)
	}
	if ext.GetBackgroundArgb() != 0xFF112233 {
		t.Errorf("BackgroundArgb = %#x, want 0xff112233", ext.GetBackgroundArgb())
	}
}

func TestPostStatusImage(t *testing.T) {
	cli := newFakeWAClient()
	cli.uploadResp = whatsmeow.UploadResponse{
		URL: "https://example/media", DirectPath: "/v/abc", MediaKey: []byte("key"),
		FileEncSHA256: []byte("enc"), FileSHA256: []byte("sha"), FileLength: 4,
	}
	cli.sendResp = whatsmeow.SendResponse{ID: "STATUS2", Timestamp: time.Unix(3000, 0)}
	a := newTestAdapter("personal", cli, time.Millisecond)

	dir := t.TempDir()
	path := filepath.Join(dir, "pic.jpg")
	if err := os.WriteFile(path, []byte("fake-jpeg-bytes"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	receipt, err := a.PostStatus(context.Background(), core.Status{Text: "una foto", Media: path})
	if err != nil {
		t.Fatalf("PostStatus() error = %v", err)
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
	if img.GetCaption() != "una foto" || img.GetURL() != "https://example/media" || img.GetDirectPath() != "/v/abc" || img.GetFileLength() != 4 {
		t.Errorf("ImageMessage = %+v, unexpected", img)
	}
}

func TestPostStatusImageMissingFile(t *testing.T) {
	a := newTestAdapter("personal", newFakeWAClient(), time.Millisecond)
	_, err := a.PostStatus(context.Background(), core.Status{Media: "/no/such/file.jpg"})
	if err == nil {
		t.Fatal("PostStatus() error = nil, want an error for a missing media file")
	}
}

func TestParseBackgroundARGB(t *testing.T) {
	cases := []struct {
		in      string
		want    uint32
		wantNil bool
	}{
		{in: "", wantNil: true},
		{in: "#112233", want: 0xFF112233},
		{in: "112233", want: 0xFF112233},
		{in: "0xAA112233", want: 0xAA112233},
		{in: "not-a-color", wantNil: true},
	}
	for _, tc := range cases {
		got := parseBackgroundARGB(tc.in)
		if tc.wantNil {
			if got != nil {
				t.Errorf("parseBackgroundARGB(%q) = %#x, want nil", tc.in, *got)
			}
			continue
		}
		if got == nil || *got != tc.want {
			t.Errorf("parseBackgroundARGB(%q) = %v, want %#x", tc.in, got, tc.want)
		}
	}
}
