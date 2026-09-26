package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

// TestRunDialsRealDaemonOverSocket proves run()'s own wiring (env-resolved
// socket path -> rpc.Dial -> runWithBackend) against a real daemon, not
// just the fakeBackend unit tests above.
func TestRunDialsRealDaemonOverSocket(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Subject: "hi"}
	if err := st.Upsert(context.Background(), item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	reg := core.NewRegistry()
	svc := core.NewService(st, reg)
	srv := rpc.NewServer(svc)
	socket := filepath.Join(dir, "bunker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, socket) }()
	t.Cleanup(func() { cancel(); <-serveErr })

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, dialErr := rpc.Dial(socket); dialErr == nil {
			c.Close()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Setenv("BUNKER_SOCKET", socket)

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	code := run([]string{"list", "--channel", "mail", "--json"}, os.Stdin, stdoutW, os.Stderr)
	stdoutW.Close()
	if code != 0 {
		t.Fatalf("run() exit code = %d", code)
	}

	buf := make([]byte, 4096)
	n, _ := stdoutR.Read(buf)
	out := string(buf[:n])
	if !strings.Contains(out, "mail:cl:1") {
		t.Fatalf("run() stdout = %q, want it to contain mail:cl:1", out)
	}
}
