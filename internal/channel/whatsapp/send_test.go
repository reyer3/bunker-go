package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestSendToJIDPlainText(t *testing.T) {
	cli := newFakeWAClient()
	cli.sendResp = whatsmeow.SendResponse{ID: "SENT1", Timestamp: time.Unix(1000, 0)}
	a := newTestAdapter("personal", cli, time.Millisecond)

	receipt, err := a.Send(context.Background(), core.Outgoing{
		Channel: core.ChannelWhatsApp,
		Account: "personal",
		To:      []string{"1234@s.whatsapp.net"},
		Body:    "hola",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if want := itemID("personal", "1234@s.whatsapp.net", "SENT1"); receipt.ID != want {
		t.Errorf("Receipt.ID = %q, want %q", receipt.ID, want)
	}
	if len(cli.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(cli.sent))
	}
	if cli.sent[0].message.GetConversation() != "hola" {
		t.Errorf("sent conversation = %q, want %q", cli.sent[0].message.GetConversation(), "hola")
	}
}

// TestSendRejectsMultipleRecipients covers T12(d): WhatsApp cannot address
// more than one recipient. Send must report ErrUnsupported instead of
// silently delivering to out.To[0] and dropping the rest.
func TestSendRejectsMultipleRecipients(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, time.Millisecond)

	_, err := a.Send(context.Background(), core.Outgoing{
		To:   []string{"1111@s.whatsapp.net", "2222@s.whatsapp.net"},
		Body: "hola",
	})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if len(cli.sent) != 0 {
		t.Fatalf("sent messages = %+v, want 0 (must not send to only the first)", cli.sent)
	}
}

// TestSendRejectsCc covers T12(d): WhatsApp has no Cc concept. Send must
// report ErrUnsupported instead of silently dropping the Cc list.
func TestSendRejectsCc(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, time.Millisecond)

	_, err := a.Send(context.Background(), core.Outgoing{
		To:   []string{"1111@s.whatsapp.net"},
		Cc:   []string{"2222@s.whatsapp.net"},
		Body: "hola",
	})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if len(cli.sent) != 0 {
		t.Fatalf("sent messages = %+v, want 0 (must not silently drop Cc)", cli.sent)
	}
}

func TestSendResolvesPhoneNumberViaIsOnWhatsApp(t *testing.T) {
	cli := newFakeWAClient()
	target := mustJID(t, "56912345678@s.whatsapp.net")
	cli.isOnWAResults = []types.IsOnWhatsAppResponse{{Query: "+56912345678", JID: target, IsIn: true}}
	a := newTestAdapter("personal", cli, time.Millisecond)

	_, err := a.Send(context.Background(), core.Outgoing{To: []string{"+56912345678"}, Body: "hola"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(cli.sent) != 1 || cli.sent[0].to != target {
		t.Fatalf("sent to = %+v, want target %v", cli.sent, target)
	}
}

func TestSendPhoneNotOnWhatsAppFails(t *testing.T) {
	cli := newFakeWAClient()
	cli.isOnWAResults = []types.IsOnWhatsAppResponse{{IsIn: false}}
	a := newTestAdapter("personal", cli, time.Millisecond)

	_, err := a.Send(context.Background(), core.Outgoing{To: []string{"+56900000000"}, Body: "hola"})
	if err == nil {
		t.Fatal("Send() error = nil, want an error for a number not on WhatsApp")
	}
}

func TestSendReplyQuotesContextInfo(t *testing.T) {
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

	_, err := a.Send(context.Background(), core.Outgoing{
		Thread:  "1234@s.whatsapp.net",
		ReplyTo: origID,
		Body:    "respuesta",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(cli.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(cli.sent))
	}
	ext := cli.sent[0].message.GetExtendedTextMessage()
	if ext == nil {
		t.Fatalf("sent message has no ExtendedTextMessage: %+v", cli.sent[0].message)
	}
	if ext.GetText() != "respuesta" {
		t.Errorf("text = %q, want %q", ext.GetText(), "respuesta")
	}
	ci := ext.GetContextInfo()
	if ci.GetStanzaID() != "M1" {
		t.Errorf("ContextInfo.StanzaID = %q, want M1", ci.GetStanzaID())
	}
	if ci.GetParticipant() != sender.String() {
		t.Errorf("ContextInfo.Participant = %q, want %q", ci.GetParticipant(), sender.String())
	}
}

func TestSendPacesConsecutiveSends(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, 30*time.Millisecond)

	start := time.Now()
	for i := 0; i < 2; i++ {
		if _, err := a.Send(context.Background(), core.Outgoing{To: []string{"1234@s.whatsapp.net"}, Body: "x"}); err != nil {
			t.Fatalf("Send() error = %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("two sends took %v, want at least the 30ms pacing interval", elapsed)
	}
}

func TestSendRespectsContextCancelDuringPacing(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, time.Hour)
	if _, err := a.Send(context.Background(), core.Outgoing{To: []string{"1234@s.whatsapp.net"}, Body: "first"}); err != nil {
		t.Fatalf("first Send() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := a.Send(ctx, core.Outgoing{To: []string{"1234@s.whatsapp.net"}, Body: "second"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send() error = %v, want context.DeadlineExceeded", err)
	}
}
