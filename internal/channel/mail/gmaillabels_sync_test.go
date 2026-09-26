package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// gmailAdapterForTest wires an Adapter whose primary IMAP connection
// (Run/Fetch/reconcile's ordinary client.Fetch/Select/Idle calls) goes
// to primaryAddr (a plain memIMAPServer, via testDialInsecure) while its
// Gmail-only raw XOAUTH2 connection (for X-GM-LABELS) goes to
// gmailAddr (the fake Gmail TLS server) — the two are entirely
// independent dials in production too (see gmaillabels.go's package
// comment), so a test is free to point them at different fakes.
func gmailAdapterForTest(t *testing.T, primaryAddr, gmailAddr string) *Adapter {
	t.Helper()
	host, portStr, err := net.SplitHostPort(gmailAddr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", gmailAddr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	cfg := AccountConfig{Name: "com", Username: "alice@example.com", IMAPHost: host, IMAPPort: port, Gmail: true}
	adapter := newAdapter(cfg, nil, fixedTokenSource("ya29.fake"), testDialInsecure(primaryAddr))
	adapter.gmailTLSConfig = &tls.Config{InsecureSkipVerify: true}
	return adapter
}

// TestAdapterRunAppliesGmailLabelsFromRawFetch covers T14(a)'s Gmail
// half wired into ordinary sync: a message synced over the primary IMAP
// connection gets its Labels filled in from a batched X-GM-LABELS fetch
// over the raw connection, matching the live gap ("Labels come back
// empty" on `com`, 2026-09-25) this task closes.
func TestAdapterRunAppliesGmailLabelsFromRawFetch(t *testing.T) {
	primaryAddr, _, _ := newMemIMAPServer(t)
	appendMessage(t, primaryAddr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))
	gmailAddr, _ := newFakeGmailIMAPServer(t)

	adapter := gmailAdapterForTest(t, primaryAddr, gmailAddr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	item := waitForUpsert(t, sink, 5*time.Second)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}

	if len(item.Labels) != 1 || item.Labels[0] != "bunker-test" {
		t.Errorf("Labels = %v, want [\"bunker-test\"] from the raw X-GM-LABELS fetch", item.Labels)
	}
}

// TestAdapterFetchAppliesGmailLabels covers T14(a) for core.Fetcher:
// a single re-fetch of one Gmail item also fills in Labels from the raw
// X-GM-LABELS connection.
func TestAdapterFetchAppliesGmailLabels(t *testing.T) {
	primaryAddr, _, _ := newMemIMAPServer(t)
	appendMessage(t, primaryAddr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))
	uidValidity := mailboxUIDValidity(t, primaryAddr, "INBOX")
	gmailAddr, _ := newFakeGmailIMAPServer(t)

	adapter := gmailAdapterForTest(t, primaryAddr, gmailAddr)

	id := fmt.Sprintf("mail:com:%d.1", uidValidity)
	item, err := adapter.Fetch(context.Background(), id)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(item.Labels) != 1 || item.Labels[0] != "bunker-test" {
		t.Errorf("Labels = %v, want [\"bunker-test\"] from the raw X-GM-LABELS fetch", item.Labels)
	}
}

// TestAdapterRunAppliesGmailLabelsOnStartupReconciliation covers
// T14(a) for startup reconciliation (T9c): a stored Gmail item that
// survives the UID presence check also gets its Labels refreshed from
// the raw X-GM-LABELS connection, bounded to just the still-present
// stored items.
func TestAdapterRunAppliesGmailLabelsOnStartupReconciliation(t *testing.T) {
	primaryAddr, _, _ := newMemIMAPServer(t)
	appendMessage(t, primaryAddr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))
	uidValidity := mailboxUIDValidity(t, primaryAddr, "INBOX")
	gmailAddr, _ := newFakeGmailIMAPServer(t)

	adapter := gmailAdapterForTest(t, primaryAddr, gmailAddr)

	id := fmt.Sprintf("mail:com:%d.1", uidValidity)
	sink := newFakeSink()
	sink.items[id] = core.Item{
		ID: id, Channel: core.ChannelMail, Account: "com",
		Unread: true, Meta: map[string]string{"folder": "INBOX"},
	}
	sink.cursors[cursorKey("com", "inbox.uidvalidity")] = fmt.Sprint(uidValidity)
	sink.cursors[cursorKey("com", "inbox.last_uid")] = "1"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	deadline := time.Now().Add(5 * time.Second)
	var refreshed core.Item
	for time.Now().Before(deadline) {
		it, err := sink.getItem(id)
		if err == nil && len(it.Labels) == 1 && it.Labels[0] == "bunker-test" {
			refreshed = it
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if refreshed.ID == "" {
		final, _ := sink.getItem(id)
		t.Fatalf("stored item's Labels were never refreshed from the raw X-GM-LABELS fetch, last seen = %v", final.Labels)
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}
