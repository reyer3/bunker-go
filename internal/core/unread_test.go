package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// seenAdapter is a core.Organizer whose Seen=false support is set per test.
type seenAdapter struct {
	callerAdapter
	unreadErr error
	ops       []core.OrganizeOp
}

func (a *seenAdapter) Organize(_ context.Context, _ string, op core.OrganizeOp) error {
	a.ops = append(a.ops, op)
	if op.Seen != nil && !*op.Seen {
		return a.unreadErr
	}
	return nil
}

func TestMarkUnread(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Unread: false}
	for _, c := range []struct {
		name      string
		adapter   *seenAdapter
		wantLocal bool
		wantErr   bool
	}{
		{"server supports it", &seenAdapter{callerAdapter: callerAdapter{channel: core.ChannelMail, account: "cl"}}, false, false},
		{"channel cannot", &seenAdapter{callerAdapter: callerAdapter{channel: core.ChannelMail, account: "cl"}, unreadErr: core.ErrUnsupported}, true, false},
		{"server fails", &seenAdapter{callerAdapter: callerAdapter{channel: core.ChannelMail, account: "cl"}, unreadErr: errors.New("imap down")}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := newMemStore(item)
			reg := core.NewRegistry()
			reg.Register(c.adapter)
			local, err := core.NewService(store, reg).MarkUnread(context.Background(), item.ID)
			if (err != nil) != c.wantErr || local != c.wantLocal {
				t.Fatalf("MarkUnread = %v, %v; want local=%v err=%v", local, err, c.wantLocal, c.wantErr)
			}
			got, _ := store.Get(context.Background(), item.ID)
			if !c.wantErr && !got.Unread {
				t.Fatal("the item should be unread in the store")
			}
			if c.wantErr && got.Unread {
				t.Fatal("a failed server call must not flip the store")
			}
		})
	}
}
