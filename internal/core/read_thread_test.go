package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// --- ReadThread (conversation-view.md's read-on-open bug fix) ---
//
// The live bug (2026-09-27): opening a WhatsApp conversation in the TUI
// marked only the newest item read (Service.Read/openChatCmd called
// client.Read for a single id), leaving every other unread incoming item
// stranded in the unread panel. ReadThread instead loads the whole
// conversation and marks every unread, non-FromMe item read in one call.

// spyThreadReaderAdapter implements core.ThreadReader (and, to prove it is
// preferred, core.ReadMarker too): it records every MarkThreadRead call so
// tests can assert the exact id batch Service.ReadThread sends.
type spyThreadReaderAdapter struct {
	channel core.Channel
	account string

	threadReadCalls [][]string
	threadReadErr   error

	markReadCalls []string
}

func (s *spyThreadReaderAdapter) Channel() core.Channel { return s.channel }
func (s *spyThreadReaderAdapter) Account() string       { return s.account }
func (s *spyThreadReaderAdapter) Run(ctx context.Context, sink core.Sink) error {
	return nil
}

func (s *spyThreadReaderAdapter) MarkThreadRead(ctx context.Context, ids []string) error {
	cp := append([]string(nil), ids...)
	s.threadReadCalls = append(s.threadReadCalls, cp)
	return s.threadReadErr
}

func (s *spyThreadReaderAdapter) MarkRead(ctx context.Context, id string) error {
	s.markReadCalls = append(s.markReadCalls, id)
	return nil
}

var (
	_ core.ThreadReader = (*spyThreadReaderAdapter)(nil)
	_ core.ReadMarker   = (*spyThreadReaderAdapter)(nil)
)

// spyOrganizerOnlyAdapter implements only core.Organizer (never
// ReadMarker), mirroring the real mail adapter: Service.ReadThread must
// fall back to Organize(Seen=true) per item for it.
type spyOrganizerOnlyAdapter struct {
	channel core.Channel
	account string
	calls   []organizeSeenCall
	err     error
}

type organizeSeenCall struct {
	id   string
	seen *bool
}

func (s *spyOrganizerOnlyAdapter) Channel() core.Channel { return s.channel }
func (s *spyOrganizerOnlyAdapter) Account() string       { return s.account }
func (s *spyOrganizerOnlyAdapter) Run(ctx context.Context, sink core.Sink) error {
	return nil
}

func (s *spyOrganizerOnlyAdapter) Organize(ctx context.Context, id string, op core.OrganizeOp) error {
	s.calls = append(s.calls, organizeSeenCall{id: id, seen: op.Seen})
	return s.err
}

var _ core.Organizer = (*spyOrganizerOnlyAdapter)(nil)

func threadItem(id string, channel core.Channel, account, thread string, at time.Time, unread, fromMe bool) core.Item {
	return core.Item{
		ID: id, Channel: channel, Account: account, Thread: thread,
		Timestamp: at, Unread: unread, FromMe: fromMe,
	}
}

// TestServiceReadThreadMarksOnlyUnreadNonFromMeItemsInThatThread is the
// live bug's acceptance scenario: a thread with 3 unread incoming items
// interleaved with 2 FromMe items (marked Unread=true too, to prove FromMe
// alone excludes them), plus another thread's own unread item. Only the 3
// unread incoming items of "t1" are marked read, and the adapter's batch
// covers exactly those ids, oldest->newest.
func TestServiceReadThreadMarksOnlyUnreadNonFromMeItemsInThatThread(t *testing.T) {
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	i1 := threadItem("whatsapp:wa:1", core.ChannelWhatsApp, "wa", "t1", base, true, false)
	i2 := threadItem("whatsapp:wa:2", core.ChannelWhatsApp, "wa", "t1", base.Add(time.Minute), true, true)
	i3 := threadItem("whatsapp:wa:3", core.ChannelWhatsApp, "wa", "t1", base.Add(2*time.Minute), true, false)
	i4 := threadItem("whatsapp:wa:4", core.ChannelWhatsApp, "wa", "t1", base.Add(3*time.Minute), true, true)
	i5 := threadItem("whatsapp:wa:5", core.ChannelWhatsApp, "wa", "t1", base.Add(4*time.Minute), true, false)
	other := threadItem("whatsapp:wa:6", core.ChannelWhatsApp, "wa", "t2", base.Add(5*time.Minute), true, false)

	store := newMemStore(i1, i2, i3, i4, i5, other)
	reg := core.NewRegistry()
	adapter := &spyThreadReaderAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(adapter)
	svc := core.NewService(store, reg)

	count, err := svc.ReadThread(context.Background(), "whatsapp", "wa", "t1", true)
	if err != nil {
		t.Fatalf("ReadThread returned error: %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
	if len(adapter.threadReadCalls) != 1 {
		t.Fatalf("MarkThreadRead calls = %d, want 1", len(adapter.threadReadCalls))
	}
	want := []string{i1.ID, i3.ID, i5.ID}
	got := adapter.threadReadCalls[0]
	if len(got) != len(want) {
		t.Fatalf("MarkThreadRead ids = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("MarkThreadRead ids = %+v, want %+v", got, want)
		}
	}
	if len(adapter.markReadCalls) != 0 {
		t.Fatalf("MarkRead (single-item) was called %d times, want 0: ThreadReader must be preferred", len(adapter.markReadCalls))
	}

	for _, id := range want {
		stored, err := store.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("store.Get(%s): %v", id, err)
		}
		if stored.Unread {
			t.Errorf("stored %s.Unread = true, want false", id)
		}
	}
	for _, id := range []string{i2.ID, i4.ID, other.ID} {
		stored, err := store.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("store.Get(%s): %v", id, err)
		}
		if !stored.Unread {
			t.Errorf("stored %s.Unread = false, want true (FromMe/other-thread item must never be touched)", id)
		}
	}
}

