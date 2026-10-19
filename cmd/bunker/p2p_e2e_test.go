package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestEndToEndP2PStatusAgainstFakeDaemon starts the real daemon with
// --p2p-listen, then drives `bunker p2p status --json` through the CLI entry
// point. It proves the RPC path end to end: the node lives in the daemon, the
// CLI only talks over the socket.
func TestEndToEndP2PStatusAgainstFakeDaemon(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "bunker.sock")
	t.Setenv("BUNKER_SOCKET", socket)
	t.Setenv("BUNKER_STATE_DIR", dir)

	ctx, cancel := context.WithCancel(context.Background())
	daemonDone := make(chan error, 1)
	daemonOut := &bytes.Buffer{}
	daemonErr := &bytes.Buffer{}
	go func() {
		daemonDone <- runDaemon(ctx, dir, filepath.Join(dir, "avatars"), socket, true,
			"127.0.0.1:0", "/ip4/127.0.0.1/tcp/0", daemonOut, daemonErr)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-daemonDone; err != nil {
			t.Errorf("runDaemon: %v", err)
		}
	})

	dialUntilReady(t, socket).Close()

	code, out := runCLI(t, []string{"p2p", "status", "--json"})
	if code != 0 {
		t.Fatalf("p2p status --json exit code = %d: %s%s", code, out, daemonErr.String())
	}

	var status struct {
		PeerID string   `json:"id"`
		Addr   string   `json:"addr"`
		Peers  []string `json:"peers"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if status.PeerID == "" {
		t.Fatalf("peer id vacío: %s", out)
	}
	if status.Addr == "" {
		t.Fatalf("addr vacía: %s", out)
	}
}

// TestEndToEndP2PStatusWithoutNodeFails proves the daemon without
// --p2p-listen reports ErrUnsupported instead of an empty status.
func TestEndToEndP2PStatusWithoutNodeFails(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "bunker.sock")
	t.Setenv("BUNKER_SOCKET", socket)
	t.Setenv("BUNKER_STATE_DIR", dir)

	ctx, cancel := context.WithCancel(context.Background())
	daemonDone := make(chan error, 1)
	daemonOut := &bytes.Buffer{}
	daemonErr := &bytes.Buffer{}
	go func() {
		daemonDone <- runDaemon(ctx, dir, filepath.Join(dir, "avatars"), socket, true,
			"127.0.0.1:0", "", daemonOut, daemonErr)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-daemonDone; err != nil {
			t.Errorf("runDaemon: %v", err)
		}
	})

	dialUntilReady(t, socket).Close()

	code, out := runCLI(t, []string{"p2p", "status", "--json"})
	if code == 0 {
		t.Fatalf("p2p status sin nodo devolvió exit 0: %s", out)
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if resp.Error == "" {
		t.Fatalf("error vacío: %s", out)
	}
}
