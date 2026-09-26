package store_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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
