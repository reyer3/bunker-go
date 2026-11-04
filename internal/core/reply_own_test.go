package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// A message bunker sent is stored with To but no From (storeSentItem), so
// replying to it must go to that To, not fail on an empty sender.
func TestServiceReplyToOwnSentItemUsesItsRecipient(t *testing.T) {
	own := core.Item{ID: "whatsapp:wa:own1", Channel: core.ChannelWhatsApp, Account: "wa", Thread: "t1", FromMe: true, To: []core.Address{{ID: "51999@s.whatsapp.net"}}}
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(spy)
	store := newMemStore(own)
	svc := core.NewService(store, reg)

	plan, _, err := svc.Reply(context.Background(), own.ID, "hola amigo", nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Reply dry-run to own item: %v", err)
	}
	if len(plan.Recipients) != 1 || plan.Recipients[0] != "51999@s.whatsapp.net" {
		t.Errorf("plan.Recipients = %v, want the stored To", plan.Recipients)
	}

	if _, _, err := svc.Reply(context.Background(), own.ID, "hola amigo", nil, nil, nil, false); err != nil {
		t.Fatalf("Reply to own item: %v", err)
	}
	if got := spy.lastOutgoing.To; len(got) != 1 || got[0] != "51999@s.whatsapp.net" {
		t.Errorf("adapter To = %v, want the stored To", got)
	}

	// The copy stored for this reply keeps the addressee, so the next
	// reply to it works too instead of failing the same way.
	sent, err := store.Get(context.Background(), "sent-1")
	if err != nil {
		t.Fatalf("stored reply: %v", err)
	}
	if len(sent.To) != 1 || sent.To[0].ID != "51999@s.whatsapp.net" {
		t.Errorf("stored To = %+v, want the addressee", sent.To)
	}
	if _, _, err := svc.Reply(context.Background(), "sent-1", "otra vez", nil, nil, nil, true); err != nil {
		t.Errorf("reply to the stored reply: %v", err)
	}
}

// With nobody to address (no From and no To) the reply still fails loudly.
func TestServiceReplyToOwnItemWithoutAnyAddressStillFails(t *testing.T) {
	own := core.Item{ID: "whatsapp:wa:own2", Channel: core.ChannelWhatsApp, Account: "wa", Thread: "t1", FromMe: true}
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(spy)
	svc := core.NewService(newMemStore(own), reg)

	_, _, err := svc.Reply(context.Background(), own.ID, "x", nil, nil, nil, false)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if spy.sendCalls != 0 {
		t.Error("adapter was called")
	}
}

// An incoming item is still answered to its sender, even when To is set.
func TestServiceReplyToIncomingItemStillUsesSender(t *testing.T) {
	in := core.Item{ID: "whatsapp:wa:in1", Channel: core.ChannelWhatsApp, Account: "wa", Thread: "t1", From: core.Address{ID: "51111@s.whatsapp.net"}, To: []core.Address{{ID: "me@s.whatsapp.net"}}}
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(spy)
	svc := core.NewService(newMemStore(in), reg)

	if _, _, err := svc.Reply(context.Background(), in.ID, "x", nil, nil, nil, false); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got := spy.lastOutgoing.To; len(got) != 1 || got[0] != "51111@s.whatsapp.net" {
		t.Errorf("adapter To = %v, want the sender", got)
	}
}
