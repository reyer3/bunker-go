package rpc_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/rpc"
)

// TestClientCallRespectsContextDeadline proves the render command's "never
// hang tmux" budget: a Client call against a server that accepts the
// connection but never replies must return promptly once ctx's deadline
// passes, instead of blocking forever.
func TestClientCallRespectsContextDeadline(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "hanging.sock")

	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		close(accepted)
		<-time.After(2 * time.Second) // never responds within the test's budget
		conn.Close()
	}()

	client, err := rpc.Dial(socket)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = client.Counts(ctx)
	elapsed := time.Since(start)

	<-accepted
	if err == nil {
		t.Fatal("expected an error once the deadline passed")
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("Counts took %v, want it to respect the ~100ms context deadline", elapsed)
	}
}
