package rpc_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

// startListPageServer serves a real store seeded with items over a
// socket, with no adapters: ListPage only ever reads the store.
func startListPageServer(t *testing.T, items ...core.Item) *rpc.Client {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	for _, it := range items {
		if err := st.Upsert(context.Background(), it); err != nil {
			t.Fatalf("Upsert %s: %v", it.ID, err)
		}
	}
	svc := core.NewService(st, core.NewRegistry())
	socket := filepath.Join(dir, "bunker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- rpc.NewServer(svc).Serve(ctx, socket) }()
	t.Cleanup(func() { cancel(); <-serveErr })

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := rpc.Dial(socket); err == nil {
			t.Cleanup(func() { c.Close() })
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("server never became reachable")
	return nil
}

// TestClientListPageOverSocket drives the query language and cursor
// pagination through the real socket, server and store: the daemon
// parses the query and the cursor round-trips opaquely.
func TestClientListPageOverSocket(t *testing.T) {
	ts := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	items := []core.Item{{ID: "mail:cl:other", Channel: core.ChannelMail, Account: "cl", Subject: "hola", Unread: true, Timestamp: ts}}
	// Three matches sharing one timestamp, so paging needs the id tiebreak.
	for i := range 3 {
		items = append(items, core.Item{
			ID: fmt.Sprintf("mail:cl:p%d", i), Channel: core.ChannelMail, Account: "cl",
			From: core.Address{ID: "ana@example.com", Name: "Ana"}, Subject: "factura", Timestamp: ts,
		})
	}
	client := startListPageServer(t, items...)
	ctx := context.Background()

	const q = "from:ana subject:factura"
	page, err := client.ListPage(ctx, core.Filter{Limit: 2}, q)
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page 1 = %+v", page)
	}
	page2, err := client.ListPage(ctx, core.Filter{Limit: 2, Cursor: page.NextCursor}, q)
	if err != nil {
		t.Fatalf("ListPage page 2: %v", err)
	}
	if len(page2.Items) != 1 || page2.NextCursor != "" {
		t.Fatalf("page 2 = %+v", page2)
	}
	var got []string
	for _, it := range append(page.Items, page2.Items...) {
		got = append(got, it.ID)
	}
	if strings.Join(got, ",") != "mail:cl:p2,mail:cl:p1,mail:cl:p0" {
		t.Fatalf("pages = %v", got)
	}

	unread, err := client.ListPage(ctx, core.Filter{Channel: core.ChannelMail}, "is:unread")
	if err != nil || len(unread.Items) != 1 || unread.Items[0].ID != "mail:cl:other" {
		t.Fatalf("is:unread = %+v, %v", unread, err)
	}

	if _, err := client.ListPage(ctx, core.Filter{}, "foo:bar"); err == nil || !strings.Contains(err.Error(), `"foo:"`) {
		t.Fatalf("an unknown operator must come back as an error naming it, got %v", err)
	}
	if _, err := client.ListPage(ctx, core.Filter{Cursor: "garbage!"}, ""); err == nil {
		t.Fatal("a malformed cursor must be an error")
	}
}
