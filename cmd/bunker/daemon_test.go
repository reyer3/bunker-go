package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
)

// daemonStartBudget bounds how long a test waits for the daemon. It is
// generous because the pure-Go SQLite store opens slowly under -race on a
// CI runner busy with every other package; polling keeps the usual case
// as fast as the daemon itself.
const daemonStartBudget = 15 * time.Second

// dialUntilReady polls until socket accepts a connection or the test's
// budget runs out, so tests never sleep a fixed guess.
func dialUntilReady(t *testing.T, socket string) *rpc.Client {
	t.Helper()
	deadline := time.Now().Add(daemonStartBudget)
	for time.Now().Before(deadline) {
		if c, err := rpc.Dial(socket); err == nil {
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("daemon never became reachable")
	return nil
}

func TestRunDaemonFakeSeedsDemoItemsAndServesRPC(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "bunker.sock")

	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runDaemon(ctx, dir, filepath.Join(dir, "avatars"), socket, true, &stdout, &stderr) }()

	client := dialUntilReady(t, socket)
	defer client.Close()

	// Each fake adapter seeds its item from its own Run goroutine, so the
	// socket accepting connections does not by itself mean every seed has
	// landed yet: poll Counts until all three demo items show up.
	var counts map[core.Channel]map[string]int
	deadline := time.Now().Add(daemonStartBudget)
	for time.Now().Before(deadline) {
		var err error
		counts, err = client.Counts(context.Background())
		if err != nil {
			t.Fatalf("Counts: %v", err)
		}
		if counts["mail"]["demo"] == 1 && counts["whatsapp"]["demo"] == 1 && counts["matrix"]["demo"] == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if counts["mail"]["demo"] != 1 || counts["whatsapp"]["demo"] != 1 || counts["matrix"]["demo"] != 1 {
		t.Fatalf("counts = %+v, want 1 unread demo item per channel", counts)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runDaemon returned error after cancel: %v", err)
		}
	case <-time.After(daemonStartBudget):
		t.Fatal("runDaemon did not shut down after ctx cancel")
	}
}

func TestRunDaemonWithoutConfigStartsWithEmptyRegistry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUNKER_CONFIG_DIR", filepath.Join(dir, "no-config-here"))
	socket := filepath.Join(dir, "bunker.sock")

	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runDaemon(ctx, dir, filepath.Join(dir, "avatars"), socket, false, &stdout, &stderr) }()

	client := dialUntilReady(t, socket)
	defer client.Close()

	counts, err := client.Counts(context.Background())
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if len(counts) != 0 {
		t.Fatalf("counts = %+v, want empty with no configured accounts", counts)
	}

	cancel()
	<-done
}
