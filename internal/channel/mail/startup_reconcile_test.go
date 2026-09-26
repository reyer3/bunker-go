package mail

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// mailboxUIDValidity opens a throwaway connection to learn mailbox's
// current UIDVALIDITY, so a test can pre-seed the store with an item id
// that looks exactly like one a prior sync of this same mailbox
// instance would have produced.
func mailboxUIDValidity(t *testing.T, addr, mailbox string) uint32 {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial for uidvalidity: %v", err)
	}
	client := imapclient.New(conn, nil)
	defer client.Close()
	if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
		t.Fatalf("login for uidvalidity: %v", err)
	}
	mbox, err := client.Select(mailbox, nil).Wait()
	if err != nil {
		t.Fatalf("select %s for uidvalidity: %v", mailbox, err)
	}
	return mbox.UIDValidity
}

// TestAdapterRunDropsStaleStoredItemOnStartup covers T9(c): a stored
// INBOX item whose UID no longer exists on the server — exactly the
// live regression row kept on purpose, mail:cl:1700000000.100, after
// the first live MOVE test on 2026-09-25 — must be gone once Run has
// started, and counts must drop with it.
func TestAdapterRunDropsStaleStoredItemOnStartup(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<keep@x>", "", "Keep", "a@x", "r@x", "b"))
	uidValidity := mailboxUIDValidity(t, addr, "INBOX")

	staleID := fmt.Sprintf("mail:cl:%d.999", uidValidity)
	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	sink := newFakeSink()
	sink.items[staleID] = core.Item{
		ID: staleID, Channel: core.ChannelMail, Account: "cl",
		Unread: true, Meta: map[string]string{"folder": "INBOX"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	waitForUpsert(t, sink, 5*time.Second) // "Keep" from the initial sync

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := sink.getItem(staleID); err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := sink.getItem(staleID); err == nil {
		t.Errorf("stale item %s is still stored after Run started", staleID)
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

// TestAdapterRunRefreshesSeenForRemainingStoredItems covers T9(c): a
// stored item whose UID is still present, but whose message was marked
// \Seen on the server before the daemon (re)started, must have its
// stored Unread flag refreshed to match. The sync cursor is pre-seeded
// past this message's own UID, so the ordinary incremental sync (which
// only fetches mail newer than the cursor) has nothing to do here —
// isolating the startup reconciliation's own FETCH FLAGS refresh from
// the unrelated case where an item happens to still be inside the
// normal initial-sync window.
func TestAdapterRunRefreshesSeenForRemainingStoredItems(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))
	markSeenDirect(t, addr, "INBOX", 1)
	uidValidity := mailboxUIDValidity(t, addr, "INBOX")

	id := fmt.Sprintf("mail:cl:%d.1", uidValidity)
	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	sink := newFakeSink()
	sink.items[id] = core.Item{
		ID: id, Channel: core.ChannelMail, Account: "cl",
		Unread: true, Meta: map[string]string{"folder": "INBOX"},
	}
	sink.cursors[cursorKey("cl", "inbox.uidvalidity")] = fmt.Sprint(uidValidity)
	sink.cursors[cursorKey("cl", "inbox.last_uid")] = "1"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	deadline := time.Now().Add(5 * time.Second)
	var refreshed core.Item
	for time.Now().Before(deadline) {
		it, err := sink.getItem(id)
		if err == nil && !it.Unread {
			refreshed = it
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if refreshed.ID == "" {
		t.Fatal("stored item's Unread was never refreshed to false")
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

// TestAdapterRunDropsEveryStoredItemOnUIDValidityChange covers T9(c)'s
// UIDVALIDITY-changed branch: a stored item keyed by an old, no-longer-
// current UIDVALIDITY is dropped outright (its UID could now mean an
// entirely different message), rather than being individually resolved.
func TestAdapterRunDropsEveryStoredItemOnUIDValidityChange(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<keep@x>", "", "Keep", "a@x", "r@x", "b"))
	currentValidity := mailboxUIDValidity(t, addr, "INBOX")

	// a UIDVALIDITY that is guaranteed not to match the mailbox's real
	// (freshly assigned) one, simulating the mailbox having been
	// recreated since this item was stored.
	staleID := fmt.Sprintf("mail:cl:%d.1", currentValidity+1000)
	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	sink := newFakeSink()
	sink.items[staleID] = core.Item{
		ID: staleID, Channel: core.ChannelMail, Account: "cl",
		Unread: true, Meta: map[string]string{"folder": "INBOX"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	waitForUpsert(t, sink, 5*time.Second) // "Keep" from the initial sync

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := sink.getItem(staleID); err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := sink.getItem(staleID); err == nil {
		t.Errorf("stored item %s with a stale UIDVALIDITY is still present", staleID)
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

// TestAdapterRunRefreshesLabelsForRemainingStoredItemsOnStartup covers
// T14(a): startup reconciliation (T9c) must also read Dovecot keywords
// back into a still-present stored item's Labels, the same live gap
// TestAdapterRunObservesKeywordChangeFromAnotherClient covers for the
// IDLE path — this one for a keyword already on the server before the
// daemon (re)started, not one added while it was running.
func TestAdapterRunRefreshesLabelsForRemainingStoredItemsOnStartup(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessageWithFlags(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"),
		[]imap.Flag{"bunker-test"})
	uidValidity := mailboxUIDValidity(t, addr, "INBOX")

	id := fmt.Sprintf("mail:cl:%d.1", uidValidity)
	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	sink := newFakeSink()
	sink.items[id] = core.Item{
		ID: id, Channel: core.ChannelMail, Account: "cl",
		Unread: true, Meta: map[string]string{"folder": "INBOX"},
	}
	sink.cursors[cursorKey("cl", "inbox.uidvalidity")] = fmt.Sprint(uidValidity)
	sink.cursors[cursorKey("cl", "inbox.last_uid")] = "1"

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
		t.Fatalf("stored item's Labels were never refreshed from the server keyword, last seen = %v", final.Labels)
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

// markSeenDirect flags message seq as \Seen via a throwaway connection,
// independent of the adapter under test.
func markSeenDirect(t *testing.T, addr, mailbox string, seq uint32) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial for mark seen: %v", err)
	}
	client := imapclient.New(conn, nil)
	defer client.Close()
	if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
		t.Fatalf("login for mark seen: %v", err)
	}
	if _, err := client.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("select %s for mark seen: %v", mailbox, err)
	}
	var seqSet imap.SeqSet
	seqSet.AddRange(seq, seq)
	if err := client.Store(seqSet, &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen},
	}, nil).Close(); err != nil {
		t.Fatalf("store \\Seen: %v", err)
	}
}
