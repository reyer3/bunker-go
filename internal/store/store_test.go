package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func sampleItem() core.Item {
	return core.Item{
		ID:         "mail:cl:1",
		Channel:    core.ChannelMail,
		Account:    "cl",
		Thread:     "t1",
		ThreadName: "Hello thread",
		From:       core.Address{ID: "a@b.cl", Name: "A"},
		To:         []core.Address{{ID: "c@d.cl", Name: "C"}},
		Subject:    "hi",
		Body:       "body",
		Attachments: []core.Attachment{
			{Name: "f.pdf", MIME: "application/pdf", Size: 10, Ref: "ref1"},
		},
		Labels:    []string{"inbox", "vip"},
		Unread:    true,
		FromMe:    false,
		Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Meta:      map[string]string{"uid": "42"},
	}
}

func TestUpsertAndGetRoundTrips(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	want := sampleItem()

	if err := s.Upsert(ctx, want); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := s.Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != want.ID || got.Subject != want.Subject || got.Body != want.Body {
		t.Fatalf("Get() = %+v, want %+v", got, want)
	}
	if got.From != want.From {
		t.Fatalf("From = %+v, want %+v", got.From, want.From)
	}
	if len(got.To) != 1 || got.To[0] != want.To[0] {
		t.Fatalf("To = %+v, want %+v", got.To, want.To)
	}
	if len(got.Attachments) != 1 || got.Attachments[0] != want.Attachments[0] {
		t.Fatalf("Attachments = %+v, want %+v", got.Attachments, want.Attachments)
	}
	if len(got.Labels) != 2 {
		t.Fatalf("Labels = %+v, want 2 entries", got.Labels)
	}
	if !got.Unread {
		t.Fatalf("Unread = false, want true")
	}
	if !got.Timestamp.Equal(want.Timestamp) {
		t.Fatalf("Timestamp = %v, want %v", got.Timestamp, want.Timestamp)
	}
	if got.Meta["uid"] != "42" {
		t.Fatalf("Meta = %+v, want uid=42", got.Meta)
	}
	if got.FromMe {
		t.Fatalf("FromMe = true, want false")
	}
}

func TestUpsertAndGetRoundTripsFromMe(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	want := sampleItem()
	want.ID = "whatsapp:personal:1"
	want.Channel = core.ChannelWhatsApp
	want.Account = "personal"
	want.FromMe = true

	if err := s.Upsert(ctx, want); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.FromMe {
		t.Fatalf("FromMe = false, want true")
	}
}

func TestGetUnknownReturnsErrNotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.Get(context.Background(), "mail:cl:missing")
	if err == nil {
		t.Fatal("expected error for unknown id")
	}
}

func TestUpsertIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := sampleItem()

	if err := s.Upsert(ctx, item); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	item.Subject = "updated subject"
	item.Labels = []string{"inbox"}
	if err := s.Upsert(ctx, item); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, err := s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Subject != "updated subject" {
		t.Fatalf("Subject = %q, want %q", got.Subject, "updated subject")
	}
	if len(got.Labels) != 1 || got.Labels[0] != "inbox" {
		t.Fatalf("Labels = %+v, want [inbox]", got.Labels)
	}

	all, err := s.List(ctx, core.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("List len = %d, want 1 (upsert must not duplicate rows)", len(all))
	}
}

func TestMarkReadPreservesOtherFields(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := sampleItem()
	if err := s.Upsert(ctx, item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := s.MarkRead(ctx, item.ID, true); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}

	got, err := s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Unread {
		t.Fatalf("Unread = true after MarkRead(true), want false")
	}
	if got.Subject != item.Subject {
		t.Fatalf("Subject changed by MarkRead: got %q, want %q", got.Subject, item.Subject)
	}

	if err := s.MarkRead(ctx, item.ID, false); err != nil {
		t.Fatalf("MarkRead(false): %v", err)
	}
	got, err = s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Unread {
		t.Fatalf("Unread = false after MarkRead(false), want true")
	}
}

