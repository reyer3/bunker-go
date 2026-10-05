package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

var awaitBase = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func ago(days int, extra time.Duration) time.Time {
	return awaitBase.Add(-time.Duration(days)*24*time.Hour + extra)
}

// awaitItem is a chat message; mine says who sent it.
func awaitItem(id string, ch core.Channel, account, thread, from string, mine bool, at time.Time) core.Item {
	return core.Item{ID: id, Channel: ch, Account: account, Thread: thread,
		From: core.Address{ID: from, Name: "Name " + from}, FromMe: mine, Body: "body " + id, Timestamp: at}
}

func seedAwaiting(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	s := openTestStore(t)
	ctx := context.Background()
	const me = "51900000009:3@s.whatsapp.net"
	ana := awaitItem("wa:ana:1", core.ChannelWhatsApp, "personal", "ana@s.whatsapp.net", "ana@s.whatsapp.net", false, ago(6, 0))
	ana.ThreadName = "Ana Ejemplo"
	deleted := awaitItem("wa:del:1", core.ChannelWhatsApp, "personal", "del@s.whatsapp.net", me, true, ago(4, 0))
	deleted.Deleted = true
	mail := awaitItem("mail:cl:1", core.ChannelMail, "cl", "t1", "me@example.com", true, ago(9, 0))
	mail.To = []core.Address{{ID: "ventas@example.com", Name: "Proveedor"}}
	items := []core.Item{
		// Ana answered six days ago, I wrote last five days ago: awaiting.
		// My message carries no chat name: the thread's newest one is used.
		ana,
		awaitItem("wa:ana:2", core.ChannelWhatsApp, "personal", "ana@s.whatsapp.net", me, true, ago(5, 0)),
		// I wrote yesterday: not old enough yet.
		awaitItem("wa:beto:1", core.ChannelWhatsApp, "personal", "beto@s.whatsapp.net", me, true, ago(1, 0)),
		// Carla answered me: nothing awaited.
		awaitItem("wa:carla:1", core.ChannelWhatsApp, "personal", "carla@s.whatsapp.net", me, true, ago(5, 0)),
		awaitItem("wa:carla:2", core.ChannelWhatsApp, "personal", "carla@s.whatsapp.net", "carla@s.whatsapp.net", false, ago(4, 0)),
		// A group where I wrote last: only with Groups.
		awaitItem("wa:grp:1", core.ChannelWhatsApp, "personal", "120363000000000001@g.us", me, true, ago(4, time.Minute)),
		// My own chat ("message yourself"): never.
		awaitItem("wa:self:1", core.ChannelWhatsApp, "personal", "51900000009@s.whatsapp.net", me, true, ago(4, 0)),
		// My newest message was deleted: never.
		deleted,
		// Two messages at the same instant: the highest id is the newest.
		awaitItem("wa:tie:a", core.ChannelWhatsApp, "personal", "tie@s.whatsapp.net", me, true, ago(4, 0)),
		awaitItem("wa:tie:b", core.ChannelWhatsApp, "personal", "tie@s.whatsapp.net", "tie@s.whatsapp.net", false, ago(4, 0)),
		awaitItem("wa:tie2:a", core.ChannelWhatsApp, "personal", "tie2@s.whatsapp.net", "tie2@s.whatsapp.net", false, ago(4, 0)),
		awaitItem("wa:tie2:b", core.ChannelWhatsApp, "personal", "tie2@s.whatsapp.net", me, true, ago(4, 0)),
		// A threadless mail of mine is no conversation.
		awaitItem("mail:cl:solo", core.ChannelMail, "cl", "", "me@example.com", true, ago(9, 0)),
		mail,
		// A Matrix room with one other person is a direct chat...
		awaitItem("mx:dm:1", core.ChannelMatrix, "mx", "!dm:example.org", "@luis:example.org", false, ago(8, 0)),
		awaitItem("mx:dm:2", core.ChannelMatrix, "mx", "!dm:example.org", "@me:example.org", true, ago(7, 0)),
		// ...one with two others is a group.
		awaitItem("mx:room:1", core.ChannelMatrix, "mx", "!room:example.org", "@luis:example.org", false, ago(8, 0)),
		awaitItem("mx:room:2", core.ChannelMatrix, "mx", "!room:example.org", "@eva:example.org", false, ago(8, time.Hour)),
		awaitItem("mx:room:3", core.ChannelMatrix, "mx", "!room:example.org", "@me:example.org", true, ago(6, 0)),
	}
	for _, it := range items {
		if err := s.Upsert(ctx, it); err != nil {
			t.Fatalf("Upsert %s: %v", it.ID, err)
		}
	}
	return s, ctx
}

func awaitIDs(t *testing.T, s *store.Store, ctx context.Context, q core.AwaitingQuery) []string {
	t.Helper()
	if q.Before.IsZero() {
		q.Before = ago(3, 0)
	}
	if q.Limit == 0 {
		q.Limit = 50
	}
	got, err := s.AwaitingReply(ctx, q)
	if err != nil {
		t.Fatalf("AwaitingReply: %v", err)
	}
	ids := make([]string, len(got))
	for i, it := range got {
		ids[i] = it.ID
	}
	return ids
}

func TestAwaitingReplyRules(t *testing.T) {
	s, ctx := seedAwaiting(t)
	for _, tc := range []struct {
		name string
		q    core.AwaitingQuery
		want []string
	}{
		{"default", core.AwaitingQuery{}, []string{"wa:tie2:b", "wa:ana:2", "mx:dm:2", "mail:cl:1"}},
		{"with groups", core.AwaitingQuery{Groups: true},
			[]string{"wa:grp:1", "wa:tie2:b", "wa:ana:2", "mx:room:3", "mx:dm:2", "mail:cl:1"}},
		{"one channel", core.AwaitingQuery{Channel: core.ChannelMail}, []string{"mail:cl:1"}},
		{"one account", core.AwaitingQuery{Account: "mx", Groups: true}, []string{"mx:room:3", "mx:dm:2"}},
		{"limit", core.AwaitingQuery{Limit: 2}, []string{"wa:tie2:b", "wa:ana:2"}},
		{"older cutoff", core.AwaitingQuery{Before: ago(6, 0)}, []string{"mx:dm:2", "mail:cl:1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := awaitIDs(t, s, ctx, tc.q); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("ids = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAwaitingReplyFillsThreadName(t *testing.T) {
	s, ctx := seedAwaiting(t)
	got, err := s.AwaitingReply(ctx, core.AwaitingQuery{Before: ago(3, 0), Channel: core.ChannelWhatsApp, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range got {
		if it.ID == "wa:ana:2" {
			if it.ThreadName != "Ana Ejemplo" || !it.FromMe || it.Body != "body wa:ana:2" || !it.Timestamp.Equal(ago(5, 0)) {
				t.Errorf("item = %+v", it)
			}
			return
		}
	}
	t.Fatalf("wa:ana:2 missing from %+v", got)
}
