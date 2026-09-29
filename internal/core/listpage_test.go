package core_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// pagingStore is memStore plus a recording core.PageLister, so the test
// sees exactly what Service.ListPage hands the store.
type pagingStore struct {
	*memStore
	got []core.Filter
}

func (p *pagingStore) ListPage(ctx context.Context, filter core.Filter) (core.Page, error) {
	p.got = append(p.got, filter)
	return core.Page{Items: []core.Item{}, NextCursor: "next"}, nil
}

func TestServiceListPageParsesWithItsClock(t *testing.T) {
	st := &pagingStore{memStore: newMemStore()}
	svc := core.NewService(st, core.NewRegistry())
	svc.SetQueryClock(func() time.Time { return queryNow })

	unread := true
	page, err := svc.ListPage(context.Background(), core.Filter{Channel: core.ChannelMail, Unread: &unread, Cursor: "c1", Limit: 5}, "from:ana after:7d")
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != "next" || len(st.got) != 1 {
		t.Fatalf("page = %+v, calls = %d", page, len(st.got))
	}
	f := st.got[0]
	if f.Channel != core.ChannelMail || f.Unread == nil || !*f.Unread || f.Cursor != "c1" || f.Limit != 5 {
		t.Fatalf("the plain filter fields must pass through: %+v", f)
	}
	if f.Match == nil || len(f.Match.Terms) != 2 || !f.Match.Terms[1].Time.Equal(queryNow.AddDate(0, 0, -7)) {
		t.Fatalf("Match = %+v", f.Match)
	}

	// An empty query adds no Match at all.
	if _, err := svc.ListPage(context.Background(), core.Filter{}, ""); err != nil {
		t.Fatal(err)
	}
	if st.got[1].Match != nil {
		t.Fatalf("empty query set Match = %+v", st.got[1].Match)
	}
}

func TestServiceListPageRejectsABadQueryBeforeTheStore(t *testing.T) {
	st := &pagingStore{memStore: newMemStore()}
	svc := core.NewService(st, core.NewRegistry())
	if _, err := svc.ListPage(context.Background(), core.Filter{}, "foo:bar"); !errors.Is(err, core.ErrInvalidQuery) {
		t.Fatalf("err = %v, want ErrInvalidQuery", err)
	}
	if len(st.got) != 0 {
		t.Fatal("a bad query must never reach the store")
	}
}

func TestServiceListPageWithoutPagingStoreIsUnsupported(t *testing.T) {
	svc := core.NewService(newMemStore(), core.NewRegistry())
	if _, err := svc.ListPage(context.Background(), core.Filter{}, ""); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