// threadItem builds a fictional item for MarkThreadReadUpTo tests: same
// channel/account/thread by default, unread and not from the user, at ts.
func threadItem(id, channel, account, thread string, ts time.Time, unread, fromMe bool) core.Item {
	return core.Item{
		ID:        id,
		Channel:   core.Channel(channel),
		Account:   account,
		Thread:    thread,
		From:      core.Address{ID: "them@x.cl"},
		Subject:   "hi",
		Unread:    unread,
		FromMe:    fromMe,
		Timestamp: ts,
	}
}

func TestMarkThreadReadUpToMarksUnreadItemsAtOrBeforeCutoff(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

	before := threadItem("mail:cl:before", "mail", "cl", "t1", base, true, false)
	atCutoff := threadItem("mail:cl:at", "mail", "cl", "t1", base.Add(1*time.Minute), true, false)
	after := threadItem("mail:cl:after", "mail", "cl", "t1", base.Add(2*time.Minute), true, false)
	for _, it := range []core.Item{before, atCutoff, after} {
		if err := s.Upsert(ctx, it); err != nil {
			t.Fatalf("Upsert %s: %v", it.ID, err)
		}
	}

	cutoff := base.Add(1 * time.Minute)
	if err := s.MarkThreadReadUpTo(ctx, core.ChannelMail, "cl", "t1", cutoff); err != nil {
		t.Fatalf("MarkThreadReadUpTo: %v", err)
	}

	gotBefore, err := s.Get(ctx, before.ID)
	if err != nil {
		t.Fatalf("Get before: %v", err)
	}
	if gotBefore.Unread {
		t.Fatalf("item before cutoff still unread")
	}
	gotAt, err := s.Get(ctx, atCutoff.ID)
	if err != nil {
		t.Fatalf("Get at: %v", err)
	}
	if gotAt.Unread {
		t.Fatalf("item at cutoff still unread")
	}
	gotAfter, err := s.Get(ctx, after.ID)
	if err != nil {
		t.Fatalf("Get after: %v", err)
	}
	if !gotAfter.Unread {
		t.Fatalf("item after cutoff was marked read, want still unread")
	}
}

func TestMarkThreadReadUpToNeverTouchesFromMeItems(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

	// FromMe items should never be "unread" in practice, but this proves
	// MarkThreadReadUpTo's own filter, not just an upstream invariant.
	mine := threadItem("whatsapp:personal:mine", "whatsapp", "personal", "t1", base, true, true)
	if err := s.Upsert(ctx, mine); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := s.MarkThreadReadUpTo(ctx, core.ChannelWhatsApp, "personal", "t1", base.Add(time.Hour)); err != nil {
		t.Fatalf("MarkThreadReadUpTo: %v", err)
	}

	got, err := s.Get(ctx, mine.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Unread {
		t.Fatalf("FromMe item was marked read, want untouched")
	}
}

func TestMarkThreadReadUpToNeverTouchesOtherThreadsOrAccounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

	otherThread := threadItem("whatsapp:personal:other-thread", "whatsapp", "personal", "t2", base, true, false)
	otherAccount := threadItem("whatsapp:work:other-account", "whatsapp", "work", "t1", base, true, false)
	otherChannel := threadItem("matrix:personal:other-channel", "matrix", "personal", "t1", base, true, false)
	for _, it := range []core.Item{otherThread, otherAccount, otherChannel} {
		if err := s.Upsert(ctx, it); err != nil {
			t.Fatalf("Upsert %s: %v", it.ID, err)
		}
	}

	if err := s.MarkThreadReadUpTo(ctx, core.ChannelWhatsApp, "personal", "t1", base.Add(time.Hour)); err != nil {
		t.Fatalf("MarkThreadReadUpTo: %v", err)
	}

	for _, id := range []string{otherThread.ID, otherAccount.ID, otherChannel.ID} {
		got, err := s.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if !got.Unread {
			t.Fatalf("item %s outside the (channel,account,thread) scope was marked read", id)
		}
	}
}

func TestMarkThreadReadUpToNoUnreadItemsIsNotAnError(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.MarkThreadReadUpTo(ctx, core.ChannelMail, "cl", "no-such-thread", time.Now()); err != nil {
		t.Fatalf("MarkThreadReadUpTo on an empty thread: %v", err)
	}
}

