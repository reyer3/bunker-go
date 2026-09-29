package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// matchNow anchors relative dates in these tests.
var matchNow = time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)

// matchIDs parses q with the query language and returns the ids List
// finds for it, sorted.
func matchIDs(t *testing.T, s *store.Store, q string) []string {
	t.Helper()
	parsed, err := core.ParseQueryAt(q, matchNow)
	if err != nil {
		t.Fatalf("ParseQueryAt(%q): %v", q, err)
	}
	got, err := s.List(context.Background(), core.Filter{Match: &parsed})
	if err != nil {
		t.Fatalf("List match %q: %v", q, err)
	}
	ids := make([]string, len(got))
	for i, it := range got {
		ids[i] = it.ID
	}
	slices.Sort(ids)
	return ids
}

func assertMatch(t *testing.T, s *store.Store, q string, want ...string) {
	t.Helper()
	slices.Sort(want)
	if want == nil {
		want = []string{}
	}
	if got := matchIDs(t, s, q); !slices.Equal(got, want) {
		t.Errorf("match %q = %v, want %v", q, got, want)
	}
}

// matchFixtures is a small inbox where each item differs from the others
// in the fields the operators address, so every assertion shows which
// field matched.
func matchFixtures(t *testing.T, s *store.Store) {
	t.Helper()
	invoice := ftsItem("mail:cl:invoice")
	invoice.From = core.Address{ID: "ana@example.com", Name: "Ana María"}
	invoice.To = []core.Address{{ID: "equipo@example.com", Name: "Equipo"}}
	invoice.Subject = "Factura de marzo"
	invoice.Body = "Adjunto la orden de compra"
	invoice.Attachments = []core.Attachment{{Name: "factura.pdf"}}
	invoice.Unread = true
	invoice.Labels = []string{"Important"}
	invoice.Meta = map[string]string{"folder": "INBOX"}
	invoice.Timestamp = time.Date(2026, 3, 28, 9, 0, 0, 0, time.UTC)

	report := ftsItem("mail:cl:report")
	report.From = core.Address{ID: "luis@example.com", Name: "Luis"}
	report.To = []core.Address{{ID: "ana@example.com", Name: "Ana María"}}
	report.Subject = "Informe semanal"
	report.Body = "compra de equipos, orden pendiente"
	report.Meta = map[string]string{"folder": "Sent"}
	report.Timestamp = time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)

	archived := ftsItem("mail:work:archived")
	archived.Account = "work"
	archived.From = core.Address{ID: "noreply@example.com", Name: "Sistema"}
	archived.Subject = "100% listo_ok"
	archived.Body = "sin novedad"
	archived.Meta = map[string]string{"folder": "Archive"}
	archived.Timestamp = time.Date(2025, 12, 1, 9, 0, 0, 0, time.UTC)

	chat := core.Item{
		ID: "whatsapp:personal:chat", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "chat",
		From:      core.Address{ID: "51911@s.whatsapp.net", Name: "Ana"},
		Body:      "te mando la factura",
		Unread:    true,
		Timestamp: time.Date(2026, 3, 30, 9, 0, 0, 0, time.UTC),
	}
	upsertAll(t, s, invoice, report, archived, chat)
}

func TestListMatchEachOperator(t *testing.T) {
	s := openTestStore(t)
	matchFixtures(t, s)
	const (
		invoice  = "mail:cl:invoice"
		report   = "mail:cl:report"
		archived = "mail:work:archived"
		chat     = "whatsapp:personal:chat"
	)

	assertMatch(t, s, "", invoice, report, archived, chat)
	// Free text covers every indexed field, prefix-matched.
	assertMatch(t, s, "factu", invoice, chat)
	assertMatch(t, s, "orden compra", invoice, report)
	// A quoted phrase needs the words together and in order.
	assertMatch(t, s, `"orden de compra"`, invoice)
	assertMatch(t, s, `"compra orden"`)

	// from: is the sender's name or address, never the recipients.
	assertMatch(t, s, "from:ana", invoice, chat)
	assertMatch(t, s, "from:ana@example.com", invoice)
	assertMatch(t, s, `from:"Ana María"`, invoice)
	assertMatch(t, s, "from:maria", invoice)
	// to: is the recipients, never the sender.
	assertMatch(t, s, "to:ana", report)
	assertMatch(t, s, "to:equipo", invoice)
	// subject: is the subject only: "compra" is in bodies, not subjects.
	assertMatch(t, s, "subject:informe", report)
	assertMatch(t, s, "subject:compra")

	assertMatch(t, s, "is:unread", invoice, chat)
	assertMatch(t, s, "is:read", report, archived)
	assertMatch(t, s, "has:attachment", invoice)
	assertMatch(t, s, "in:inbox", invoice)
	assertMatch(t, s, "in:INBOX", invoice)
	assertMatch(t, s, "in:sent", report)
	assertMatch(t, s, "channel:whatsapp", chat)
	assertMatch(t, s, "channel:mail", invoice, report, archived)
	assertMatch(t, s, "account:work", archived)
	assertMatch(t, s, "label:important", invoice)
	assertMatch(t, s, "label:vip")

	assertMatch(t, s, "after:2026-03-28", invoice, chat)
	assertMatch(t, s, "before:2026-03-28", report, archived)
	assertMatch(t, s, "after:2026-01-01 before:2026-03-01", report)
	assertMatch(t, s, "after:7d", invoice, chat)
	assertMatch(t, s, "after:3m", invoice, report, chat)
	assertMatch(t, s, "before:3m", archived)
}

