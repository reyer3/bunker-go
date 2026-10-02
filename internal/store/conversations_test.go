package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func convItem(id string, ch core.Channel, account, thread string, unread bool, at time.Time) core.Item {
	return core.Item{
		ID: id, Channel: ch, Account: account, Thread: thread, ThreadName: "chat " + thread,
		From: core.Address{ID: "peer-" + thread, Name: "Peer " + thread}, Body: "body " + id,
		Unread: unread, Timestamp: at,
	}
}

func seedConversations(t *testing.T) (context.Context, func(core.ConversationFilter) []core.Conversation) {
	t.Helper()
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	items := []core.Item{
		// 1:1 chat "a": read, newest message at +10m.
		convItem("wa:1", core.ChannelWhatsApp, "me", "a", false, base),
		convItem("wa:2", core.ChannelWhatsApp, "me", "a", false, base.Add(10*time.Minute)),
		// Group "g": two unread, newest at +30m.
		convItem("wa:3", core.ChannelWhatsApp, "me", "g@g.us", true, base.Add(20*time.Minute)),
		convItem("wa:4", core.ChannelWhatsApp, "me", "g@g.us", true, base.Add(30*time.Minute)),
		convItem("wa:5", core.ChannelWhatsApp, "me", "g@g.us", false, base.Add(time.Minute)),
		// Same thread id on another account is another conversation.
		convItem("wa:6", core.ChannelWhatsApp, "alt", "a", true, base.Add(5*time.Minute)),
		convItem("mx:1", core.ChannelMatrix, "mx", "!room", true, base.Add(15*time.Minute)),
		convItem("mail:1", core.ChannelMail, "cl", "t1", true, base.Add(40*time.Minute)),
		// Threadless items are one conversation each.
		convItem("mail:2", core.ChannelMail, "cl", "", true, base.Add(50*time.Minute)),
		convItem("mail:3", core.ChannelMail, "cl", "", false, base.Add(45*time.Minute)),
	}
	for _, it := range items {
		if err := s.Upsert(ctx, it); err != nil {
			t.Fatalf("Upsert %s: %v", it.ID, err)
		}
	}
	return ctx, func(f core.ConversationFilter) []core.Conversation {
		t.Helper()
		got, err := s.Conversations(ctx, f)
		if err != nil {
			t.Fatalf("Conversations: %v", err)
		}
		return got
	}
}

func convSummary(cs []core.Conversation) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = fmt.Sprintf("%s/%d", c.Last.ID, c.Unread)
	}
	return out
}

func assertConvs(t *testing.T, got []core.Conversation, want ...string) {
	t.Helper()
	have := convSummary(got)
	if fmt.Sprint(have) != fmt.Sprint(want) {
		t.Fatalf("conversations = %v, want %v", have, want)
	}
}

func TestConversationsOrderUnreadAndKinds(t *testing.T) {
	_, conv := seedConversations(t)
	got := conv(core.ConversationFilter{Channel: core.ChannelWhatsApp})
	// group (newest +30m, 2 unread), 1:1 "a" (+10m, read, listed anyway),
	// the other account's "a" (+5m, 1 unread).
	assertConvs(t, got, "wa:4/2", "wa:2/0", "wa:6/1")
	if got[1].Last.Unread {
		t.Fatalf("read conversation reports an unread last item")
	}
}

func TestConversationsLimitKeepsNewest(t *testing.T) {
	_, conv := seedConversations(t)
	assertConvs(t, conv(core.ConversationFilter{Channel: core.ChannelWhatsApp, Limit: 2}), "wa:4/2", "wa:2/0")
}

func TestConversationsChannelAndAccountFilters(t *testing.T) {
	_, conv := seedConversations(t)
	assertConvs(t, conv(core.ConversationFilter{Channel: core.ChannelMatrix}), "mx:1/1")
	assertConvs(t, conv(core.ConversationFilter{Channel: core.ChannelWhatsApp, Account: "alt"}), "wa:6/1")
	// No channel: every channel, newest first across them.
	assertConvs(t, conv(core.ConversationFilter{}),
		"mail:2/1", "mail:3/0", "mail:1/1", "wa:4/2", "mx:1/1", "wa:2/0", "wa:6/1")
}

func TestConversationsEmptyStore(t *testing.T) {
	s := openTestStore(t)
	got, err := s.Conversations(context.Background(), core.ConversationFilter{})
	if err != nil || len(got) != 0 {
		t.Fatalf("Conversations = %v, %v; want empty", got, err)
	}
}

func TestConversationsTieBreaksOnID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"wa:x", "wa:y"} {
		if err := s.Upsert(ctx, convItem(id, core.ChannelWhatsApp, "me", "t", false, at)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Conversations(ctx, core.ConversationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	assertConvs(t, got, "wa:y/0")
}