func TestCursorRoundTrips(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	got, err := s.Cursor(ctx, "mail:cl")
	if err != nil {
		t.Fatalf("Cursor (unset): %v", err)
	}
	if got != "" {
		t.Fatalf("Cursor (unset) = %q, want empty", got)
	}

	if err := s.SetCursor(ctx, "mail:cl", "uid:100"); err != nil {
		t.Fatalf("SetCursor: %v", err)
	}
	got, err = s.Cursor(ctx, "mail:cl")
	if err != nil {
		t.Fatalf("Cursor: %v", err)
	}
	if got != "uid:100" {
		t.Fatalf("Cursor = %q, want uid:100", got)
	}

	if err := s.SetCursor(ctx, "mail:cl", "uid:200"); err != nil {
		t.Fatalf("SetCursor (update): %v", err)
	}
	got, _ = s.Cursor(ctx, "mail:cl")
	if got != "uid:200" {
		t.Fatalf("Cursor after update = %q, want uid:200", got)
	}
}

func TestListFiltersByChannelAccountUnreadLabelQuery(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	mailUnread := sampleItem()
	mailRead := sampleItem()
	mailRead.ID = "mail:cl:2"
	mailRead.Unread = false
	mailRead.Subject = "second"
	mailRead.Labels = []string{"inbox"}
	wa := sampleItem()
	wa.ID = "whatsapp:personal:1"
	wa.Channel = core.ChannelWhatsApp
	wa.Account = "personal"
	wa.Subject = "wa hello"
	wa.Labels = nil

	for _, it := range []core.Item{mailUnread, mailRead, wa} {
		if err := s.Upsert(ctx, it); err != nil {
			t.Fatalf("Upsert %s: %v", it.ID, err)
		}
	}

	byChannel, err := s.List(ctx, core.Filter{Channel: core.ChannelMail})
	if err != nil {
		t.Fatalf("List by channel: %v", err)
	}
	if len(byChannel) != 2 {
		t.Fatalf("List by channel len = %d, want 2", len(byChannel))
	}

	unreadOnly := true
	byUnread, err := s.List(ctx, core.Filter{Unread: &unreadOnly})
	if err != nil {
		t.Fatalf("List by unread: %v", err)
	}
	if len(byUnread) != 2 { // mailUnread + wa (sampleItem default Unread=true)
		t.Fatalf("List by unread len = %d, want 2", len(byUnread))
	}

	byLabel, err := s.List(ctx, core.Filter{Label: "vip"})
	if err != nil {
		t.Fatalf("List by label: %v", err)
	}
	if len(byLabel) != 1 || byLabel[0].ID != mailUnread.ID {
		t.Fatalf("List by label = %+v, want just %s", byLabel, mailUnread.ID)
	}

	byQuery, err := s.List(ctx, core.Filter{Query: "wa hello"})
	if err != nil {
		t.Fatalf("List by query: %v", err)
	}
	if len(byQuery) != 1 || byQuery[0].ID != wa.ID {
		t.Fatalf("List by query = %+v, want just %s", byQuery, wa.ID)
	}

	limited, err := s.List(ctx, core.Filter{Limit: 1})
	if err != nil {
		t.Fatalf("List with limit: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("List with limit len = %d, want 1", len(limited))
	}
}

func TestListFiltersByThread(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	inThread := sampleItem()
	sameThreadOtherAccount := sampleItem()
	sameThreadOtherAccount.ID = "mail:other:1"
	sameThreadOtherAccount.Account = "other"
	otherThread := sampleItem()
	otherThread.ID = "mail:cl:2"
	otherThread.Thread = "t2"

	for _, it := range []core.Item{inThread, sameThreadOtherAccount, otherThread} {
		if err := s.Upsert(ctx, it); err != nil {
			t.Fatalf("Upsert %s: %v", it.ID, err)
		}
	}

	got, err := s.List(ctx, core.Filter{Channel: core.ChannelMail, Account: "cl", Thread: "t1"})
	if err != nil {
		t.Fatalf("List by thread: %v", err)
	}
	if len(got) != 1 || got[0].ID != inThread.ID {
		t.Fatalf("List by thread = %+v, want just %s", got, inThread.ID)
	}

	byThreadOnly, err := s.List(ctx, core.Filter{Thread: "t2"})
	if err != nil {
		t.Fatalf("List by thread only: %v", err)
	}
	if len(byThreadOnly) != 1 || byThreadOnly[0].ID != otherThread.ID {
		t.Fatalf("List by thread only = %+v, want just %s", byThreadOnly, otherThread.ID)
	}
}

func TestListQueryMatchesLikeWildcardsLiterally(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	items := map[string]string{
		"mail:cl:pct":       "descuento 50% hoy",
		"mail:cl:pctdecoy":  "descuento 500 hoy",
		"mail:cl:under":     "archivo a_b.txt",
		"mail:cl:underdeco": "archivo axb.txt",
		"mail:cl:slash":     `ruta C:\tmp`,
		"mail:cl:slashdeco": "ruta C:tmp",
	}
	for id, body := range items {
		it := sampleItem()
		it.ID = id
		it.Subject = ""
		it.Body = body
		it.Labels = nil
		if err := s.Upsert(ctx, it); err != nil {
			t.Fatalf("Upsert %s: %v", id, err)
		}
	}

	for _, tc := range []struct {
		query string
		want  string
	}{
		{"50%", "mail:cl:pct"},
		{"a_b", "mail:cl:under"},
		{`C:\`, "mail:cl:slash"},
	} {
		got, err := s.List(ctx, core.Filter{Query: tc.query})
		if err != nil {
			t.Fatalf("List query %q: %v", tc.query, err)
		}
		if len(got) != 1 || got[0].ID != tc.want {
			ids := make([]string, len(got))
			for i, it := range got {
				ids[i] = it.ID
			}
			t.Fatalf("List query %q = %v, want just %s", tc.query, ids, tc.want)
		}
	}
}

func TestCounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a := sampleItem()
	b := sampleItem()
	b.ID = "mail:cl:2"
	b.Unread = false
	c := sampleItem()
	c.ID = "whatsapp:personal:1"
	c.Channel = core.ChannelWhatsApp
	c.Account = "personal"

	for _, it := range []core.Item{a, b, c} {
		if err := s.Upsert(ctx, it); err != nil {
			t.Fatalf("Upsert %s: %v", it.ID, err)
		}
	}

	counts, err := s.Counts(ctx)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts[core.ChannelMail]["cl"] != 1 {
		t.Fatalf("mail/cl counts = %d, want 1", counts[core.ChannelMail]["cl"])
	}
	if counts[core.ChannelWhatsApp]["personal"] != 1 {
		t.Fatalf("whatsapp/personal counts = %d, want 1", counts[core.ChannelWhatsApp]["personal"])
	}
}

// Store must satisfy core.Store at compile time.
var _ core.Store = (*store.Store)(nil)

// preFromMeSchema is the items table exactly as it shipped before FromMe
// and the thread index (odd/tasks/conversation-view.md K1): no from_me
// column, no idx_items_thread. TestMigrationAddsFromMeColumnToExistingDatabase
// builds a database with this old shape to prove store.Open's migration
// is safe to run against it, without depending on store.Store's current
// schema constant ever staying reachable in its "old" form.
const preFromMeSchema = `
CREATE TABLE IF NOT EXISTS items (
	id           TEXT PRIMARY KEY,
	channel      TEXT NOT NULL,
	account      TEXT NOT NULL,
	thread       TEXT NOT NULL DEFAULT '',
	thread_name  TEXT NOT NULL DEFAULT '',
	from_id      TEXT NOT NULL DEFAULT '',
	from_name    TEXT NOT NULL DEFAULT '',
	to_json      TEXT NOT NULL DEFAULT '[]',
	subject      TEXT NOT NULL DEFAULT '',
	body         TEXT NOT NULL DEFAULT '',
	attachments_json TEXT NOT NULL DEFAULT '[]',
	unread       INTEGER NOT NULL DEFAULT 0,
	timestamp    INTEGER NOT NULL DEFAULT 0,
	meta_json    TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_items_channel_account ON items(channel, account);
CREATE INDEX IF NOT EXISTS idx_items_unread ON items(unread);
`

// TestMigrationAddsFromMeColumnToExistingDatabase proves the from_me
// migration (K1) is additive and idempotent on a database that predates
// it: store.Open must add the column (and the new thread index) to an
// already-populated items table without erroring or touching existing
// rows, and running it again (a second Open) must be a no-op, not a
// second failed ALTER TABLE.
func TestMigrationAddsFromMeColumnToExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bunker.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(preFromMeSchema); err != nil {
		t.Fatalf("create pre-FromMe schema: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO items (id, channel, account, thread, subject, unread, timestamp)
		VALUES ('mail:cl:1', 'mail', 'cl', 't1', 'legacy row', 1, 1000)`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw handle: %v", err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open on pre-FromMe database: %v", err)
	}
	got, err := s.Get(context.Background(), "mail:cl:1")
	if err != nil {
		t.Fatalf("Get legacy row after migration: %v", err)
	}
	if got.Subject != "legacy row" {
		t.Fatalf("Subject = %q, want %q (migration must not touch existing data)", got.Subject, "legacy row")
	}
	if got.FromMe {
		t.Fatalf("FromMe = true, want false (backfilled default) for a pre-migration row")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Idempotency: opening the already-migrated database again must not
	// fail with "duplicate column name" or similar.
	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("second Open (idempotency): %v", err)
	}
	t.Cleanup(func() { s2.Close() })
	if err := s2.Upsert(context.Background(), sampleItem()); err != nil {
		t.Fatalf("Upsert after second Open: %v", err)
	}
}

// TestThreadReturnsOldestFirstWithBeforeCursor exercises the (channel,
// account, thread, timestamp)-indexed Thread query: it must scope to one
// conversation, return oldest→newest, and page backward with "before"
// strictly excluding the cursor timestamp itself.
func TestThreadReturnsOldestFirstWithBeforeCursor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	thread := "5511999999999@s.whatsapp.net"
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	mk := func(id string, offsetMin int) core.Item {
		return core.Item{
			ID: id, Channel: core.ChannelWhatsApp, Account: "personal", Thread: thread,
			Body: id, Timestamp: base.Add(time.Duration(offsetMin) * time.Minute),
		}
	}
	items := []core.Item{mk("w:1", 0), mk("w:2", 1), mk("w:3", 2), mk("w:4", 3)}
	otherThread := mk("w:other", 1)
	otherThread.ID = "w:other"
	otherThread.Thread = "5511888888888@s.whatsapp.net"
	for _, it := range append(items, otherThread) {
		if err := s.Upsert(ctx, it); err != nil {
			t.Fatalf("Upsert %s: %v", it.ID, err)
		}
	}

	all, err := s.Thread(ctx, core.Filter{Channel: core.ChannelWhatsApp, Account: "personal", Thread: thread}, time.Time{}, 10)
	if err != nil {
		t.Fatalf("Thread: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("Thread() len = %d, want 4", len(all))
	}
	wantOrder := []string{"w:1", "w:2", "w:3", "w:4"}
	for i, want := range wantOrder {
		if all[i].ID != want {
			t.Fatalf("Thread()[%d].ID = %q, want %q (oldest→newest)", i, all[i].ID, want)
		}
	}

	// Page backward from w:3's timestamp: strictly before it excludes
	// w:3 itself, leaving w:1 and w:2.
	before := base.Add(2 * time.Minute)
	page, err := s.Thread(ctx, core.Filter{Channel: core.ChannelWhatsApp, Account: "personal", Thread: thread}, before, 10)
	if err != nil {
		t.Fatalf("Thread (before): %v", err)
	}
	if len(page) != 2 || page[0].ID != "w:1" || page[1].ID != "w:2" {
		t.Fatalf("Thread(before=w:3.ts) = %+v, want [w:1, w:2]", page)
	}

	limited, err := s.Thread(ctx, core.Filter{Channel: core.ChannelWhatsApp, Account: "personal", Thread: thread}, time.Time{}, 2)
	if err != nil {
		t.Fatalf("Thread (limit 2): %v", err)
	}
	if len(limited) != 2 || limited[0].ID != "w:3" || limited[1].ID != "w:4" {
		t.Fatalf("Thread(limit=2) = %+v, want the 2 most recent, oldest→newest [w:3, w:4]", limited)
	}
}

// rawUserVersion reads PRAGMA user_version directly over an independent
// connection, so these tests assert on the on-disk schema version
// without depending on store.Store exposing it itself.
func rawUserVersion(t *testing.T, path string) int {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()
	var v int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	return v
}

// TestOpenStampsFreshDatabaseWithCurrentSchemaVersion proves a brand new
// database ends Open at the latest schema version, not 0.
func TestOpenStampsFreshDatabaseWithCurrentSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bunker.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	want := store.CurrentSchemaVersion()
	if got := rawUserVersion(t, path); got != want {
		t.Fatalf("user_version = %d, want %d", got, want)
	}
}

// TestOpenStampsLegacyUnversionedDatabaseFromItsColumns proves an
// existing database that predates schema versioning (PRAGMA user_version
// defaults to 0 on any database that never set it) is detected by its
// current columns — here, pre-FromMe — and stamped with the matching
// (latest) version after every migration adds what it was missing,
// without touching its existing row.
func TestOpenStampsLegacyUnversionedDatabaseFromItsColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bunker.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(preFromMeSchema); err != nil {
		t.Fatalf("create pre-FromMe schema: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO items (id, channel, account, thread, subject, unread, timestamp)
		VALUES ('mail:cl:1', 'mail', 'cl', 't1', 'legacy row', 1, 1000)`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw handle: %v", err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open on unversioned legacy database: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	want := store.CurrentSchemaVersion()
	if got := rawUserVersion(t, path); got != want {
		t.Fatalf("user_version after stamping = %d, want %d", got, want)
	}
	got, err := s.Get(context.Background(), "mail:cl:1")
	if err != nil {
		t.Fatalf("Get legacy row: %v", err)
	}
	if got.Subject != "legacy row" {
		t.Fatalf("Subject = %q, want %q (stamping must not rewrite data)", got.Subject, "legacy row")
	}
}

// TestOpenIsIdempotentOnAnAlreadyStampedDatabase proves reopening a
// database already at the current version neither errors nor changes
// its version (no migration re-applies).
func TestOpenIsIdempotentOnAnAlreadyStampedDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bunker.db")

	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { s2.Close() })

	want := store.CurrentSchemaVersion()
	if got := rawUserVersion(t, path); got != want {
		t.Fatalf("user_version after reopen = %d, want %d (unchanged)", got, want)
	}
}

// TestOpenRefusesDatabaseWithFutureSchemaVersion proves a database
// stamped with a schema version newer than this binary understands
// fails at Open with a clear error, instead of silently operating on an
// unrecognized shape.
func TestOpenRefusesDatabaseWithFutureSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bunker.db")

	// A normal Open first brings the database to today's real schema
	// (version 1); only its version pragma is then bumped past what
	// this binary knows, so the refusal is proven against an otherwise
	// perfectly valid, fully migrated database.
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("initial Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 9999`); err != nil {
		t.Fatalf("set future user_version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw handle: %v", err)
	}

	_, err = store.Open(path)
	if err == nil {
		t.Fatal("Open on a future-versioned database: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "9999") {
		t.Fatalf("Open error = %q, want it to mention the offending version 9999", err.Error())
	}
}

// TestOpenCreatesPrivateFileAndDir covers the live-link finding
// (2026-09-25): the state DB was created 0644 with a 0755-ish directory,
// readable by every other local user. Open must leave the DB file at
// 0600 and its (possibly newly created) parent directory at 0700,
// including any -wal/-shm sidecar SQLite's WAL mode creates alongside it.
func TestOpenCreatesPrivateFileAndDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode bits are not meaningful on Windows")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "nested", "state")
	dbPath := filepath.Join(dir, "bunker.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Force a write, so a WAL-mode driver has something to flush and a
	// chance to create -wal/-shm before this test inspects them.
	if err := s.Upsert(context.Background(), sampleItem()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("state dir mode = %o, want 0700", perm)
	}

	fileInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat db file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("db file mode = %o, want 0600", perm)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		info, err := os.Stat(dbPath + suffix)
		if os.IsNotExist(err) {
			continue // this driver/session did not create this sidecar; nothing to check.
		}
		if err != nil {
			t.Fatalf("Stat %s: %v", suffix, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 0600", suffix, perm)
		}
	}
}

// TestEditItemReplacesBodyAndSetsEditedFlag pins S2's edit model: the
// stored body is replaced and Edited becomes true, every other field
// (attachments, labels) is preserved.
func TestEditItemReplacesBodyAndSetsEditedFlag(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := sampleItem()
	item.ID = "whatsapp:personal:1"
	item.Channel = core.ChannelWhatsApp
	item.Account = "personal"
	if err := s.Upsert(ctx, item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := s.EditItem(ctx, item.ID, "cuerpo corregido"); err != nil {
		t.Fatalf("EditItem: %v", err)
	}

	got, err := s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Body != "cuerpo corregido" {
		t.Fatalf("Body = %q, want %q", got.Body, "cuerpo corregido")
	}
	if !got.Edited {
		t.Fatal("Edited = false, want true after EditItem")
	}
	if len(got.Attachments) != 1 || len(got.Labels) != 2 {
		t.Fatalf("EditItem must preserve other fields: attachments=%+v labels=%+v", got.Attachments, got.Labels)
	}
}

// TestEditItemUnknownIDReturnsErrNotFound mirrors MarkRead/Delete's own
// no-op-error contract for an id EditItem has never seen.
func TestEditItemUnknownIDReturnsErrNotFound(t *testing.T) {
	s := openTestStore(t)
	if err := s.EditItem(context.Background(), "whatsapp:personal:missing", "x"); err == nil {
		t.Fatal("EditItem on an unknown id: expected an error, got nil")
	}
}

// TestRevokeItemClearsBodyKeepsRowAndSetsDeletedFlag pins S2's revoke
// model: Body is cleared, Deleted becomes true, and the row itself (and
// its place in Thread's history) survives — unlike Delete.
func TestRevokeItemClearsBodyKeepsRowAndSetsDeletedFlag(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := sampleItem()
	item.ID = "whatsapp:personal:1"
	item.Channel = core.ChannelWhatsApp
	item.Account = "personal"
	if err := s.Upsert(ctx, item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := s.RevokeItem(ctx, item.ID); err != nil {
		t.Fatalf("RevokeItem: %v", err)
	}

	got, err := s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get after revoke: %v (row must survive, unlike Delete)", err)
	}
	if got.Body != "" {
		t.Fatalf("Body = %q, want empty after RevokeItem", got.Body)
	}
	if !got.Deleted {
		t.Fatal("Deleted = false, want true after RevokeItem")
	}
}

// TestRevokeItemUnknownIDReturnsErrNotFound mirrors EditItem/Delete's
// own no-op-error contract.
func TestRevokeItemUnknownIDReturnsErrNotFound(t *testing.T) {
	s := openTestStore(t)
	if err := s.RevokeItem(context.Background(), "whatsapp:personal:missing"); err == nil {
		t.Fatal("RevokeItem on an unknown id: expected an error, got nil")
	}
}

// TestSetReactionStoresAndGetReturnsIt proves a reaction round-trips
// through Get.
func TestSetReactionStoresAndGetReturnsIt(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := sampleItem()
	item.ID = "whatsapp:personal:1"
	item.Channel = core.ChannelWhatsApp
	item.Account = "personal"
	if err := s.Upsert(ctx, item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := s.SetReaction(ctx, item.ID, core.Reaction{Sender: "alice@s.whatsapp.net", Emoji: "👍"}); err != nil {
		t.Fatalf("SetReaction: %v", err)
	}

	got, err := s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Reactions) != 1 || got.Reactions[0] != (core.Reaction{Sender: "alice@s.whatsapp.net", Emoji: "👍"}) {
		t.Fatalf("Reactions = %+v, want one alice/👍 reaction", got.Reactions)
	}
}

// TestSetReactionFromSameSenderReplacesThePrevious proves a newer
// reaction from the same sender replaces its previous one instead of
// appending a second row.
func TestSetReactionFromSameSenderReplacesThePrevious(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := sampleItem()
	item.ID = "whatsapp:personal:1"
	item.Channel = core.ChannelWhatsApp
	item.Account = "personal"
	if err := s.Upsert(ctx, item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := s.SetReaction(ctx, item.ID, core.Reaction{Sender: "alice@s.whatsapp.net", Emoji: "👍"}); err != nil {
		t.Fatalf("first SetReaction: %v", err)
	}
	if err := s.SetReaction(ctx, item.ID, core.Reaction{Sender: "alice@s.whatsapp.net", Emoji: "❤️"}); err != nil {
		t.Fatalf("second SetReaction: %v", err)
	}

	got, err := s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Reactions) != 1 || got.Reactions[0].Emoji != "❤️" {
		t.Fatalf("Reactions = %+v, want a single replaced ❤️ reaction", got.Reactions)
	}
}

// TestSetReactionWithEmptyEmojiRemovesIt proves an empty emoji removes
// that sender's reaction rather than storing an empty one.
func TestSetReactionWithEmptyEmojiRemovesIt(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := sampleItem()
	item.ID = "whatsapp:personal:1"
	item.Channel = core.ChannelWhatsApp
	item.Account = "personal"
	if err := s.Upsert(ctx, item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := s.SetReaction(ctx, item.ID, core.Reaction{Sender: "alice@s.whatsapp.net", Emoji: "👍"}); err != nil {
		t.Fatalf("SetReaction: %v", err)
	}

	if err := s.SetReaction(ctx, item.ID, core.Reaction{Sender: "alice@s.whatsapp.net", Emoji: ""}); err != nil {
		t.Fatalf("SetReaction (remove): %v", err)
	}

	got, err := s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Reactions) != 0 {
		t.Fatalf("Reactions = %+v, want none after removing the only one", got.Reactions)
	}
}

// TestSetReactionUnknownItemReturnsErrNotFound proves reacting to an id
// the store has never seen is a no-op error, not a silent orphan row.
func TestSetReactionUnknownItemReturnsErrNotFound(t *testing.T) {
	s := openTestStore(t)
	err := s.SetReaction(context.Background(), "whatsapp:personal:missing", core.Reaction{Sender: "alice@s.whatsapp.net", Emoji: "👍"})
	if err == nil {
		t.Fatal("SetReaction on an unknown item: expected an error, got nil")
	}
}

// TestMigrationV2AddsEditedDeletedAndReactionsToExistingDatabase proves
// migrateV2 is additive and idempotent on a database that predates it
// (already at schema version 1: from_me exists, edited/deleted/reactions
// do not), the same way TestMigrationAddsFromMeColumnToExistingDatabase
// proved migrateV1 against a pre-FromMe database.
func TestMigrationV2AddsEditedDeletedAndReactionsToExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bunker.db")

	// Build a version-1 database directly: today's schema (which already
	// includes from_me) stamped at user_version 1, predating migrateV2.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(preFromMeSchema); err != nil {
		t.Fatalf("create pre-FromMe schema: %v", err)
	}
	if _, err := raw.Exec(`ALTER TABLE items ADD COLUMN from_me INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("add from_me column: %v", err)
	}
	// A real version-1 database also has labels/cursors (migrateV1 execs
	// the full schema, not just the items table), which preFromMeSchema
	// alone omits.
	if _, err := raw.Exec(`
		CREATE TABLE IF NOT EXISTS labels (
			item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
			label   TEXT NOT NULL,
			PRIMARY KEY (item_id, label)
		);
		CREATE TABLE IF NOT EXISTS cursors (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	`); err != nil {
		t.Fatalf("create labels/cursors tables: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO items (id, channel, account, thread, subject, unread, from_me, timestamp)
		VALUES ('whatsapp:personal:1', 'whatsapp', 'personal', 't1', 'legacy row', 1, 0, 1000)`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatalf("stamp version 1: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw handle: %v", err)
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open on a version-1 database: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if got, want := rawUserVersion(t, path), store.CurrentSchemaVersion(); got != want {
		t.Fatalf("user_version after migrating = %d, want %d", got, want)
	}

	got, err := s.Get(context.Background(), "whatsapp:personal:1")
	if err != nil {
		t.Fatalf("Get legacy row after migrateV2: %v", err)
	}
	if got.Subject != "legacy row" {
		t.Fatalf("Subject = %q, want %q (migration must not touch existing data)", got.Subject, "legacy row")
	}
	if got.Edited || got.Deleted {
		t.Fatalf("Edited/Deleted = %v/%v, want both false (backfilled defaults) for a pre-migration row", got.Edited, got.Deleted)
	}

	// The reactions table must also exist and accept a write.
	if err := s.SetReaction(context.Background(), "whatsapp:personal:1", core.Reaction{Sender: "bob@s.whatsapp.net", Emoji: "👍"}); err != nil {
		t.Fatalf("SetReaction after migrateV2: %v", err)
	}
}
