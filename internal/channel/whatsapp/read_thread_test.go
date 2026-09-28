package whatsapp

import (
	"context"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestMarkThreadReadBatchesOneCallPerSender covers K9 (conversation-view.md's
// read-thread fix): a DM's several unread ids resolve to the same (chat,
// sender) pair, so MarkThreadRead must send exactly one whatsmeow MarkRead
// call carrying every id, wrapped in the same presence available/
// unavailable sequence as the single-item MarkRead.
func TestMarkThreadReadBatchesOneCallPerSender(t *testing.T) {
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
	cli.emit(quotedMessageEvent(chat, sender, "M2", "que tal"))
	id1 := itemID("personal", "1234@s.whatsapp.net", "M1")
	id2 := itemID("personal", "1234@s.whatsapp.net", "M2")
	waitFor(t, func() bool { _, ok := a.cachedItem(id2); return ok })

	if err := a.MarkThreadRead(context.Background(), []string{id1, id2}); err != nil {
		t.Fatalf("MarkThreadRead() error = %v", err)
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
		t.Fatalf("MarkRead calls = %d, want 1 (batched)", len(cli.markedRead))
	}
	call := cli.markedRead[0]
	if call.chat != chat || call.sender != sender {
		t.Fatalf("MarkRead call target = %+v, want chat=%v sender=%v", call, chat, sender)
	}
	if len(call.ids) != 2 || call.ids[0] != "M1" || call.ids[1] != "M2" {
		t.Fatalf("MarkRead call ids = %+v, want [M1 M2]", call.ids)
	}
}

// TestMarkThreadReadGroupsByParticipantForGroupChats covers whatsmeow's
// group requirement: MarkRead must be called once per distinct sender
// within the same chat, each carrying only that sender's ids.
func TestMarkThreadReadGroupsByParticipantForGroupChats(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	a := newTestAdapter("personal", cli)
	sink := newSpySink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	group := mustJID(t, "5551234@g.us")
	alice := mustJID(t, "1111@s.whatsapp.net")
	bob := mustJID(t, "2222@s.whatsapp.net")
	cli.emit(quotedMessageEvent(group, alice, "M1", "hola"))
	cli.emit(quotedMessageEvent(group, bob, "M2", "hey"))
	cli.emit(quotedMessageEvent(group, alice, "M3", "otra vez"))
	id1 := itemID("personal", "5551234@g.us", "M1")
	id2 := itemID("personal", "5551234@g.us", "M2")
	id3 := itemID("personal", "5551234@g.us", "M3")
	waitFor(t, func() bool { _, ok := a.cachedItem(id3); return ok })

	if err := a.MarkThreadRead(context.Background(), []string{id1, id2, id3}); err != nil {
		t.Fatalf("MarkThreadRead() error = %v", err)
	}

	if len(cli.markedRead) != 2 {
		t.Fatalf("MarkRead calls = %d, want 2 (one per sender)", len(cli.markedRead))
	}
	aliceCall := cli.markedRead[0]
	if aliceCall.chat != group || aliceCall.sender != alice {
		t.Fatalf("first MarkRead call target = %+v, want chat=%v sender=%v (alice, first seen)", aliceCall, group, alice)
	}
	if len(aliceCall.ids) != 2 || aliceCall.ids[0] != "M1" || aliceCall.ids[1] != "M3" {
		t.Fatalf("alice's MarkRead ids = %+v, want [M1 M3]", aliceCall.ids)
	}
	bobCall := cli.markedRead[1]
	if bobCall.chat != group || bobCall.sender != bob {
		t.Fatalf("second MarkRead call target = %+v, want chat=%v sender=%v (bob)", bobCall, group, bob)
	}
	if len(bobCall.ids) != 1 || bobCall.ids[0] != "M2" {
		t.Fatalf("bob's MarkRead ids = %+v, want [M2]", bobCall.ids)
	}
}

// TestMarkThreadReadEmptyIsNoOp covers the idempotent-with-nothing-unread
// case: no ids means no waClient calls at all, not even presence.
func TestMarkThreadReadEmptyIsNoOp(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli)
	if err := a.MarkThreadRead(context.Background(), nil); err != nil {
		t.Fatalf("MarkThreadRead() error = %v, want nil for an empty batch", err)
	}
	if len(cli.callLog()) != 0 {
		t.Fatalf("call log = %+v, want none for an empty batch", cli.callLog())
	}
}

// TestMarkThreadReadUnknownIDFailsBeforeAnyCall mirrors
// TestMarkReadUnknownIDReturnsError: one unparseable id in the batch must
// fail before any waClient call, on WhatsApp's own or any other id's
// behalf.
func TestMarkThreadReadUnknownIDFailsBeforeAnyCall(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli)

	err := a.MarkThreadRead(context.Background(), []string{"whatsapp:personal:1234@s.whatsapp.net/M1", "not-a-valid-id"})
	if err == nil {
		t.Fatal("MarkThreadRead() error = nil, want an error for an unparseable id")
	}
	if len(cli.callLog()) != 0 {
		t.Fatalf("call log = %+v, want none (must fail before any waClient call)", cli.callLog())
	}
}

// TestMarkThreadReadIsThreadReaderCapability is a static assertion that
// Adapter satisfies core.ThreadReader.
func TestMarkThreadReadIsThreadReaderCapability(t *testing.T) {
	var a Adapter
	var _ core.ThreadReader = &a
}