// TestServiceReadThreadFallsBackToReadMarkerPerItem covers an adapter that
// implements ReadMarker but not ThreadReader (the pre-K9 shape): Service.
// ReadThread must fall back to one MarkRead call per unread item.
func TestServiceReadThreadFallsBackToReadMarkerPerItem(t *testing.T) {
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	i1 := threadItem("matrix:mx:1", core.ChannelMatrix, "mx", "t1", base, true, false)
	i2 := threadItem("matrix:mx:2", core.ChannelMatrix, "mx", "t1", base.Add(time.Minute), true, false)

	store := newMemStore(i1, i2)
	reg := core.NewRegistry()
	adapter := &spyReadMarkerAdapter{spyAdapter: spyAdapter{channel: core.ChannelMatrix, account: "mx"}}
	reg.Register(adapter)
	svc := core.NewService(store, reg)

	count, err := svc.ReadThread(context.Background(), "matrix", "mx", "t1", true)
	if err != nil {
		t.Fatalf("ReadThread returned error: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	if want := []string{i1.ID, i2.ID}; len(adapter.markReadCalls) != len(want) || adapter.markReadCalls[0] != want[0] || adapter.markReadCalls[1] != want[1] {
		t.Fatalf("markReadCalls = %+v, want %+v", adapter.markReadCalls, want)
	}
}

// TestServiceReadThreadFallsBackToOrganizerSeenPerItem covers mail-shaped
// adapters (core.Organizer, never core.ReadMarker): Service.ReadThread
// must set \Seen (Organize with Seen=true) on each unread item.
func TestServiceReadThreadFallsBackToOrganizerSeenPerItem(t *testing.T) {
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	i1 := threadItem("mail:cl:1", core.ChannelMail, "cl", "t1", base, true, false)
	i2 := threadItem("mail:cl:2", core.ChannelMail, "cl", "t1", base.Add(time.Minute), true, false)
	fromMe := threadItem("mail:cl:3", core.ChannelMail, "cl", "t1", base.Add(2*time.Minute), true, true)

	store := newMemStore(i1, i2, fromMe)
	reg := core.NewRegistry()
	adapter := &spyOrganizerOnlyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(adapter)
	svc := core.NewService(store, reg)

	count, err := svc.ReadThread(context.Background(), "mail", "cl", "t1", true)
	if err != nil {
		t.Fatalf("ReadThread returned error: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	if len(adapter.calls) != 2 {
		t.Fatalf("Organize calls = %+v, want 2", adapter.calls)
	}
	for i, want := range []string{i1.ID, i2.ID} {
		call := adapter.calls[i]
		if call.id != want {
			t.Errorf("Organize call %d id = %q, want %q", i, call.id, want)
		}
		if call.seen == nil || !*call.seen {
			t.Errorf("Organize call %d Seen = %v, want true", i, call.seen)
		}
	}
}

// TestServiceReadThreadWithReceiptFalseSkipsChannelSideEffects covers
// --no-receipt: the store is still updated (fixing the actual unread-panel
// bug) but nothing is sent to the channel itself.
func TestServiceReadThreadWithReceiptFalseSkipsChannelSideEffects(t *testing.T) {
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	i1 := threadItem("whatsapp:wa:1", core.ChannelWhatsApp, "wa", "t1", base, true, false)

	store := newMemStore(i1)
	reg := core.NewRegistry()
	adapter := &spyThreadReaderAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(adapter)
	svc := core.NewService(store, reg)

	count, err := svc.ReadThread(context.Background(), "whatsapp", "wa", "t1", false)
	if err != nil {
		t.Fatalf("ReadThread returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if len(adapter.threadReadCalls) != 0 {
		t.Fatalf("MarkThreadRead calls = %d, want 0 with receipt=false", len(adapter.threadReadCalls))
	}
	stored, err := store.Get(context.Background(), i1.ID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if stored.Unread {
		t.Error("stored item.Unread = true, want false: --no-receipt must still clear the local unread state")
	}
}

// TestServiceReadThreadIsIdempotent covers "opening the same conversation
// twice never double-sends": the second call finds nothing left unread.
func TestServiceReadThreadIsIdempotent(t *testing.T) {
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	i1 := threadItem("whatsapp:wa:1", core.ChannelWhatsApp, "wa", "t1", base, true, false)

	store := newMemStore(i1)
	reg := core.NewRegistry()
	adapter := &spyThreadReaderAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(adapter)
	svc := core.NewService(store, reg)

	first, err := svc.ReadThread(context.Background(), "whatsapp", "wa", "t1", true)
	if err != nil || first != 1 {
		t.Fatalf("first ReadThread = (%d, %v), want (1, nil)", first, err)
	}
	second, err := svc.ReadThread(context.Background(), "whatsapp", "wa", "t1", true)
	if err != nil || second != 0 {
		t.Fatalf("second ReadThread = (%d, %v), want (0, nil)", second, err)
	}
	if len(adapter.threadReadCalls) != 1 {
		t.Fatalf("MarkThreadRead calls = %d, want 1 (never called again once nothing is unread)", len(adapter.threadReadCalls))
	}
}
