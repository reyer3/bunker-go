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
	"github.com/reyer3/bunker-go/internal/oggfixture"
)

// A send's receipt must name the chat live ingest keys the same person's
// messages on (enrichItem): the phone-number JID when a LID resolves to
// one, so the stored sent item joins that conversation instead of a
// separate one keyed by the LID or by the number as typed.
func TestSendReceiptUsesCanonicalChat(t *testing.T) {
	const (
		lid = "100000000000001@lid"
		pn  = "56900000001@s.whatsapp.net"
	)
	tests := []struct {
		name       string
		out        core.Outgoing
		isOnWA     types.JID
		mapping    bool
		wantThread string
	}{
		{name: "bare number resolved to a mapped LID", out: core.Outgoing{To: []string{"56900000001"}}, isOnWA: mustJIDNoT(lid), mapping: true, wantThread: pn},
		{name: "bare number resolved to a PN", out: core.Outgoing{To: []string{"+56900000001"}}, isOnWA: mustJIDNoT(pn), wantThread: pn},
		{name: "LID without a known mapping stays the LID", out: core.Outgoing{To: []string{"56900000001"}}, isOnWA: mustJIDNoT(lid), wantThread: lid},
		{name: "explicit LID thread with a mapping", out: core.Outgoing{Thread: lid}, mapping: true, wantThread: pn},
		{name: "group thread is kept", out: core.Outgoing{Thread: "120363000000000001@g.us"}, mapping: true, wantThread: "120363000000000001@g.us"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := newFakeWAClient()
			cli.sendResp = whatsmeow.SendResponse{ID: "SENT-1", Timestamp: time.Unix(5000, 0)}
			if !tt.isOnWA.IsEmpty() {
				cli.isOnWAResults = []types.IsOnWhatsAppResponse{{JID: tt.isOnWA, IsIn: true}}
			}
			a := newTestAdapter("personal", cli, time.Millisecond)
			names := newFakeNameResolver()
			if tt.mapping {
				names.pnForLID[mustJID(t, lid)] = mustJID(t, pn)
			}
			a.SetNameResolver(names)

			out := tt.out
			out.Body = "hola"
			receipt, err := a.Send(context.Background(), out)
			if err != nil {
				t.Fatalf("Send() error = %v", err)
			}
			if receipt.Thread != tt.wantThread {
				t.Errorf("Receipt.Thread = %q, want %q", receipt.Thread, tt.wantThread)
			}
			if want := itemID("personal", tt.wantThread, "SENT-1"); receipt.ID != want {
				t.Errorf("Receipt.ID = %q, want %q (keyed like ingest keys the same message)", receipt.ID, want)
			}
		})
	}
}

func TestSendMediaAndVoiceReceiptsUseCanonicalChat(t *testing.T) {
	const (
		lid = "100000000000001@lid"
		pn  = "56900000001@s.whatsapp.net"
	)
	setup := func(t *testing.T) *Adapter {
		cli := newFakeWAClient()
		cli.uploadResp = whatsmeow.UploadResponse{URL: "https://example/media", DirectPath: "/v/abc", FileLength: 4}
		cli.sendResp = whatsmeow.SendResponse{ID: "SENT-M", Timestamp: time.Unix(6000, 0)}
		a := newTestAdapter("personal", cli, time.Millisecond)
		names := newFakeNameResolver()
		names.pnForLID[mustJID(t, lid)] = mustJID(t, pn)
		a.SetNameResolver(names)
		return a
	}
	check := func(t *testing.T, receipt core.Receipt) {
		t.Helper()
		if receipt.Thread != pn {
			t.Errorf("Receipt.Thread = %q, want %q", receipt.Thread, pn)
		}
		if want := itemID("personal", pn, "SENT-M"); receipt.ID != want {
			t.Errorf("Receipt.ID = %q, want %q", receipt.ID, want)
		}
	}

	t.Run("media", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pic.jpg")
		if err := os.WriteFile(path, []byte("fake-jpeg-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		receipt, err := setup(t).SendMedia(context.Background(), core.Outgoing{Thread: lid, Attachments: []string{path}})
		if err != nil {
			t.Fatalf("SendMedia() error = %v", err)
		}
		check(t, receipt)
	})
	t.Run("voice", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nota.ogg")
		if err := oggfixture.Write(path, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		receipt, err := setup(t).SendVoice(context.Background(), core.Outgoing{Thread: lid, Attachments: []string{path}, Voice: true})
		if err != nil {
			t.Fatalf("SendVoice() error = %v", err)
		}
		check(t, receipt)
	})
}

func mustJIDNoT(s string) types.JID {
	jid, err := types.ParseJID(s)
	if err != nil {
		panic(err)
	}
	return jid
}
