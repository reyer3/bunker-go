package whatsapp

import (
	"context"
	"errors"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func boolPtr(b bool) *bool { return &b }

func TestOrganizeSeenTrueMarksRead(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	a := newTestAdapter("personal", cli)
	sink := newSpySink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "1234@s.whatsapp.net")
	sender := mustJID(t, "9999@s.whatsapp.net")
	cli.emit(quotedMessageEvent(chat, sender, "M1", "hola"))
	id := itemID("personal", "1234@s.whatsapp.net", "M1")
	waitFor(t, func() bool { _, ok := a.cachedItem(id); return ok })

	if err := a.Organize(context.Background(), id, core.OrganizeOp{Seen: boolPtr(true)}); err != nil {
		t.Fatalf("Organize() error = %v", err)
	}
	if len(cli.markedRead) != 1 {
		t.Fatalf("MarkRead calls = %d, want 1", len(cli.markedRead))
	}
	call := cli.markedRead[0]
	if call.chat != chat || call.sender != sender || len(call.ids) != 1 || call.ids[0] != "M1" {
		t.Errorf("MarkRead call = %+v, unexpected", call)
	}
}

func TestOrganizeSeenFalseIsUnsupported(t *testing.T) {
	a := newTestAdapter("personal", newFakeWAClient())
	err := a.Organize(context.Background(), "whatsapp:personal:1234@s.whatsapp.net/M1", core.OrganizeOp{Seen: boolPtr(false)})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("Organize() error = %v, want core.ErrUnsupported", err)
	}
}

func TestOrganizeLabelsAreUnsupported(t *testing.T) {
	a := newTestAdapter("personal", newFakeWAClient())
	err := a.Organize(context.Background(), "whatsapp:personal:1234@s.whatsapp.net/M1", core.OrganizeOp{AddLabels: []string{"vip"}})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("Organize() error = %v, want core.ErrUnsupported", err)
	}
}

func TestOrganizeNoOpWhenSeenNil(t *testing.T) {
	a := newTestAdapter("personal", newFakeWAClient())
	if err := a.Organize(context.Background(), "whatsapp:personal:1234@s.whatsapp.net/M1", core.OrganizeOp{}); err != nil {
		t.Fatalf("Organize() error = %v, want nil for a no-op", err)
	}
}
