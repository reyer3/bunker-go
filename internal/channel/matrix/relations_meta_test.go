package matrix

import (
	"context"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"

	"github.com/reyer3/bunker-go/internal/core"
)

// Element threads and quoted replies: toItem must record m.relates_to's
// thread root and in_reply_to target in the item's Meta (full item ids,
// with the target's sender name when the room's history carried it
// through), and Send must answer INSIDE the thread (rel_type m.thread)
// when the replied-to message belongs to one — never unrolled into the
// bare room stream, which is what made group rooms unreadable.

func relationMetaAdapter(t *testing.T) *Adapter {
	t.Helper()
	client, err := mautrix.NewClient("https://matrix.example.org", relSelf, "token")
	if err != nil {
		t.Fatalf("mautrix.NewClient: %v", err)
	}
	return newAdapter("work", client, nil)
}

func threadRootEvent() *event.Event {
	return &event.Event{
		ID: "$root", RoomID: relRoom, Sender: relBob, Type: event.EventMessage, Timestamp: 1700000000000,
		Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "arranco el hilo"}},
	}
}

func threadChildEvent() *event.Event {
	// A reply inside the thread: m.thread relation, with the usual
	// in_reply_to fallback to the previous message.
	return &event.Event{
		ID: "$child", RoomID: relRoom, Sender: relCarol, Type: event.EventMessage, Timestamp: 1700000001000,
		Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType: event.MsgText, Body: "sigo el hilo",
			RelatesTo: (&event.RelatesTo{}).SetThread("$root", "$child-prev"),
		}},
	}
}

func plainReplyEvent() *event.Event {
	return &event.Event{
		ID: "$reply", RoomID: relRoom, Sender: relCarol, Type: event.EventMessage, Timestamp: 1700000002000,
		Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType: event.MsgText, Body: "una cita",
			RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: "$root"}},
		}},
	}
}

func TestToItemTagsThreadRootAndReplyMeta(t *testing.T) {
	a := relationMetaAdapter(t)
	a.remember(core.Item{ID: itemID("work", relRoom, "$root"), From: core.Address{Name: "Beto"}})

	item := a.toItem(threadChildEvent())
	if want := itemID("work", relRoom, "$root"); item.Meta["thread_root"] != want {
		t.Errorf("thread_root = %q, want %q", item.Meta["thread_root"], want)
	}
	if item.Meta["thread_root_name"] != "Beto" {
		t.Errorf("thread_root_name = %q, want Beto", item.Meta["thread_root_name"])
	}
	// SetThread("$root", "$child-prev") carries a fallback reply, so it
	// must be tagged as well (both keys hold full item ids).
	if want := itemID("work", relRoom, "$child-prev"); item.Meta["reply_to"] != want {
		t.Errorf("reply_to = %q, want %q", item.Meta["reply_to"], want)
	}

	item = a.toItem(plainReplyEvent())
	if want := itemID("work", relRoom, "$root"); item.Meta["reply_to"] != want {
		t.Errorf("reply_to = %q, want %q", item.Meta["reply_to"], want)
	}
	if item.Meta["thread_root"] != "" {
		t.Errorf("thread_root = %q, want empty for a plain reply", item.Meta["thread_root"])
	}

	item = a.toItem(threadRootEvent())
	if item.Meta["thread_root"] != "" || item.Meta["reply_to"] != "" {
		t.Errorf("root message meta = %v, want no relations", item.Meta)
	}
}

func TestToItemHidesUnresolvedRelationNames(t *testing.T) {
	a := relationMetaAdapter(t)

	item := a.toItem(threadChildEvent())
	if item.Meta["thread_root"] == "" {
		t.Fatalf("thread_root missing: %v", item.Meta)
	}
	if item.Meta["thread_root_name"] != "" {
		t.Errorf("thread_root_name = %q, want empty (root not in cache)", item.Meta["thread_root_name"])
	}
}

func TestThreadRootOfResolvesTargetAndRoot(t *testing.T) {
	a := relationMetaAdapter(t)
	rootID := itemID("work", relRoom, "$root")
	childID := itemID("work", relRoom, "$child")

	if got := a.threadRootOf(childID); got != "" {
		t.Fatalf("unknown target resolved to %q", got)
	}

	// The target itself lives in a thread: meta carries its root.
	a.remember(core.Item{ID: childID, Meta: map[string]string{"thread_root": rootID}})
	if got := a.threadRootOf(childID); got != rootID {
		t.Fatalf("threaded target resolved to %q, want %q", got, rootID)
	}

	// The target IS a thread root: another cached item is threaded
	// under it. The scan is what lets replying to the first message
	// stay inside Element's thread.
	a.remember(core.Item{ID: itemID("work", relRoom, "$second"), Meta: map[string]string{"thread_root": rootID}})
	if got := a.threadRootOf(rootID); got != rootID {
		t.Fatalf("thread root resolved to %q, want %q", got, rootID)
	}

	if got := a.threadRootOf("mail:cl:1"); got != "" {
		t.Fatalf("non-matrix target resolved to %q", got)
	}
}

func TestSendAnswersInsideThread(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	a := newTestAdapter(t, srv, nil)
	ctx := context.Background()
	childID := itemID("work", relRoom, "$child")
	rootID := itemID("work", relRoom, "$root")

	a.remember(core.Item{ID: childID, Meta: map[string]string{"thread_root": rootID}})
	if _, err := a.Send(ctx, core.Outgoing{Thread: relRoom.String(), Body: "dentro del hilo", ReplyTo: childID}); err != nil {
		t.Fatalf("Send into thread: %v", err)
	}
	_, content := sentContent(t, state, 0)
	rel, _ := content["m.relates_to"].(map[string]any)
	if rel["rel_type"] != "m.thread" {
		t.Fatalf("rel_type = %v, want m.thread", rel["rel_type"])
	}
	if rel["event_id"] != "$root" {
		t.Fatalf("thread event_id = %v, want $root", rel["event_id"])
	}
	reply, _ := rel["m.in_reply_to"].(map[string]any)
	if reply["event_id"] != "$child" {
		t.Fatalf("fallback reply = %v, want $child", reply["event_id"])
	}

	// A plain reply keeps the classic relation, no rel_type.
	plainID := itemID("work", relRoom, "$reply")
	if _, err := a.Send(ctx, core.Outgoing{Thread: relRoom.String(), Body: "cita simple", ReplyTo: plainID}); err != nil {
		t.Fatalf("Send plain reply: %v", err)
	}
	_, content = sentContent(t, state, 1)
	rel, _ = content["m.relates_to"].(map[string]any)
	if rel["rel_type"] != nil {
		t.Fatalf("rel_type = %v, want none for a plain reply", rel["rel_type"])
	}
	reply, _ = rel["m.in_reply_to"].(map[string]any)
	if reply["event_id"] != "$reply" {
		t.Fatalf("in_reply_to = %v, want $reply", reply["event_id"])
	}
}
