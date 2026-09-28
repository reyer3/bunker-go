package main

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// fakeRetrierAdapter is a minimal core.Adapter that also implements
// core.Retrier, so startAdapters's wiring can be proven without a real
// Matrix crypto machine: RetryUndecryptable and Run each record that they
// ran, and Run blocks on ctx like a real adapter's sync loop would.
type fakeRetrierAdapter struct {
	channel core.Channel
	account string

	mu         sync.Mutex
	retried    bool
	runStarted chan struct{}
}

func (f *fakeRetrierAdapter) Channel() core.Channel { return f.channel }
func (f *fakeRetrierAdapter) Account() string       { return f.account }

func (f *fakeRetrierAdapter) RetryUndecryptable(_ context.Context, _ core.Store) error {
	f.mu.Lock()
	f.retried = true
	f.mu.Unlock()
	return nil
}

func (f *fakeRetrierAdapter) Run(ctx context.Context, _ core.Sink) error {
	close(f.runStarted)
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeRetrierAdapter) wasRetried() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.retried
}

var (
	_ core.Adapter = (*fakeRetrierAdapter)(nil)
	_ core.Retrier = (*fakeRetrierAdapter)(nil)
)

// TestStartAdaptersCallsRetryUndecryptableBeforeRun proves the daemon
// wiring T8 asks for: an adapter that implements core.Retrier gets one
// RetryUndecryptable call before its Run loop starts, using the full
// core.Store (not just the Sink an Adapter's Run gets).
func TestStartAdaptersCallsRetryUndecryptableBeforeRun(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	reg := core.NewRegistry()
	fake := &fakeRetrierAdapter{channel: core.ChannelMatrix, account: "work", runStarted: make(chan struct{})}
	reg.Register(fake)

	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	wg := startAdapters(ctx, reg, st, &stdout, &stderr, nil)

	select {
	case <-fake.runStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to start")
	}

	if !fake.wasRetried() {
		t.Error("RetryUndecryptable was not called before Run started")
	}

	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("startAdapters' WaitGroup did not finish after ctx cancel")
	}
}
