package tui

import (
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// mailThread builds one Mail inboxGroup (a single conversation) with one
// item, for groupBySender fixtures. Fictional addresses/names only.
func mailThread(id, account, thread, fromAddr, fromName string, at time.Time) inboxGroup {
	return inboxGroup{items: []core.Item{{
		ID: id, Channel: core.ChannelMail, Account: account, Thread: thread,
		From: core.Address{ID: fromAddr, Name: fromName}, Unread: true, Timestamp: at,
	}}}
}

// TestGroupBySenderMergesByLowerCasedAddressNotName covers the doc's key
// normalization rule: the sender key is the lower-cased From address, not
// the display name, so two different display names for the exact same
// address (case-insensitive) merge into one sender row, while a
// different address never merges even with an identical name.
func TestGroupBySenderMergesByLowerCasedAddressNotName(t *testing.T) {
	base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	threads := []inboxGroup{
		mailThread("mail:cl:1", "cl", "t1", "Alice@Example.com", "Alice Old Name", base),
		mailThread("mail:cl:2", "cl", "t2", "alice@example.com", "Alice New Name", base.Add(-time.Hour)),
		mailThread("mail:cl:3", "cl", "t3", "alice@example.org", "Alice New Name", base.Add(-2*time.Hour)),
	}
	senders := groupBySender(threads)
	if len(senders) != 2 {
		t.Fatalf("senders = %d, want 2 (one merged .com sender, one distinct .org sender)", len(senders))
	}
	merged := senders[0]
	if merged.key != "alice@example.com" {
		t.Fatalf("merged sender key = %q, want lower-cased alice@example.com", merged.key)
	}
	if len(merged.threads) != 2 {
		t.Fatalf("merged sender threads = %d, want 2", len(merged.threads))
	}
	// The shown name is the newest non-empty name: thread t1 (base) is
	// newer than t2 (base-1h), so "Alice Old Name" wins even though it
	// sorts as the first-seen display name, not the last.
	if merged.name != "Alice Old Name" {
		t.Fatalf("merged sender name = %q, want the newest thread's name", merged.name)
	}
	if merged.threads[0].items[0].ID != "mail:cl:1" || merged.threads[1].items[0].ID != "mail:cl:2" {
		t.Fatalf("merged sender threads not newest-first: %+v", merged.threads)
	}
}

// TestGroupBySenderOrdersSendersByNewestThread covers "senders are
// ordered by their newest mail."
func TestGroupBySenderOrdersSendersByNewestThread(t *testing.T) {
	base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	threads := []inboxGroup{
		mailThread("mail:cl:1", "cl", "t1", "bob@example.com", "Bob", base),
		mailThread("mail:cl:2", "cl", "t2", "carol@example.com", "Carol", base.Add(-time.Minute)),
	}
	senders := groupBySender(threads)
	if len(senders) != 2 || senders[0].key != "bob@example.com" || senders[1].key != "carol@example.com" {
		t.Fatalf("senders = %+v, want bob then carol (newest first)", senders)
	}
}

// TestGroupBySenderUnreadCountSumsAllThreads covers the sender row's
// unread badge: the sum of every thread's loaded unread items, not the
// number of threads.
func TestGroupBySenderUnreadCountSumsAllThreads(t *testing.T) {
	base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	t1 := mailThread("mail:cl:1", "cl", "t1", "bob@example.com", "Bob", base)
	t1.items = append(t1.items, core.Item{ID: "mail:cl:1b", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{ID: "bob@example.com"}, Unread: true, Timestamp: base.Add(-time.Minute)})
	t2 := mailThread("mail:cl:2", "cl", "t2", "bob@example.com", "Bob", base.Add(-2*time.Minute))
	senders := groupBySender([]inboxGroup{t1, t2})
	if len(senders) != 1 {
		t.Fatalf("senders = %d, want 1", len(senders))
	}
	if got := senders[0].unreadCount(); got != 3 {
		t.Fatalf("unreadCount = %d, want 3 (2 + 1 across both threads)", got)
	}
}

// TestGroupBySenderFallsBackToAddressWhenNoNameKnown covers a sender with
// no display name at all: the address itself is shown, never a raw
// protocol identifier substitute.
func TestGroupBySenderFallsBackToAddressWhenNoNameKnown(t *testing.T) {
	senders := groupBySender([]inboxGroup{mailThread("mail:cl:1", "cl", "t1", "dave@example.com", "", time.Now())})
	if senders[0].name != "dave@example.com" {
		t.Fatalf("name = %q, want the address as fallback", senders[0].name)
	}
}

// TestGroupBySenderSingleThreadStillGetsASenderRow covers the doc's
// consistency rule: even a sender with exactly one thread is wrapped.
func TestGroupBySenderSingleThreadStillGetsASenderRow(t *testing.T) {
	senders := groupBySender([]inboxGroup{mailThread("mail:cl:1", "cl", "t1", "eve@example.com", "Eve", time.Now())})
	if len(senders) != 1 || len(senders[0].threads) != 1 {
		t.Fatalf("senders = %+v, want exactly one sender wrapping its one thread", senders)
	}
}
