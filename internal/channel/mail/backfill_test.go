package mail

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// appendMessageAt is appendMessage plus an explicit INTERNALDATE, so
// Backfill's UID SEARCH SINCE tests can control which messages are "old"
// vs "new" independent of wall-clock APPEND time.
func appendMessageAt(t *testing.T, addr, mailbox, raw string, at time.Time) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial for append: %v", err)
	}
	client := imapclient.New(conn, nil)
	defer client.Close()
	if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
		t.Fatalf("login for append: %v", err)
	}
	cmd := client.Append(mailbox, int64(len(raw)), &imap.AppendOptions{Time: at})
	if _, err := cmd.Write([]byte(raw)); err != nil {
		t.Fatalf("append write: %v", err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatalf("append close: %v", err)
	}
	if _, err := cmd.Wait(); err != nil {
		t.Fatalf("append wait: %v", err)
	}
}

func rawBackfillMessage(subject string) string {
	return "From: a@x\r\nSubject: " + subject + "\r\nDate: Fri, 25 Sep 2026 10:00:00 +0000\r\n\r\nbody\r\n"
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestAdapterBackfillFindsOnlyMessagesSinceDate: mail-history H2's core
// acceptance scenario — an "old" message before --since and a "new" one
// on/after it; only the new one is added.
func TestAdapterBackfillFindsOnlyMessagesSinceDate(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessageAt(t, addr, "INBOX", rawBackfillMessage("old"), time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	appendMessageAt(t, addr, "INBOX", rawBackfillMessage("new"), time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	st := openTestStore(t)

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	result, err := adapter.Backfill(context.Background(), st, "INBOX", since, false)
	if err != nil {
		t.Fatalf("Backfill error = %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("result.Count = %d, want 1 (only the 'new' message)", result.Count)
	}

	items, err := st.List(context.Background(), core.Filter{Channel: core.ChannelMail, Account: "cl"})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(items) != 1 || items[0].Subject != "new" {
		t.Fatalf("stored items = %+v, want exactly the 'new' message", items)
	}
}

// TestAdapterBackfillUpsertsOnlyMissingUIDs: a message the store already
// has (from a prior sync) must not need re-fetching; Backfill still
// reports it in the range but Count only reflects what it actually
// upserted (the message the store lacked).
func TestAdapterBackfillUpsertsOnlyMissingUIDs(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	since := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	appendMessageAt(t, addr, "INBOX", rawBackfillMessage("first"), since.Add(time.Hour))
	appendMessageAt(t, addr, "INBOX", rawBackfillMessage("second"), since.Add(2*time.Hour))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	st := openTestStore(t)

	// Simulate "first" already synced by a prior real Run: its item id
	// uses the same "mail:cl:<uidvalidity>.<uid>" scheme Backfill itself
	// builds, so pre-seeding uid 1 of the account's INBOX is exactly
	// "the store already has it".
	uidValidity, err := discoverUIDValidity(t, addr)
	if err != nil {
		t.Fatalf("discoverUIDValidity: %v", err)
	}
	seeded := core.Item{ID: itemID("cl", "INBOX", uidValidity, 1), Channel: core.ChannelMail, Account: "cl", Subject: "first (already synced)"}
	if err := st.Upsert(context.Background(), seeded); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	result, err := adapter.Backfill(context.Background(), st, "INBOX", since, false)
	if err != nil {
		t.Fatalf("Backfill error = %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("result.Count = %d, want 1 (only uid 2, 'second')", result.Count)
	}

	got, err := st.Get(context.Background(), seeded.ID)
	if err != nil {
		t.Fatalf("store.Get(seeded): %v", err)
	}
	if got.Subject != "first (already synced)" {
		t.Errorf("seeded item Subject = %q, want its untouched original (Backfill must not re-fetch it)", got.Subject)
	}
}

// TestAdapterBackfillDryRunUpsertsNothing: --dry-run reports the count
// and range without writing anything to the store.
func TestAdapterBackfillDryRunUpsertsNothing(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	since := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	appendMessageAt(t, addr, "INBOX", rawBackfillMessage("new"), since.Add(time.Hour))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	st := openTestStore(t)

	result, err := adapter.Backfill(context.Background(), st, "INBOX", since, true)
	if err != nil {
		t.Fatalf("Backfill error = %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("result.Count = %d, want 1 (dry-run still reports what it would add)", result.Count)
	}
	if result.FirstID == "" || result.LastID == "" {
		t.Errorf("result = %+v, want a non-empty id range even on dry-run", result)
	}

	items, err := st.List(context.Background(), core.Filter{Channel: core.ChannelMail, Account: "cl"})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("store has %d items after a dry-run, want 0", len(items))
	}
}

// TestAdapterBackfillIsIdempotent: running Backfill twice inserts
// nothing new the second time.
func TestAdapterBackfillIsIdempotent(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	since := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	appendMessageAt(t, addr, "INBOX", rawBackfillMessage("new"), since.Add(time.Hour))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	st := openTestStore(t)

	if _, err := adapter.Backfill(context.Background(), st, "INBOX", since, false); err != nil {
		t.Fatalf("first Backfill error = %v", err)
	}
	second, err := adapter.Backfill(context.Background(), st, "INBOX", since, false)
	if err != nil {
		t.Fatalf("second Backfill error = %v", err)
	}
	if second.Count != 0 {
		t.Fatalf("second Backfill Count = %d, want 0 (already synced)", second.Count)
	}

	items, err := st.List(context.Background(), core.Filter{Channel: core.ChannelMail, Account: "cl"})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("store has %d items, want exactly 1 after two backfills", len(items))
	}
}

// TestAdapterBackfillNeverMovesTheSyncCursor: Backfill must never touch
// the cursors Run's own syncFrom/fetchAndUpsert persist, so a later Run
// resumes exactly where it left off.
func TestAdapterBackfillNeverMovesTheSyncCursor(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	since := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	appendMessageAt(t, addr, "INBOX", rawBackfillMessage("new"), since.Add(time.Hour))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	st := openTestStore(t)

	if _, err := adapter.Backfill(context.Background(), st, "INBOX", since, false); err != nil {
		t.Fatalf("Backfill error = %v", err)
	}

	for _, key := range []string{cursorKey("cl", "inbox.uidvalidity"), cursorKey("cl", "inbox.last_uid")} {
		val, err := st.Cursor(context.Background(), key)
		if err != nil {
			t.Fatalf("Cursor(%q): %v", key, err)
		}
		if val != "" {
			t.Errorf("cursor %q = %q, want untouched (empty, a fresh store never Run)", key, val)
		}
	}
}

// TestAdapterBackfillNeverMarksSeen: Backfill's header fetch must use
// BODY.PEEK like every other read-only mail path, never marking the
// message \Seen on the server.
func TestAdapterBackfillNeverMarksSeen(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	since := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	appendMessageAt(t, addr, "INBOX", multipartMessage, since.Add(time.Hour))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	st := openTestStore(t)

	if _, err := adapter.Backfill(context.Background(), st, "INBOX", since, false); err != nil {
		t.Fatalf("Backfill error = %v", err)
	}
	if messageHasFlag(t, addr, "INBOX", "<multi@example.org>", imap.FlagSeen) {
		t.Error("server marked the message \\Seen; Backfill must use BODY.PEEK")
	}
}

// TestAdapterBackfillWrongChannelIsUnsupported isn't applicable at the
// adapter level (the mail Adapter is always core.ChannelMail); the
// "clear error when the channel isn't mail" acceptance criterion is
// covered at the CLI layer (cmd/bunker/backfill_test.go) and the core
// layer (internal/core/service_test.go's ErrUnsupported tests), where
// the channel selection actually happens.

// discoverUIDValidity opens a throwaway connection to learn INBOX's
// current UIDVALIDITY, the same way Backfill's own dial+Select does.
func discoverUIDValidity(t *testing.T, addr string) (uint32, error) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return 0, err
	}
	client := imapclient.New(conn, nil)
	defer client.Close()
	if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
		return 0, err
	}
	mbox, err := client.Select("INBOX", nil).Wait()
	if err != nil {
		return 0, err
	}
	return mbox.UIDValidity, nil
}