func TestListMatchNegationAndCombinations(t *testing.T) {
	s := openTestStore(t)
	matchFixtures(t, s)

	assertMatch(t, s, "-factura", "mail:cl:report", "mail:work:archived")
	assertMatch(t, s, "-is:unread", "mail:cl:report", "mail:work:archived")
	assertMatch(t, s, "-has:attachment", "mail:cl:report", "mail:work:archived", "whatsapp:personal:chat")
	// Items with no folder at all (chats) are "not in the inbox" too.
	assertMatch(t, s, "-in:inbox", "mail:cl:report", "mail:work:archived", "whatsapp:personal:chat")
	assertMatch(t, s, "-channel:mail", "whatsapp:personal:chat")
	assertMatch(t, s, "-label:important", "mail:cl:report", "mail:work:archived", "whatsapp:personal:chat")
	assertMatch(t, s, `-"orden de compra" compra`, "mail:cl:report")

	assertMatch(t, s, "from:ana is:unread channel:mail has:attachment", "mail:cl:invoice")
	assertMatch(t, s, "from:ana -channel:mail", "whatsapp:personal:chat")
	assertMatch(t, s, "factura -from:ana")
	assertMatch(t, s, "compra -in:inbox after:2026-01-01", "mail:cl:report")
}

func TestListMatchKeepsPlainFilterFields(t *testing.T) {
	s := openTestStore(t)
	matchFixtures(t, s)
	q, err := core.ParseQueryAt("factura", matchNow)
	if err != nil {
		t.Fatal(err)
	}
	unread := true
	got, err := s.List(context.Background(), core.Filter{Channel: core.ChannelMail, Unread: &unread, Match: &q})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "mail:cl:invoice" {
		t.Fatalf("got %v", got)
	}
}

// TestListMatchNeverInjects runs SQL and FTS5 syntax through every text
// operator: each must be matched as text or literally, never error and
// never widen the result.
func TestListMatchNeverInjects(t *testing.T) {
	s := openTestStore(t)
	matchFixtures(t, s)

	for _, v := range []string{
		`'`, `''`, `' OR 1=1 --`, `%`, `_`, `\`, `%'; DROP TABLE items; --`,
		`*`, `^`, `NEAR`, `OR`, `NOT`, `{subject}`, `body:x`, `)`, `(`,
		`a"b`, `"`, `x" OR "y`,
	} {
		for _, op := range []string{"", "from:", "to:", "subject:", "in:", "label:", "account:", "-", "-from:"} {
			// A bare " opens a quote the parser rejects, so it only
			// travels inside a quoted value.
			q := op + `"` + v + `"`
			if v == `"` || v == `a"b` || v == `x" OR "y` {
				q = op + v
				if _, err := core.ParseQueryAt(q, matchNow); err != nil {
					continue
				}
			}
			parsed, err := core.ParseQueryAt(q, matchNow)
			if err != nil {
				t.Errorf("ParseQueryAt(%q): %v", q, err)
				continue
			}
			if _, err := s.List(context.Background(), core.Filter{Match: &parsed}); err != nil {
				t.Errorf("List %q: %v", q, err)
			}
		}
	}

	// The table survived, and wildcards and quotes match literally.
	assertMatch(t, s, `"%"`, "mail:work:archived")
	assertMatch(t, s, `"_"`, "mail:work:archived")
	assertMatch(t, s, `subject:"%"`, "mail:work:archived")
	assertMatch(t, s, `from:"%"`)
	assertMatch(t, s, `"' OR 1=1 --"`)
	assertMatch(t, s, `in:"' OR '1'='1"`)
	assertMatch(t, s, `label:"%"`)
	// FTS5 operators and column filters are words, not syntax: they
	// neither error nor escape the from: scope.
	assertMatch(t, s, `from:"x OR factura"`)
	assertMatch(t, s, `from:"{subject}:factura"`)
	assertMatch(t, s, `subject:"informe" "NEAR(orden compra)"`)
}

