package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// Example data only: a person whose chat ingest keys on pn, and the LID
// their bare number resolved to when bunker sent them messages.
const (
	waPN      = "56900000001@s.whatsapp.net"
	waLID     = "100000000000001@lid"
	waDigits  = "56900000001"
	waAccount = "personal"
)

func waID(account, chat, msg string) string {
	return fmt.Sprintf("whatsapp:%s:%s/%s", account, chat, msg)
}

// splitThreadFixture is a store as bunker left it before sent items took
// the adapter-resolved chat: sends to a bare number grouped under the
// digits as typed (with or without '+'), keyed on the LID chat, one of
// them also stored by its echo under the canonical chat.
func splitThreadFixture() []core.Item {
	wa := func(id, thread, name string, ts int64, fromMe bool) core.Item {
		return core.Item{ID: id, Channel: core.ChannelWhatsApp, Account: waAccount, Thread: thread, ThreadName: name, Body: "hola " + id, FromMe: fromMe, Timestamp: time.Unix(ts, 0)}
	}
	sentA := wa(waID(waAccount, waLID, "SENT-A"), waDigits, "", 20, true)
	sentA.Labels = []string{"importante"}
	return []core.Item{
		wa(waID(waAccount, waPN, "IN-1"), waPN, "Ana Ejemplo", 10, false),
		sentA,
		wa(waID(waAccount, waLID, "SENT-B"), "+"+waDigits, "", 30, true),
		// SENT-C exists twice: the optimistic sent copy in the split
		// thread, and the richer echo ingest stored under the PN chat.
		wa(waID(waAccount, waLID, "SENT-C"), waDigits, "", 40, true),
		{ID: waID(waAccount, waPN, "SENT-C"), Channel: core.ChannelWhatsApp, Account: waAccount, Thread: waPN, ThreadName: "Ana Ejemplo", Body: "hola eco", FromMe: true, Timestamp: time.Unix(40, 0),
			Attachments: []core.Attachment{{Name: "foto.jpg", MIME: "image/jpeg", Size: 4, Ref: "/v/ref"}}},
		// Another account's split thread has no named chat to inherit.
		{ID: waID("work", waLID, "SENT-W"), Channel: core.ChannelWhatsApp, Account: "work", Thread: waDigits, Body: "trabajo", FromMe: true, Timestamp: time.Unix(50, 0)},
		// Never touched: groups, items without a thread, other channels,
		// and threads that are not only digits.
		wa(waID(waAccount, "120363000000000001@g.us", "G-1"), "120363000000000001@g.us", "Grupo", 60, false),
		wa(waID(waAccount, waPN, "NO-THREAD"), "", "", 70, false),
		wa(waID(waAccount, waPN, "MIXED"), "5690abc", "", 75, false),
		{ID: "matrix:work:!r:example.org/$1", Channel: core.ChannelMatrix, Account: "work", Thread: "12345", Body: "matrix", Timestamp: time.Unix(80, 0)},
	}
}

// openAtVersion opens the store at path after stamping it back to
// version, so the next Open re-runs every migration above it.
func openAtVersion(t *testing.T, path string, version int) *store.Store {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		t.Fatalf("stamp version %d: %v", version, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// snapshot renders every item's id, thread, name, labels and reactions,
// so two migrations can be compared for a no-op.
func snapshot(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	items, err := s.List(ctx, core.Filter{Limit: 1000})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var lines []string
	for _, it := range items {
		full, err := s.Get(ctx, it.ID)
		if err != nil {
			t.Fatalf("Get %s: %v", it.ID, err)
		}
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%v|%v", full.ID, full.Thread, full.ThreadName, full.Labels, full.Reactions))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func TestMigrationMergesWhatsAppThreadsSplitByBareNumberSends(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bunker.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	upsertAll(t, s, splitThreadFixture()...)
	if err := s.SetReaction(ctx, waID(waAccount, waLID, "SENT-C"), core.Reaction{Sender: "me", Emoji: "👍"}); err != nil {
		t.Fatalf("SetReaction: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = openAtVersion(t, path, 6)
	defer s.Close()
	if got, want := rawUserVersion(t, path), store.CurrentSchemaVersion(); got != want {
		t.Fatalf("user_version = %d, want %d", got, want)
	}

	thread, err := s.Thread(ctx, core.Filter{Channel: core.ChannelWhatsApp, Account: waAccount, Thread: waPN}, time.Time{}, 100)
	if err != nil {
		t.Fatalf("Thread: %v", err)
	}
	var ids []string
	for _, it := range thread {
		ids = append(ids, it.ID)
		if it.ThreadName != "Ana Ejemplo" {
			t.Errorf("%s ThreadName = %q, want the chat's name", it.ID, it.ThreadName)
		}
	}
	want := []string{
		waID(waAccount, waPN, "IN-1"),
		waID(waAccount, waLID, "SENT-A"),
		waID(waAccount, waLID, "SENT-B"),
		waID(waAccount, waPN, "SENT-C"),
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("canonical chat = %v, want %v (one copy of SENT-C, the echo)", ids, want)
	}

	sentA, err := s.Get(ctx, waID(waAccount, waLID, "SENT-A"))
	if err != nil {
		t.Fatalf("Get SENT-A: %v", err)
	}
	if !slices.Equal(sentA.Labels, []string{"importante"}) {
		t.Errorf("SENT-A labels = %v, want kept", sentA.Labels)
	}
	echo, err := s.Get(ctx, waID(waAccount, waPN, "SENT-C"))
	if err != nil {
		t.Fatalf("Get echo: %v", err)
	}
	if len(echo.Attachments) != 1 || len(echo.Reactions) != 1 || echo.Reactions[0].Emoji != "👍" {
		t.Errorf("echo = attachments %v reactions %v, want its attachment and the dropped copy's reaction", echo.Attachments, echo.Reactions)
	}

	// The renamed items are findable by the chat's name.
	if got := queryIDs(t, s, "Ejemplo"); !slices.Contains(got, waID(waAccount, waLID, "SENT-A")) {
		t.Errorf("search by chat name = %v, want it to include SENT-A", got)
	}

	for id, wantThread := range map[string]string{
		waID("work", waLID, "SENT-W"):                     "56900000001@s.whatsapp.net",
		waID(waAccount, "120363000000000001@g.us", "G-1"): "120363000000000001@g.us",
		waID(waAccount, waPN, "NO-THREAD"):                "",
		waID(waAccount, waPN, "MIXED"):                    "5690abc",
		"matrix:work:!r:example.org/$1":                   "12345",
	} {
		got, err := s.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if got.Thread != wantThread {
			t.Errorf("%s Thread = %q, want %q", id, got.Thread, wantThread)
		}
	}
	if got, err := s.Get(ctx, waID("work", waLID, "SENT-W")); err != nil || got.ThreadName != "" {
		t.Errorf("work item ThreadName = %q (err %v), want empty: its chat has no name", got.ThreadName, err)
	}

	// Running the migration again changes nothing.
	before := snapshot(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openAtVersion(t, path, 6)
	defer s2.Close()
	if after := snapshot(t, s2); after != before {
		t.Fatalf("second migration changed the store:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
