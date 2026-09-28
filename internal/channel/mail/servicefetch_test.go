package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"

	"github.com/emersion/go-imap/v2"
)

// TestServiceFetchServerFallbackThenDownload is mail-history H1's
// acceptance scenario end to end: a real (in-process sqlite) store that
// never synced a message, a core.Service wired to the mail adapter
// through core.Registry the way the daemon wires it, a Fetch call on the
// id the store lacks, and a Download of that item's attachment
// afterward — all without ever marking the message \Seen on the server.
func TestServiceFetchServerFallbackThenDownload(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", multipartMessage)

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	// Learn the real "mail:cl:<uidvalidity>.<uid>" id the way Run would,
	// through a throwaway sink — never the real store, since this test's
	// whole point is a store that never synced this message.
	ctx, cancel := context.WithCancel(context.Background())
	seedSink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, seedSink) }()
	seed := waitForUpsert(t, seedSink, 5*time.Second)
	cancel()
	<-done

	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if _, err := st.Get(context.Background(), seed.ID); err == nil {
		t.Fatalf("test setup: store already has %s, want it absent", seed.ID)
	}

	reg := core.NewRegistry()
	reg.Register(adapter)
	svc := core.NewService(st, reg)

	item, err := svc.Fetch(context.Background(), seed.ID)
	if err != nil {
		t.Fatalf("Fetch() error = %v, want the server-fetched item", err)
	}
	if item.Body != "Plain body text" {
		t.Errorf("Body = %q, want the text/plain part", item.Body)
	}
	if !item.Unread {
		t.Error("Fetch() marked the message read; it must use BODY.PEEK")
	}

	stored, err := st.Get(context.Background(), seed.ID)
	if err != nil {
		t.Fatalf("store.Get after Fetch: %v, want the item upserted", err)
	}
	if len(stored.Attachments) != 1 {
		t.Fatalf("stored.Attachments = %d, want 1 (so download works without a second full fetch)", len(stored.Attachments))
	}

	destPath := filepath.Join(t.TempDir(), "recording.mp4")
	res, err := svc.Download(context.Background(), seed.ID, 0, destPath, core.DownloadOptions{})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.Name != "recording.mp4" {
		t.Errorf("Download Name = %q, want recording.mp4", res.Name)
	}
	if _, err := os.Stat(destPath); err != nil {
		t.Errorf("downloaded file missing: %v", err)
	}

	if messageHasFlag(t, addr, "INBOX", "<multi@example.org>", imap.FlagSeen) {
		t.Error("server marked the message \\Seen; Fetch's fallback must use BODY.PEEK")
	}
}

// TestServiceFetchUnknownAccountStaysErrNotFound: an id for an account
// this daemon has no adapter for must stay ErrNotFound, never dispatch
// to a different account's adapter.
func TestServiceFetchUnknownAccountStaysErrNotFound(t *testing.T) {
	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure("127.0.0.1:0"))

	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	reg := core.NewRegistry()
	reg.Register(adapter)
	svc := core.NewService(st, reg)

	if _, err := svc.Fetch(context.Background(), "mail:other:1.1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Fetch err = %v, want ErrNotFound", err)
	}
}

// TestServiceFetchServerMissingUIDStaysErrNotFound: a UID the server
// genuinely never had (the mailbox only ever held one message) stays
// ErrNotFound and is never upserted.
func TestServiceFetchServerMissingUIDStaysErrNotFound(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", multipartMessage)

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	seedSink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, seedSink) }()
	seed := waitForUpsert(t, seedSink, 5*time.Second)
	cancel()
	<-done

	_, _, uidValidity, _, err := parseItemID(seed.ID)
	if err != nil {
		t.Fatalf("parseItemID(%q): %v", seed.ID, err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	reg := core.NewRegistry()
	reg.Register(adapter)
	svc := core.NewService(st, reg)

	// The mailbox only ever held one message: UID 999999 with the same
	// UIDVALIDITY is a UID the server genuinely never had.
	missingID := itemID("cl", "INBOX", uidValidity, 999999)
	if _, err := svc.Fetch(context.Background(), missingID); err == nil {
		t.Fatal("Fetch err = nil, want ErrNotFound")
	}
	if _, err := st.Get(context.Background(), missingID); err == nil {
		t.Fatal("store.Get = nil error, nothing should have been upserted for a UID the server lacks")
	}
}
