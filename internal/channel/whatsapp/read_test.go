package whatsapp

import (
	"context"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestMarkReadCallOrderIsPresenceThenReceiptThenUnavailable covers T13(c):
// `bunker read <id>` on WhatsApp does presence available, then MarkRead
// (blue ticks), then presence unavailable — in that exact order.
func TestMarkReadCallOrderIsPresenceThenReceiptThenUnavailable(t *testing.T) {
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

	if err := a.MarkRead(context.Background(), id); err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}

	want := []string{"presence:available", "markread", "presence:unavailable"}
	got := cli.callLog()
	if len(got) != len(want) {
		t.Fatalf("call log = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call log = %+v, want %+v", got, want)
		}
	}

	if len(cli.markedRead) != 1 {
		t.Fatalf("MarkRead calls = %d, want 1", len(cli.markedRead))
	}
	call := cli.markedRead[0]
	if call.chat != chat || call.sender != sender || len(call.ids) != 1 || call.ids[0] != "M1" {
		t.Errorf("MarkRead call = %+v, unexpected", call)
	}
}

// TestMarkReadUnknownIDReturnsError covers the parse-error path: an
// unparseable id never reaches SendPresence at all.
func TestMarkReadUnknownIDReturnsError(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli)

	err := a.MarkRead(context.Background(), "not-a-valid-id")
	if err == nil {
		t.Fatal("MarkRead() error = nil, want an error for an unparseable id")
	}
	if len(cli.callLog()) != 0 {
		t.Fatalf("call log = %+v, want none (must fail before any waClient call)", cli.callLog())
	}
}

// TestMarkReadIsReadMarkerCapability is a static assertion that Adapter
// satisfies core.ReadMarker.
func TestMarkReadIsReadMarkerCapability(t *testing.T) {
	var a Adapter
	var _ core.ReadMarker = &a
}
