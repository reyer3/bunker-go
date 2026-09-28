package mail

import (
	"context"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
)

func rawSearchMessage(from, subject string) string {
	return "From: " + from + "\r\nSubject: " + subject + "\r\nDate: Fri, 25 Sep 2026 10:00:00 +0000\r\n\r\nbody\r\n"
}

// TestAdapterSearchMatchesFromFilter: mail-history H3's core scenario —
// --from narrows an IMAP UID SEARCH to matching messages only, and the
// hit is upserted into the store (read-only).
func TestAdapterSearchMatchesFromFilter(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawSearchMessage("alice@x", "hello"))
	appendMessage(t, addr, "INBOX", rawSearchMessage("bob@x", "hi"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	st := openTestStore(t)

	items, err := adapter.Search(context.Background(), st, core.SearchCriteria{From: "alice@x"})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if len(items) != 1 || items[0].From.ID != "alice@x" {
		t.Fatalf("items = %+v, want exactly alice@x's message", items)
	}

	stored, err := st.Get(context.Background(), items[0].ID)
	if err != nil {
		t.Fatalf("store.Get after Search: %v, want the hit upserted", err)
	}
	if stored.Subject != "hello" {
		t.Errorf("stored.Subject = %q, want hello", stored.Subject)
	}
}

func TestAdapterSearchMatchesSubjectFilter(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawSearchMessage("alice@x", "invoice for July"))
	appendMessage(t, addr, "INBOX", rawSearchMessage("alice@x", "lunch plans"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	st := openTestStore(t)

	items, err := adapter.Search(context.Background(), st, core.SearchCriteria{Subject: "invoice"})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if len(items) != 1 || items[0].Subject != "invoice for July" {
		t.Fatalf("items = %+v, want exactly the invoice message", items)
	}
}

func TestAdapterSearchRespectsLimit(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawSearchMessage("a@x", "one"))
	appendMessage(t, addr, "INBOX", rawSearchMessage("a@x", "two"))
	appendMessage(t, addr, "INBOX", rawSearchMessage("a@x", "three"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	st := openTestStore(t)

	items, err := adapter.Search(context.Background(), st, core.SearchCriteria{Limit: 2})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2 (Limit: 2)", len(items))
	}
	// Newest first: uid 3 ("three") then uid 2 ("two").
	if items[0].Subject != "three" || items[1].Subject != "two" {
		t.Fatalf("items = %+v, want [three, two] (newest UID first)", items)
	}
}

func TestAdapterSearchNoMatchReturnsEmpty(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawSearchMessage("a@x", "hello"))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	st := openTestStore(t)

	items, err := adapter.Search(context.Background(), st, core.SearchCriteria{From: "nobody@nowhere"})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %+v, want none", items)
	}
}

// TestAdapterSearchNeverMarksSeen: Search's header fetch must use
// BODY.PEEK like every other read-only mail path.
func TestAdapterSearchNeverMarksSeen(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", multipartMessage)

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	st := openTestStore(t)

	if _, err := adapter.Search(context.Background(), st, core.SearchCriteria{From: "alice@example.org"}); err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if messageHasFlag(t, addr, "INBOX", "<multi@example.org>", imap.FlagSeen) {
		t.Error("server marked the message \\Seen; Search must use BODY.PEEK")
	}
}
