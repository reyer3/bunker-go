package mail

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// secondClient dials addr as an independent, authenticated session,
// simulating Roundcube/Gmail acting on the same mailbox behind the
// adapter's back (T9b: "moves/reads done in Roundcube or Gmail never
// reach the store").
func secondClient(t *testing.T, addr, mailbox string) *imapclient.Client {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial second client: %v", err)
	}
	client := imapclient.New(conn, nil)
	t.Cleanup(func() { client.Close() })
	if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
		t.Fatalf("login second client: %v", err)
	}
	if _, err := client.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("select %s on second client: %v", mailbox, err)
	}
	return client
}

// TestAdapterRunObservesExpungeFromAnotherClient covers T9(b): a second
// IMAP session deletes+expunges a message the adapter already synced
// while it is IDLEing, and the adapter must remove it from the store
// instead of leaving the stale row the live bug on 2026-09-25 left
// behind.
func TestAdapterRunObservesExpungeFromAnotherClient(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<keep@x>", "", "Keep", "a@x", "r@x", "b"))
	appendMessage(t, addr, "INBOX", rawMessage("<gone@x>", "", "Gone", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	first := waitForUpsert(t, sink, 5*time.Second)
	second := waitForUpsert(t, sink, 5*time.Second)
	var goneID string
	for _, item := range []core.Item{first, second} {
		if item.Subject == "Gone" {
			goneID = item.ID
		}
	}
	if goneID == "" {
		t.Fatal("never saw the 'Gone' message upserted")
	}

	other := secondClient(t, addr, "INBOX")
	var seqSet imap.SeqSet
	seqSet.AddRange(2, 2) // "Gone" was appended second
	if err := other.Store(seqSet, &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted},
	}, nil).Close(); err != nil {
		t.Fatalf("store \\Deleted on second client: %v", err)
	}
	if _, err := other.Expunge().Collect(); err != nil {
		t.Fatalf("expunge on second client: %v", err)
	}

	deletedID := waitForDelete(t, sink, 5*time.Second)
	if deletedID != goneID {
		t.Errorf("deleted id = %q, want %q", deletedID, goneID)
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

// TestAdapterRunObservesFlagChangeFromAnotherClient covers T9(b): a
// second IMAP session marks a message \Seen (e.g. read in Roundcube)
// while the adapter is IDLEing, and the adapter must reconcile the
// store's Unread flag instead of it staying stale.
func TestAdapterRunObservesFlagChangeFromAnotherClient(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	seed := waitForUpsert(t, sink, 5*time.Second)
	if !seed.Unread {
		t.Fatal("seed item is not Unread before the external \\Seen")
	}

	other := secondClient(t, addr, "INBOX")
	var seqSet imap.SeqSet
	seqSet.AddRange(1, 1)
	if err := other.Store(seqSet, &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen},
	}, nil).Close(); err != nil {
		t.Fatalf("store \\Seen on second client: %v", err)
	}

	ev := waitForMarkRead(t, sink, 5*time.Second)
	if ev.id != seed.ID {
		t.Errorf("MarkRead id = %q, want %q", ev.id, seed.ID)
	}
	if !ev.read {
		t.Errorf("MarkRead read = false, want true (\\Seen was added)")
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

// TestAdapterRunObservesKeywordChangeFromAnotherClient covers T14(a): a
// second IMAP session adds a Dovecot custom keyword (e.g. a label
// applied in Roundcube) to a message the adapter already synced while
// it is IDLEing, and the adapter must reconcile the store's Labels
// instead of leaving them stale (the live gap: mail:cl:...13810 carried
// the "bunker-test" keyword on the server but Labels stayed null).
func TestAdapterRunObservesKeywordChangeFromAnotherClient(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	seed := waitForUpsert(t, sink, 5*time.Second)
	if len(seed.Labels) != 0 {
		t.Fatalf("seed item already has Labels = %v, want none before the external keyword", seed.Labels)
	}

	other := secondClient(t, addr, "INBOX")
	var seqSet imap.SeqSet
	seqSet.AddRange(1, 1)
	if err := other.Store(seqSet, &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{"bunker-test"},
	}, nil).Close(); err != nil {
		t.Fatalf("store keyword on second client: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var got core.Item
	for time.Now().Before(deadline) {
		it, err := sink.getItem(seed.ID)
		if err == nil && len(it.Labels) == 1 && it.Labels[0] == "bunker-test" {
			got = it
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.ID == "" {
		final, _ := sink.getItem(seed.ID)
		t.Fatalf("stored item's Labels never picked up the external keyword, last seen = %v", final.Labels)
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}
