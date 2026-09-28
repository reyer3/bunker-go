package mail

import (
	"context"
	"testing"
	"time"
)

// TestNewAdapterUsesConfiguredInitialSyncLimit: mail-history H4.
// initial_sync_limit narrows Run's initial-sync window down from the
// 200-message default, wired straight from AccountConfig.InitialSyncLimit
// (ParseAccountConfig's decoded option) into newAdapter's own
// initialSyncLimit field.
func TestNewAdapterUsesConfiguredInitialSyncLimit(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", "From: a@x\r\nSubject: first\r\nDate: Fri, 25 Sep 2026 09:00:00 +0000\r\n\r\nbody1\r\n")
	appendMessage(t, addr, "INBOX", "From: a@x\r\nSubject: second\r\nDate: Fri, 25 Sep 2026 10:00:00 +0000\r\n\r\nbody2\r\n")

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused", InitialSyncLimit: 1}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	first := waitForUpsert(t, sink, 5*time.Second)
	if first.Subject != "second" {
		t.Fatalf("first synced item Subject = %q, want %q (the newest, initial_sync_limit=1)", first.Subject, "second")
	}

	select {
	case extra := <-sink.upserts:
		t.Fatalf("got an extra upsert %+v, want only 1 message synced (initial_sync_limit=1)", extra)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	<-done
}

// TestNewAdapterDefaultsInitialSyncLimitWhenConfigOmitsIt: an
// AccountConfig built without going through ParseAccountConfig (as most
// of this package's tests do) must still default to the 200-message
// window newAdapter always used before H4 — InitialSyncLimit's zero
// value must never mean "sync nothing".
func TestNewAdapterDefaultsInitialSyncLimitWhenConfigOmitsIt(t *testing.T) {
	adapter := newAdapter(AccountConfig{Name: "cl", IMAPHost: "unused"}, nil, nil, testDialInsecure("127.0.0.1:0"))
	if adapter.initialSyncLimit != defaultInitialSyncLimit {
		t.Errorf("initialSyncLimit = %d, want the default %d", adapter.initialSyncLimit, defaultInitialSyncLimit)
	}
}
