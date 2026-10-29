package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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

// TestP2PStatusJSONHasNoRendezvousKey drives `bunker p2p status --json`
// through the CLI entry point with the in-process fake backend and asserts the
// removed DHT rendezvous no longer appears in the wire format.
func TestP2PStatusJSONHasNoRendezvousKey(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), newFakeBackend(),
		[]string{"p2p", "status", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("p2p status --json exit = %d, stderr = %s", code, stderr.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout.String(), err)
	}
	if _, ok := raw["rendezvous_peers"]; ok {
		t.Fatalf("p2p status --json still emits rendezvous_peers: %s", stdout.String())
	}
}

// TestP2PStatusRejectsSecretFlag proves the removed --secreto flag fails
// loudly as an unknown flag instead of being silently ignored.
func TestP2PStatusRejectsSecretFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), newFakeBackend(),
		[]string{"p2p", "status", "--secreto", "algo"}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatalf("p2p status --secreto devolvió exit 0: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown flag") {
		t.Fatalf("stderr = %q, want unknown flag", stderr.String())
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