func TestListMatchRejectsAnUnknownField(t *testing.T) {
	s := openTestStore(t)
	q := core.Query{Terms: []core.QueryTerm{{Field: "bogus", Value: "x"}}}
	_, err := s.List(context.Background(), core.Filter{Match: &q})
	if !errors.Is(err, core.ErrInvalidQuery) {
		t.Fatalf("err = %v, want ErrInvalidQuery", err)
	}
}

// TestListPageIsStableOverEqualTimestamps pages through items that all
// share one timestamp: the id tiebreak must return each exactly once, in
// the same order List uses, with no cursor after the last page.
func TestListPageIsStableOverEqualTimestamps(t *testing.T) {
	s := openTestStore(t)
	same := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	var want []string
	for i := range 7 {
		it := ftsItem(fmt.Sprintf("mail:cl:%02d", i))
		it.Timestamp = same
		upsertAll(t, s, it)
	}
	newer := ftsItem("mail:cl:newer")
	newer.Timestamp = same.Add(time.Second)
	older := ftsItem("mail:cl:older")
	older.Timestamp = same.Add(-time.Second)
	upsertAll(t, s, newer, older)

	all, err := s.List(context.Background(), core.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range all {
		want = append(want, it.ID)
	}
	if want[0] != "mail:cl:newer" || want[len(want)-1] != "mail:cl:older" || want[1] != "mail:cl:06" {
		t.Fatalf("List order = %v, want newest first with ids descending on ties", want)
	}

	for _, size := range []int{1, 2, 3, 4, 9, 10} {
		var got []string
		cursor := ""
		for pages := 0; ; pages++ {
			if pages > len(want) {
				t.Fatalf("size %d: paging did not terminate", size)
			}
			page, err := s.ListPage(context.Background(), core.Filter{Limit: size, Cursor: cursor})
			if err != nil {
				t.Fatalf("size %d: ListPage: %v", size, err)
			}
			if len(page.Items) > size {
				t.Fatalf("size %d: page of %d", size, len(page.Items))
			}
			for _, it := range page.Items {
				got = append(got, it.ID)
			}
			if page.NextCursor == "" {
				break
			}
			if len(page.Items) != size {
				t.Fatalf("size %d: a short page (%d) still has a cursor", size, len(page.Items))
			}
			cursor = page.NextCursor
		}
		if !slices.Equal(got, want) {
			t.Errorf("size %d: pages = %v, want %v", size, got, want)
		}
	}
}

func TestListPageAppliesMatchAndNoLimitReturnsAll(t *testing.T) {
	s := openTestStore(t)
	matchFixtures(t, s)
	q, err := core.ParseQueryAt("channel:mail", matchNow)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListPage(context.Background(), core.Filter{Match: &q, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != "mail:cl:invoice" || page.NextCursor == "" {
		t.Fatalf("page 1 = %+v", page)
	}
	page, err = s.ListPage(context.Background(), core.Filter{Match: &q, Limit: 2, Cursor: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "mail:work:archived" || page.NextCursor != "" {
		t.Fatalf("page 2 = %+v", page)
	}

	page, err = s.ListPage(context.Background(), core.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 4 || page.NextCursor != "" {
		t.Fatalf("unlimited page = %d items, cursor %q", len(page.Items), page.NextCursor)
	}

	empty, err := s.ListPage(context.Background(), core.Filter{Channel: core.ChannelMatrix})
	if err != nil || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("an empty page is an empty list, not null: %+v, %v", empty, err)
	}
}

func TestListPageRejectsABadCursor(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.ListPage(context.Background(), core.Filter{Cursor: "not-a-cursor!"}); err == nil {
		t.Fatal("want an error for a malformed cursor")
	}
}
