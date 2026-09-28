package whatsapp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types/events"
)

func uint32Ptr(v uint32) *uint32 { return &v }

// syntheticConversation builds a waHistorySync.Conversation carrying
// len(bodies) plain-text messages, oldest first (as WhatsApp's own
// history sync orders them), for handleHistorySync tests.
func syntheticConversation(id string, unreadCount int, archived bool, bodies ...string) *waHistorySync.Conversation {
	msgs := make([]*waHistorySync.HistorySyncMsg, len(bodies))
	for i, body := range bodies {
		msgs[i] = &waHistorySync.HistorySyncMsg{
			Message: &waWeb.WebMessageInfo{
				Key:              &waCommon.MessageKey{ID: strPtr(fmt.Sprintf("H%d", i))},
				Message:          &waE2E.Message{Conversation: strPtr(body)},
				MessageTimestamp: uint64Ptrx(time.Now().Unix()),
			},
		}
	}
	return &waHistorySync.Conversation{
		ID:          strPtr(id),
		UnreadCount: uint32Ptr(uint32(unreadCount)),
		Archived:    boolPtr(archived),
		Messages:    msgs,
	}
}

func uint64Ptrx(v int64) *uint64 { u := uint64(v); return &u }

func emitHistorySync(cli *fakeWAClient, convs ...*waHistorySync.Conversation) {
	cli.emit(&events.HistorySync{
		Data: &waHistorySync.HistorySync{
			Conversations: convs,
		},
	})
}

func TestHistorySyncSkipsConversationWithNoUnread(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	emitHistorySync(cli, syntheticConversation("1234@s.whatsapp.net", 0, false, "a", "b", "c"))

	settle(t)
	if got := len(sink.items()); got != 0 {
		t.Fatalf("items imported = %d, want 0 for unreadCount=0", got)
	}
}

func TestHistorySyncImportsAllMessagesWhenUnreadWithinLimit(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)
	a.SetHistoryLimit(5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	emitHistorySync(cli, syntheticConversation("1234@s.whatsapp.net", 2, false, "a", "b"))
	waitFor(t, func() bool { return len(sink.items()) == 2 })

	for _, item := range sink.items() {
		if !item.Unread {
			t.Errorf("item %q Unread = false, want true (unreadCount=2 covers both)", item.ID)
		}
	}
}

func TestHistorySyncCapsToLastNMessagesAndMarksOnlyUnreadCountAsUnread(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)
	a.SetHistoryLimit(3)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	// 6 messages total, unreadCount=2, limit=3: only the last 3 ("d","e","f")
	// are imported, and only the newest 2 of those ("e","f") are unread.
	emitHistorySync(cli, syntheticConversation("1234@s.whatsapp.net", 2, false, "a", "b", "c", "d", "e", "f"))
	waitFor(t, func() bool { return len(sink.items()) == 3 })

	byBody := map[string]bool{}
	for _, item := range sink.items() {
		byBody[item.Body] = item.Unread
	}
	if _, ok := byBody["a"]; ok {
		t.Fatalf("full history should never be imported, got %v", byBody)
	}
	want := map[string]bool{"d": false, "e": true, "f": true}
	for body, wantUnread := range want {
		got, ok := byBody[body]
		if !ok {
			t.Fatalf("message %q was not imported, got %v", body, byBody)
		}
		if got != wantUnread {
			t.Errorf("message %q Unread = %v, want %v", body, got, wantUnread)
		}
	}
}

func TestHistorySyncSkipsArchivedConversation(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	emitHistorySync(cli, syntheticConversation("1234@s.whatsapp.net", 3, true, "a", "b", "c"))

	settle(t)
	if got := len(sink.items()); got != 0 {
		t.Fatalf("items imported = %d, want 0 for an archived conversation", got)
	}
}

func TestHistorySyncSkipsStatusBroadcast(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	emitHistorySync(cli, syntheticConversation("status@broadcast", 5, false, "a", "b", "c"))

	settle(t)
	if got := len(sink.items()); got != 0 {
		t.Fatalf("items imported = %d, want 0 for status@broadcast", got)
	}
}

// settle gives an async handler a moment to (not) act, for asserting a
// negative outcome without a fixed race-prone sleep elsewhere in the
// call path to check against.
func settle(t *testing.T) {
	t.Helper()
	time.Sleep(150 * time.Millisecond)
}

func TestHistorySyncSkipsNewsletterConversation(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	emitHistorySync(cli, syntheticConversation("120363000000000001@newsletter", 3, false, "a", "b", "c"))

	settle(t)
	if got := len(sink.items()); got != 0 {
		t.Fatalf("items imported = %d, want 0 for a newsletter conversation", got)
	}
}
