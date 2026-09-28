package rpc_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/channel/fake"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

// backfillSearchAdapter wraps fake.Adapter with core.Backfiller and
// core.Searcher so these RPC dispatch tests (this file and
// search_test.go) can exercise MethodBackfill/MethodSearch without a
// real IMAP server; the mail package's own backfill_test.go/
// search_test.go already cover the real IMAP-driven behavior these
// ports wrap.
type backfillSearchAdapter struct {
	*fake.Adapter

	backfillResult core.BackfillResult
	backfillErr    error
	gotFolder      string
	gotSince       time.Time
	gotDryRun      bool

	searchResult []core.Item
	searchErr    error
	gotCriteria  core.SearchCriteria
}

func (a *backfillSearchAdapter) Backfill(_ context.Context, _ core.Store, folder string, since time.Time, dryRun bool) (core.BackfillResult, error) {
	a.gotFolder, a.gotSince, a.gotDryRun = folder, since, dryRun
	return a.backfillResult, a.backfillErr
}

func (a *backfillSearchAdapter) Search(_ context.Context, _ core.Store, criteria core.SearchCriteria) ([]core.Item, error) {
	a.gotCriteria = criteria
	return a.searchResult, a.searchErr
}

func startBackfillSearchTestServer(t *testing.T) (*rpc.Client, *backfillSearchAdapter) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	reg := core.NewRegistry()
	adapter := &backfillSearchAdapter{Adapter: fake.New(core.ChannelMail, "cl")}
	reg.Register(adapter)
	svc := core.NewService(st, reg)

	socket := filepath.Join(dir, "bunker.sock")
	srv := rpc.NewServer(svc)
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, socket) }()
	t.Cleanup(func() {
		cancel()
		<-serveErr
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := rpc.Dial(socket); err == nil {
			t.Cleanup(func() { c.Close() })
			return c, adapter
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("server never became reachable")
	return nil, nil
}

func TestClientBackfillOverSocket(t *testing.T) {
	client, adapter := startBackfillSearchTestServer(t)
	adapter.backfillResult = core.BackfillResult{Count: 2, FirstID: "mail:cl:1.1", LastID: "mail:cl:1.2"}

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	result, err := client.Backfill(context.Background(), core.ChannelMail, "cl", "INBOX", since, false)
	if err != nil {
		t.Fatalf("Backfill error = %v", err)
	}
	if result.Count != 2 || result.FirstID != "mail:cl:1.1" || result.LastID != "mail:cl:1.2" {
		t.Errorf("result = %+v, want the adapter's BackfillResult verbatim", result)
	}
	if adapter.gotFolder != "INBOX" || !adapter.gotSince.Equal(since) || adapter.gotDryRun {
		t.Errorf("adapter got folder=%q since=%v dryRun=%v", adapter.gotFolder, adapter.gotSince, adapter.gotDryRun)
	}
}

func TestClientBackfillUnsupportedReturnsErrUnsupported(t *testing.T) {
	client, _, _ := startTestServer(t) // fake.Adapter alone: no Backfiller
	_, err := client.Backfill(context.Background(), core.ChannelMail, "cl", "INBOX", time.Now(), false)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("Backfill err = %v, want ErrUnsupported", err)
	}
}
