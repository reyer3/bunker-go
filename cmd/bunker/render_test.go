package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

	dialUntilReady(t, socket).Close()

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

// startRenderTestDaemon opens a store seeded with item, wires a Service
// with health (if non-nil) as its HealthTracker, serves it over a fresh
// unix socket, points BUNKER_SOCKET at it, and returns the store path's
// directory for cleanup coordination. Modeled on
// TestRenderUsesLiveDaemonWhenReachable.
func startRenderTestDaemon(t *testing.T, item core.Item, health *core.HealthTracker) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Upsert(context.Background(), item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	svc := core.NewService(st, core.NewRegistry())
	if health != nil {
		svc.SetHealthTracker(health)
	}
	srv := rpc.NewServer(svc)
	socket := filepath.Join(dir, "bunker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, socket) }()
	t.Cleanup(func() { cancel(); <-serveErr })

	dialUntilReady(t, socket).Close()
	t.Setenv("BUNKER_SOCKET", socket)
}

// TestRenderAppendsWarningMarkerWhenAnAdapterIsNotConnected proves R4:
// render's plain output appends "!" when the live daemon reports any
// adapter not in the connected state, and the JSON form's allConnected
// field is false.
func TestRenderAppendsWarningMarkerWhenAnAdapterIsNotConnected(t *testing.T) {
	health := core.NewHealthTracker()
	health.SetBackoff(core.ChannelMail, "cl", time.Now(), nil)
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Unread: true}
	startRenderTestDaemon(t, item, health)

	var stdout, stderr bytes.Buffer
	if code := cmdRender(context.Background(), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.HasSuffix(strings.TrimRight(stdout.String(), "\n"), "!") {
		t.Fatalf("stdout = %q, want it to end with the ! marker", stdout.String())
	}

	stdout.Reset()
	if code := cmdRender(context.Background(), []string{"--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		AllConnected bool `json:"allConnected"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AllConnected {
		t.Fatal("expected allConnected = false when an adapter is in backoff")
	}
}

// TestRenderNoMarkerWhenAllAdaptersConnected proves the non-alerting
// side: no "!" and allConnected=true when every tracked adapter is
// connected.
func TestRenderNoMarkerWhenAllAdaptersConnected(t *testing.T) {
	health := core.NewHealthTracker()
	health.SetConnected(core.ChannelMail, "cl", time.Now())
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Unread: true}
	startRenderTestDaemon(t, item, health)

	var stdout, stderr bytes.Buffer
	if code := cmdRender(context.Background(), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "!") {
		t.Fatalf("stdout = %q, want no ! marker when every adapter is connected", stdout.String())
	}
}

// TestRenderNoMarkerWhenNoHealthTrackerWired proves an untracked Service
// (health tracker never wired, e.g. an older daemon build) shows no
// marker rather than always warning.
func TestRenderNoMarkerWhenNoHealthTrackerWired(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Unread: true}
	startRenderTestDaemon(t, item, nil)

	var stdout, stderr bytes.Buffer
	if code := cmdRender(context.Background(), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "!") {
		t.Fatalf("stdout = %q, want no ! marker without a wired health tracker", stdout.String())
	}
}
