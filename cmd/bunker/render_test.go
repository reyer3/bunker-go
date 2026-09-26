package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

func TestRenderFallsBackToStoreWhenDaemonIsDown(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUNKER_STATE_DIR", dir)
	t.Setenv("BUNKER_SOCKET", filepath.Join(dir, "nobody-listens.sock"))

	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Unread: true}
	if err := st.Upsert(context.Background(), item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	st.Close()

	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := cmdRender(context.Background(), []string{"--json"}, &stdout, &stderr)
	elapsed := time.Since(start)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if elapsed > renderBudget+100*time.Millisecond {
		t.Fatalf("render took %v, want it within its budget", elapsed)
	}

	var got struct {
		Segments []renderSegment `json:"segments"`
		DaemonUp bool            `json:"daemonUp"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", stdout.String(), err)
	}
	if got.DaemonUp {
		t.Fatal("expected daemonUp = false when the socket is unreachable")
	}
	found := false
	for _, seg := range got.Segments {
		if seg.Channel == core.ChannelMail {
			found = true
			if seg.Unread != 1 {
				t.Fatalf("mail segment = %+v, want unread 1", seg)
			}
		}
	}
	if !found {
		t.Fatalf("segments = %+v, want a mail segment", got.Segments)
	}
}

func TestRenderPrintsDeadMarkerWhenNothingIsReachable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUNKER_STATE_DIR", filepath.Join(dir, "does-not-exist-and-cannot-be-created"))
	t.Setenv("BUNKER_SOCKET", filepath.Join(dir, "nobody-listens.sock"))

	// Make the state dir path unwritable by pointing it at a file instead
	// of a directory, so store.Open cannot create the db either.
	blocker := filepath.Join(dir, "does-not-exist-and-cannot-be-created")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdRender(context.Background(), nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if stdout.String() != "bunker: dead\n" {
		t.Fatalf("stdout = %q, want dead marker", stdout.String())
	}
}

func TestRenderUsesLiveDaemonWhenReachable(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	item := core.Item{ID: "whatsapp:demo:1", Channel: core.ChannelWhatsApp, Account: "demo", Unread: true}
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
		if c, err := rpc.Dial(socket); err == nil {
			c.Close()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Setenv("BUNKER_SOCKET", socket)

	var stdout, stderr bytes.Buffer
	code := cmdRender(context.Background(), []string{"--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		DaemonUp bool `json:"daemonUp"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.DaemonUp {
		t.Fatal("expected daemonUp = true when the daemon answers")
	}
}
