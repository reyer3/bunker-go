package core_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// pngSignatureBytes is enough of a real PNG magic header for
// http.DetectContentType (used by core's attachment inspection) to sniff
// "image/png" without a full valid image.
var pngSignatureBytes = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'r', 'e', 's', 't'}

// writeTestFile writes data to name under a fresh t.TempDir() and returns
// its path.
func writeTestFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

// imagePolicy is the permissive core.AttachmentPolicy spyMediaAdapter uses
// by default in tests: accepts image/png up to maxBytes.
func imagePolicy(maxBytes int64) core.AttachmentPolicy {
	return core.AttachmentPolicy{MaxBytes: map[string]int64{"image/png": maxBytes}}
}

// spyAdapter implements every optional capability and counts calls so
// tests can assert the dry-run gate never reaches it.
type spyAdapter struct {
	channel core.Channel
	account string

	sendCalls       int
	organizeCalls   int
	postStatusCalls int
	fetchCalls      int
	lastOutgoing    core.Outgoing
	lastOrganizeOp  core.OrganizeOp
	lastOrganizeID  string
	lastStatus      core.Status
}

func (s *spyAdapter) Channel() core.Channel { return s.channel }
func (s *spyAdapter) Account() string       { return s.account }
func (s *spyAdapter) Run(ctx context.Context, sink core.Sink) error {
	return nil
}

func (s *spyAdapter) Send(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	s.sendCalls++
	s.lastOutgoing = out
	return core.Receipt{ID: "sent-1", Channel: s.channel, At: time.Unix(1, 0)}, nil
}

func (s *spyAdapter) Organize(ctx context.Context, id string, op core.OrganizeOp) error {
	s.organizeCalls++
	s.lastOrganizeID = id
	s.lastOrganizeOp = op
	return nil
}

func (s *spyAdapter) PostStatus(ctx context.Context, status core.Status) (core.Receipt, error) {
	s.postStatusCalls++
	s.lastStatus = status
	return core.Receipt{ID: "status-1", Channel: s.channel, At: time.Unix(2, 0)}, nil
}

func (s *spyAdapter) Fetch(ctx context.Context, id string) (core.Item, error) {
	s.fetchCalls++
	return core.Item{ID: id, Channel: s.channel, Account: s.account, Body: "fetched"}, nil
}

// spyMediaAdapter implements core.MediaSender: a Sender that can also
// attach local media files. Service.Send must prefer SendMedia over the
// embedded plain Send whenever Outgoing.Attachments is non-empty.
type spyMediaAdapter struct {
	spyAdapter

	policy            core.AttachmentPolicy
	sendMediaCalls    int
	lastMediaOutgoing core.Outgoing
	sendMediaErr      error
}

func (s *spyMediaAdapter) SendMedia(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	s.sendMediaCalls++
	s.lastMediaOutgoing = out
	if s.sendMediaErr != nil {
		return core.Receipt{}, s.sendMediaErr
	}
	return core.Receipt{ID: "media-1", Channel: s.channel, At: time.Unix(3, 0)}, nil
}

// AttachmentPolicy returns the policy tests configured on this spy (see
// imagePolicy), so Service can validate attachments before ever calling
// SendMedia.
func (s *spyMediaAdapter) AttachmentPolicy() core.AttachmentPolicy { return s.policy }

var _ core.MediaSender = (*spyMediaAdapter)(nil)

// spyMover implements core.FolderMover: an Organizer whose MoveTo also
// relocates the item to a new address, the way mail's IMAP UID encodes
// the destination folder into the ID.
type spyMover struct {
	channel core.Channel
	account string

	moveCalls int
	move      core.OrganizeMove
	moveErr   error
}

func (s *spyMover) Channel() core.Channel { return s.channel }
func (s *spyMover) Account() string       { return s.account }
func (s *spyMover) Run(ctx context.Context, sink core.Sink) error {
	return nil
}

func (s *spyMover) Organize(ctx context.Context, id string, op core.OrganizeOp) error {
	_, err := s.OrganizeMove(ctx, id, op)
	return err
}

func (s *spyMover) OrganizeMove(ctx context.Context, id string, op core.OrganizeOp) (core.OrganizeMove, error) {
	s.moveCalls++
	if s.moveErr != nil {
		return core.OrganizeMove{}, s.moveErr
	}
	return s.move, nil
}

// bareAdapter implements only the mandatory Adapter capability, used to
// exercise ErrUnsupported.
type bareAdapter struct {
	channel core.Channel
	account string
}

func (b bareAdapter) Channel() core.Channel { return b.channel }
func (b bareAdapter) Account() string       { return b.account }
func (b bareAdapter) Run(ctx context.Context, sink core.Sink) error {
	return nil
}

// memStore is a minimal in-memory core.Store fake for Service tests.
type memStore struct {
	items map[string]core.Item
	// upsertErr, when set, makes every Upsert fail instead of storing,
	// for tests proving a failed sent-item write (R3) is logged rather
	// than silently dropped.
	upsertErr error
}

func newMemStore(items ...core.Item) *memStore {
	m := &memStore{items: make(map[string]core.Item)}
	for _, it := range items {
		m.items[it.ID] = it
	}
	return m
}

func (m *memStore) Upsert(ctx context.Context, item core.Item) error {
	if m.upsertErr != nil {
		return m.upsertErr
	}
	m.items[item.ID] = item
	return nil
}

func (m *memStore) MarkRead(ctx context.Context, id string, read bool) error {
	it, ok := m.items[id]
	if !ok {
		return core.ErrNotFound
	}
	it.Unread = !read
	m.items[id] = it
	return nil
}

func (m *memStore) MarkThreadReadUpTo(ctx context.Context, channel core.Channel, account, thread string, upTo time.Time) error {
	for id, it := range m.items {
		if it.Channel != channel || it.Account != account || it.Thread != thread {
			continue
		}
		if it.FromMe || !it.Unread {
			continue
		}
		if it.Timestamp.After(upTo) {
			continue
		}
		it.Unread = false
		m.items[id] = it
	}
	return nil
}

func (m *memStore) Delete(ctx context.Context, id string) error {
	if _, ok := m.items[id]; !ok {
		return core.ErrNotFound
	}
	delete(m.items, id)
	return nil
}

func (m *memStore) Cursor(ctx context.Context, key string) (string, error) { return "", nil }
func (m *memStore) SetCursor(ctx context.Context, key, val string) error   { return nil }

func (m *memStore) EditItem(ctx context.Context, id, body string) error {
	it, ok := m.items[id]
	if !ok {
		return core.ErrNotFound
	}
	it.Body = body
	it.Edited = true
	m.items[id] = it
	return nil
}

func (m *memStore) RevokeItem(ctx context.Context, id string) error {
	it, ok := m.items[id]
	if !ok {
		return core.ErrNotFound
	}
	it.Body = ""
	it.Deleted = true
	m.items[id] = it
	return nil
}

func (m *memStore) SetReaction(ctx context.Context, id string, reaction core.Reaction) error {
	it, ok := m.items[id]
	if !ok {
		return core.ErrNotFound
	}
	var kept []core.Reaction
	for _, r := range it.Reactions {
		if r.Sender != reaction.Sender {
			kept = append(kept, r)
		}
	}
	if reaction.Emoji != "" {
		kept = append(kept, reaction)
	}
	it.Reactions = kept
	m.items[id] = it
	return nil
}

func (m *memStore) Get(ctx context.Context, id string) (core.Item, error) {
	it, ok := m.items[id]
	if !ok {
		return core.Item{}, core.ErrNotFound
	}
	return it, nil
}

func (m *memStore) List(ctx context.Context, filter core.Filter) ([]core.Item, error) {
	out := make([]core.Item, 0, len(m.items))
	for _, it := range m.items {
		if filter.Channel != "" && it.Channel != filter.Channel {
			continue
		}
		if filter.Account != "" && it.Account != filter.Account {
			continue
		}
		if filter.Thread != "" && it.Thread != filter.Thread {
			continue
		}
		if filter.Unread != nil && it.Unread != *filter.Unread {
			continue
		}
		out = append(out, it)
	}
	return out, nil
}

// Thread is a minimal in-memory stand-in: it filters by
// Channel/Account/Thread, sorts ascending by Timestamp, applies the
// before/limit cursor the same way the real store's SQL does (strictly
// before, most recent limit items within that window), and returns
// oldest→newest.
func (m *memStore) Thread(ctx context.Context, filter core.Filter, before time.Time, limit int) ([]core.Item, error) {
	var matched []core.Item
	for _, it := range m.items {
		if it.Channel != filter.Channel || it.Account != filter.Account || it.Thread != filter.Thread {
			continue
		}
		if !before.IsZero() && !it.Timestamp.Before(before) {
			continue
		}
		matched = append(matched, it)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Timestamp.After(matched[j].Timestamp) })
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Timestamp.Before(matched[j].Timestamp) })
	return matched, nil
}

func (m *memStore) Counts(ctx context.Context) (map[core.Channel]map[string]int, error) {
	out := make(map[core.Channel]map[string]int)
	for _, it := range m.items {
		if !it.Unread {
			continue
		}
		if out[it.Channel] == nil {
			out[it.Channel] = make(map[string]int)
		}
		out[it.Channel][it.Account]++
	}
	return out, nil
}

func TestServiceThreadFiltersAndDefaultsLimit(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	mk := func(id string, offsetMin int) core.Item {
		return core.Item{
			ID:        id,
			Channel:   core.ChannelWhatsApp,
			Account:   "personal",
			Thread:    "5511999999999@s.whatsapp.net",
			Timestamp: base.Add(time.Duration(offsetMin) * time.Minute),
		}
	}
	other := core.Item{
		ID: "whatsapp:personal:other", Channel: core.ChannelWhatsApp, Account: "personal",
		Thread: "5511888888888@s.whatsapp.net", Timestamp: base,
	}
	store := newMemStore(mk("w:1", 0), mk("w:2", 1), mk("w:3", 2), other)
	svc := core.NewService(store, core.NewRegistry())

	items, err := svc.Thread(context.Background(), "whatsapp", "personal", "5511999999999@s.whatsapp.net", time.Time{}, 0)
	if err != nil {
		t.Fatalf("Thread: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("Thread() len = %d, want 3 (other thread must be excluded)", len(items))
	}
	for i := 0; i+1 < len(items); i++ {
		if !items[i].Timestamp.Before(items[i+1].Timestamp) {
			t.Fatalf("Thread() not oldest→newest: %v then %v", items[i].Timestamp, items[i+1].Timestamp)
		}
	}

	// limit <= 0 must default to 50 (fewer than 50 items here, so this
	// only proves the zero limit was not treated as "return nothing").
	limited, err := svc.Thread(context.Background(), "whatsapp", "personal", "5511999999999@s.whatsapp.net", time.Time{}, 2)
	if err != nil {
		t.Fatalf("Thread (limit 2): %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("Thread(limit=2) len = %d, want 2", len(limited))
	}
	if limited[0].ID != "w:2" || limited[1].ID != "w:3" {
		t.Fatalf("Thread(limit=2) = %+v, want the 2 most recent, oldest→newest", limited)
	}
}

func TestServiceReplyDryRunNeverCallsAdapter(t *testing.T) {
	item := core.Item{
		ID:      "mail:cl:1",
		Channel: core.ChannelMail,
		Account: "cl",
		Thread:  "t1",
		From:    core.Address{ID: "them@x.cl"},
		Subject: "hi",
	}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	plan, receipt, err := svc.Reply(context.Background(), item.ID, "reply body", nil, nil, true)
	if err != nil {
		t.Fatalf("Reply dry-run returned error: %v", err)
	}
	if spy.sendCalls != 0 {
		t.Fatalf("dry-run called adapter.Send %d times, want 0", spy.sendCalls)
	}
	if !isZeroReceipt(receipt) {
		t.Fatalf("dry-run returned non-zero receipt: %+v", receipt)
	}
	if plan.Action != "reply" || plan.Channel != core.ChannelMail || plan.Account != "cl" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestServiceReplyExecutesAndReturnsReceipt(t *testing.T) {
	item := core.Item{
		ID:      "mail:cl:1",
		Channel: core.ChannelMail,
		Account: "cl",
		Thread:  "t1",
		From:    core.Address{ID: "them@x.cl"},
		Subject: "hi",
	}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	plan, receipt, err := svc.Reply(context.Background(), item.ID, "reply body", nil, nil, false)
	if err != nil {
		t.Fatalf("Reply returned error: %v", err)
	}
	if spy.sendCalls != 1 {
		t.Fatalf("adapter.Send called %d times, want 1", spy.sendCalls)
	}
	if spy.lastOutgoing.ReplyTo != item.ID || spy.lastOutgoing.Body != "reply body" {
		t.Fatalf("unexpected outgoing: %+v", spy.lastOutgoing)
	}
	if receipt.ID != "sent-1" {
		t.Fatalf("receipt = %+v, want ID sent-1", receipt)
	}
	if plan.Action != "reply" {
		t.Fatalf("plan.Action = %q, want reply", plan.Action)
	}
}

// TestServiceReplyStoresSentItemAsFromMe covers K7b (conversation-view.md,
// Usability pass): most channels never echo bunker's own outgoing message
// back as an ingest event (WhatsApp never delivers events.Message for a
// linked device's own send), so without this a real Reply would never
// appear in the chat view at all. A WhatsApp/Matrix Reply (never mail —
// see the next test) must store the result as a FromMe item keyed by the
// receipt's own id, grouped under the original item's Thread.
func TestServiceReplyStoresSentItemAsFromMe(t *testing.T) {
	item := core.Item{
		ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal",
		Thread: "5511999@s.whatsapp.net", From: core.Address{ID: "5511999@s.whatsapp.net"},
	}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	_, receipt, err := svc.Reply(context.Background(), item.ID, "reply body", nil, nil, false)
	if err != nil {
		t.Fatalf("Reply returned error: %v", err)
	}
	stored, ok := store.items[receipt.ID]
	if !ok {
		t.Fatalf("Reply did not store the sent item under receipt.ID %q; store = %+v", receipt.ID, store.items)
	}
	if !stored.FromMe || stored.Unread {
		t.Fatalf("stored item = %+v, want FromMe=true Unread=false", stored)
	}
	if stored.Thread != item.Thread || stored.Body != "reply body" {
		t.Fatalf("stored item = %+v, want Thread=%q Body=%q", stored, item.Thread, "reply body")
	}
	if len(stored.To) != 1 || stored.To[0].ID != item.From.ID {
		t.Fatalf("stored item.To = %+v, want [%s]", stored.To, item.From.ID)
	}
	if !stored.Timestamp.Equal(receipt.At) {
		t.Fatalf("stored item.Timestamp = %v, want receipt.At = %v", stored.Timestamp, receipt.At)
	}
}

// TestServiceReplyMailNeverStoresSentItem: mail already gets its sent
// copies through the K2 Sent-folder sync (conversation-view.md), keyed by
// its own UID-derived id once the message round-trips through IMAP. This
// synthetic call has no way to learn that later id in advance, so a mail
// Reply must not additionally store a second, differently-ID'd copy the
// K2 sync could never dedupe against.
func TestServiceReplyMailNeverStoresSentItem(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{ID: "them@x.cl"}}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	if _, _, err := svc.Reply(context.Background(), item.ID, "reply body", nil, nil, false); err != nil {
		t.Fatalf("Reply returned error: %v", err)
	}
	if len(store.items) != 1 {
		t.Fatalf("store has %d items after a mail Reply, want 1 (the original only, no synthetic sent copy): %+v", len(store.items), store.items)
	}
}

// TestServiceReplyWithCcSetsOutgoingCcAndPlan covers T12(b): a reply may
// carry Cc recipients, which must reach the adapter's Outgoing.Cc and be
// visible in the Plan, not silently dropped.
func TestServiceReplyWithCcSetsOutgoingCcAndPlan(t *testing.T) {
	item := core.Item{
		ID:      "mail:cl:1",
		Channel: core.ChannelMail,
		Account: "cl",
		From:    core.Address{ID: "them@x.cl"},
	}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	cc := []string{"cc1@x.cl", "cc2@x.cl"}
	plan, _, err := svc.Reply(context.Background(), item.ID, "body", cc, nil, false)
	if err != nil {
		t.Fatalf("Reply returned error: %v", err)
	}
	if len(spy.lastOutgoing.Cc) != 2 || spy.lastOutgoing.Cc[0] != "cc1@x.cl" || spy.lastOutgoing.Cc[1] != "cc2@x.cl" {
		t.Fatalf("adapter Outgoing.Cc = %+v, want %+v", spy.lastOutgoing.Cc, cc)
	}
	if len(plan.Cc) != 2 || plan.Cc[0] != "cc1@x.cl" || plan.Cc[1] != "cc2@x.cl" {
		t.Fatalf("plan.Cc = %+v, want %+v", plan.Cc, cc)
	}
}

func TestServiceReplyUnknownIDReturnsErrNotFound(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	svc := core.NewService(store, reg)

	_, _, err := svc.Reply(context.Background(), "mail:cl:missing", "x", nil, nil, true)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestServiceReplyUnsupportedCapability(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl"}
	store := newMemStore(item)
	reg := core.NewRegistry()
	reg.Register(bareAdapter{channel: core.ChannelMail, account: "cl"})
	svc := core.NewService(store, reg)

	_, _, err := svc.Reply(context.Background(), item.ID, "x", nil, nil, true)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// TestServiceReplyMediaRequiresMediaSenderCapability mirrors T11(e) for
// Reply: a channel whose adapter only implements plain Sender must not
// silently reply text-only and drop the attachments.
func TestServiceReplyMediaRequiresMediaSenderCapability(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", From: core.Address{ID: "them@x.cl"}}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	path := writeTestFile(t, "pic.png", pngSignatureBytes)
	_, _, err := svc.Reply(context.Background(), item.ID, "x", nil, []string{path}, false)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if spy.sendCalls != 0 {
		t.Fatalf("adapter.Send called %d times, want 0 (must not silently drop attachments)", spy.sendCalls)
	}
}

// TestServiceReplyMediaDryRunNeverCallsAdapter covers T11b(5)'s dry-run
// half: attachments are validated and listed in the Plan without ever
// reaching SendMedia.
func TestServiceReplyMediaDryRunNeverCallsAdapter(t *testing.T) {
	item := core.Item{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", From: core.Address{ID: "1234@s.whatsapp.net"}}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyMediaAdapter{spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}, policy: imagePolicy(1 << 20)}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	path := writeTestFile(t, "pic.png", pngSignatureBytes)
	plan, receipt, err := svc.Reply(context.Background(), item.ID, "mira", nil, []string{path}, true)
	if err != nil {
		t.Fatalf("Reply dry-run returned error: %v", err)
	}
	if spy.sendMediaCalls != 0 || spy.sendCalls != 0 {
		t.Fatalf("dry-run called adapter (SendMedia=%d, Send=%d), want 0/0", spy.sendMediaCalls, spy.sendCalls)
	}
	if !isZeroReceipt(receipt) {
		t.Fatalf("dry-run returned non-zero receipt: %+v", receipt)
	}
	if len(plan.Attachments) != 1 || plan.Attachments[0].MIME != "image/png" {
		t.Fatalf("plan.Attachments = %+v, want one image/png entry", plan.Attachments)
	}
}

// TestServiceReplyMediaExecutesUsesMediaSender covers T11b(5): a real
// Reply with attachments goes through the MediaSender capability, quoting
// the replied-to item via ReplyTo like a plain reply does.
func TestServiceReplyMediaExecutesUsesMediaSender(t *testing.T) {
	item := core.Item{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", From: core.Address{ID: "1234@s.whatsapp.net"}}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyMediaAdapter{spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}, policy: imagePolicy(1 << 20)}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	path := writeTestFile(t, "pic.png", pngSignatureBytes)
	_, receipt, err := svc.Reply(context.Background(), item.ID, "mira", nil, []string{path}, false)
	if err != nil {
		t.Fatalf("Reply returned error: %v", err)
	}
	if spy.sendMediaCalls != 1 || spy.sendCalls != 0 {
		t.Fatalf("SendMedia=%d Send=%d, want 1/0", spy.sendMediaCalls, spy.sendCalls)
	}
	if receipt.ID != "media-1" {
		t.Fatalf("receipt = %+v, want ID media-1", receipt)
	}
	if spy.lastMediaOutgoing.ReplyTo != item.ID {
		t.Fatalf("SendMedia outgoing.ReplyTo = %q, want %q", spy.lastMediaOutgoing.ReplyTo, item.ID)
	}
}

func TestServiceSendDryRunNeverCallsAdapter(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Body: "hola"}
	plan, receipt, err := svc.Send(context.Background(), out, true)
	if err != nil {
		t.Fatalf("Send dry-run returned error: %v", err)
	}
	if spy.sendCalls != 0 {
		t.Fatalf("dry-run called adapter.Send %d times, want 0", spy.sendCalls)
	}
	if !isZeroReceipt(receipt) {
		t.Fatalf("dry-run returned non-zero receipt: %+v", receipt)
	}
	if plan.Action != "send" {
		t.Fatalf("plan.Action = %q, want send", plan.Action)
	}
}

func TestServiceSendExecutes(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Body: "hola"}
	_, receipt, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if spy.sendCalls != 1 {
		t.Fatalf("adapter.Send called %d times, want 1", spy.sendCalls)
	}
	if receipt.ID != "sent-1" {
		t.Fatalf("receipt = %+v", receipt)
	}
}

// TestServiceSendStoresSentItemAsFromMe covers K7b: a real (non-dry-run)
// Send must store the result as a FromMe item keyed by the receipt's own
// id, so it appears in the chat view even though the channel itself never
// echoes bunker's own send back as an ingest event.
func TestServiceSendStoresSentItemAsFromMe(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Body: "hola"}
	_, receipt, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	stored, ok := store.items[receipt.ID]
	if !ok {
		t.Fatalf("Send did not store the sent item under receipt.ID %q; store = %+v", receipt.ID, store.items)
	}
	if !stored.FromMe || stored.Unread {
		t.Fatalf("stored item = %+v, want FromMe=true Unread=false", stored)
	}
	if stored.Body != "hola" || stored.Channel != core.ChannelWhatsApp || stored.Account != "personal" {
		t.Fatalf("stored item = %+v, want the sent body/channel/account", stored)
	}
	if stored.Thread != "5511999" {
		t.Fatalf("stored item.Thread = %q, want the recipient %q (no explicit Thread given)", stored.Thread, "5511999")
	}
	if len(stored.To) != 1 || stored.To[0].ID != "5511999" {
		t.Fatalf("stored item.To = %+v, want [5511999]", stored.To)
	}
	if !stored.Timestamp.Equal(receipt.At) {
		t.Fatalf("stored item.Timestamp = %v, want receipt.At = %v", stored.Timestamp, receipt.At)
	}
}

// TestServiceSendDryRunNeverStoresSentItem is K7b's mutation-checked half:
// a dry-run must NEVER store anything, since it never actually sent.
func TestServiceSendDryRunNeverStoresSentItem(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Body: "hola"}
	if _, _, err := svc.Send(context.Background(), out, true); err != nil {
		t.Fatalf("Send dry-run returned error: %v", err)
	}
	if len(store.items) != 0 {
		t.Fatalf("dry-run Send stored %d item(s), want 0: %+v", len(store.items), store.items)
	}
}

// TestServiceSendUpsertIsIdempotentOnRepeatedReceiptID: the channel may
// later echo the same message id back through its own ingest path; a
// second store write for the same id must overwrite, never duplicate.
func TestServiceSendUpsertIsIdempotentOnRepeatedReceiptID(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "personal"} // always returns receipt ID "sent-1"
	reg.Register(spy)
	svc := core.NewService(store, reg)

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Body: "hola"}
	if _, _, err := svc.Send(context.Background(), out, false); err != nil {
		t.Fatalf("first Send returned error: %v", err)
	}
	out.Body = "hola de nuevo"
	if _, _, err := svc.Send(context.Background(), out, false); err != nil {
		t.Fatalf("second Send returned error: %v", err)
	}
	if len(store.items) != 1 {
		t.Fatalf("store has %d items, want exactly 1 (idempotent upsert by receipt.ID): %+v", len(store.items), store.items)
	}
	if store.items["sent-1"].Body != "hola de nuevo" {
		t.Fatalf("stored body = %q, want the second call's body (last write wins)", store.items["sent-1"].Body)
	}
}

// TestServiceSendMediaStoresSentItemWithAttachments covers K7b's
// "attachments metadata" clause for the media (SendMedia) path.
func TestServiceSendMediaStoresSentItemWithAttachments(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyMediaAdapter{spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}, policy: imagePolicy(1 << 20)}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	path := writeTestFile(t, "pic.png", pngSignatureBytes)
	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Body: "mira", Attachments: []string{path}}
	_, receipt, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	stored, ok := store.items[receipt.ID]
	if !ok {
		t.Fatalf("Send did not store the sent item under receipt.ID %q", receipt.ID)
	}
	if len(stored.Attachments) != 1 || stored.Attachments[0].Name != "pic.png" || stored.Attachments[0].MIME != "image/png" {
		t.Fatalf("stored item.Attachments = %+v, want one pic.png/image/png entry", stored.Attachments)
	}
}

// TestServiceSendIncludesCcInOutgoingAndPlan covers T12(b): a send may
// carry Cc recipients, which must reach the adapter's Outgoing.Cc and be
// visible in the Plan, not silently dropped.
func TestServiceSendIncludesCcInOutgoingAndPlan(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	// A single recipient is enough to exercise Cc pass-through; a second
	// recipient would additionally exercise T13(a)'s fan-out (spyAdapter
	// implements plain Sender only, no core.MultiRecipientSender), which
	// has its own dedicated tests in fanout_test.go.
	out := core.Outgoing{
		Channel: core.ChannelMail,
		Account: "cl",
		To:      []string{"a@b.cl"},
		Cc:      []string{"e@f.cl"},
		Body:    "hi",
	}
	plan, _, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if len(spy.lastOutgoing.Cc) != 1 || spy.lastOutgoing.Cc[0] != "e@f.cl" {
		t.Fatalf("adapter Outgoing.Cc = %+v, want [e@f.cl]", spy.lastOutgoing.Cc)
	}
	if len(plan.Cc) != 1 || plan.Cc[0] != "e@f.cl" {
		t.Fatalf("plan.Cc = %+v, want [e@f.cl]", plan.Cc)
	}
}

// TestServiceSendMediaRequiresMediaSenderCapability covers T11(e): an
// adapter that only implements plain Sender (not MediaSender) must not
// silently send text-only and drop the attachments — Service.Send has to
// report ErrUnsupported instead.
func TestServiceSendMediaRequiresMediaSenderCapability(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"x@y.cl"}, Body: "hola", Attachments: []string{"/tmp/pic.jpg"}}
	_, _, err := svc.Send(context.Background(), out, false)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if spy.sendCalls != 0 {
		t.Fatalf("adapter.Send called %d times, want 0 (must not silently drop attachments)", spy.sendCalls)
	}
}

// TestServiceSendMediaDryRunNeverCallsAdapter mirrors the plain-Send dry-
// run gate for the media path: dry-run must not call SendMedia (no
// upload), and the returned Plan must list the attachments.
func TestServiceSendMediaDryRunNeverCallsAdapter(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyMediaAdapter{spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}, policy: imagePolicy(1 << 20)}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	path := writeTestFile(t, "pic.png", pngSignatureBytes)
	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Body: "mira", Attachments: []string{path}}
	plan, receipt, err := svc.Send(context.Background(), out, true)
	if err != nil {
		t.Fatalf("Send dry-run returned error: %v", err)
	}
	if spy.sendMediaCalls != 0 || spy.sendCalls != 0 {
		t.Fatalf("dry-run called adapter (SendMedia=%d, Send=%d), want 0/0", spy.sendMediaCalls, spy.sendCalls)
	}
	if !isZeroReceipt(receipt) {
		t.Fatalf("dry-run returned non-zero receipt: %+v", receipt)
	}
	if len(plan.Media) != 1 || plan.Media[0] != path {
		t.Fatalf("plan.Media = %+v, want [%s]", plan.Media, path)
	}
	if len(plan.Attachments) != 1 {
		t.Fatalf("plan.Attachments = %+v, want 1 entry", plan.Attachments)
	}
	got := plan.Attachments[0]
	if got.Name != "pic.png" || got.MIME != "image/png" || got.Size != int64(len(pngSignatureBytes)) {
		t.Fatalf("plan.Attachments[0] = %+v, want Name=pic.png MIME=image/png Size=%d", got, len(pngSignatureBytes))
	}
}

// TestServiceSendMediaExecutesUsesMediaSenderNotPlainSend covers T11(b):
// a real Send with Attachments must go through the MediaSender capability,
// never the plain Sender.Send that would drop them.
func TestServiceSendMediaExecutesUsesMediaSenderNotPlainSend(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyMediaAdapter{spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}, policy: imagePolicy(1 << 20)}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	path := writeTestFile(t, "pic.png", pngSignatureBytes)
	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Body: "mira", Attachments: []string{path}}
	_, receipt, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if spy.sendMediaCalls != 1 {
		t.Fatalf("adapter.SendMedia called %d times, want 1", spy.sendMediaCalls)
	}
	if spy.sendCalls != 0 {
		t.Fatalf("adapter.Send (plain, text-only) called %d times, want 0", spy.sendCalls)
	}
	if receipt.ID != "media-1" {
		t.Fatalf("receipt = %+v, want ID media-1", receipt)
	}
	if spy.lastMediaOutgoing.Body != "mira" {
		t.Fatalf("SendMedia outgoing = %+v, want Body 'mira'", spy.lastMediaOutgoing)
	}
}

// TestServiceSendAttachmentPolicyRejectsUnsupportedType covers T11b(1): the
// adapter's AttachmentPolicy, not the CLI, decides which MIME types are
// acceptable — a type absent from MaxBytes fails before SendMedia is ever
// called, on dry-run too.
func TestServiceSendAttachmentPolicyRejectsUnsupportedType(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyMediaAdapter{spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}, policy: imagePolicy(1 << 20)}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	path := writeTestFile(t, "notes.txt", []byte("plain text, not an image"))
	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Attachments: []string{path}}

	if _, _, err := svc.Send(context.Background(), out, true); err == nil {
		t.Fatal("Send dry-run error = nil, want a policy rejection for an unsupported type")
	}
	if _, _, err := svc.Send(context.Background(), out, false); err == nil {
		t.Fatal("Send error = nil, want a policy rejection for an unsupported type")
	}
	if spy.sendMediaCalls != 0 {
		t.Fatalf("adapter.SendMedia called %d times, want 0", spy.sendMediaCalls)
	}
}

// TestServiceSendAttachmentPolicyRejectsOversize covers T11b(3): the size
// limit lives in the adapter's policy and is enforced by Service before
// any upload, on dry-run too.
func TestServiceSendAttachmentPolicyRejectsOversize(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyMediaAdapter{spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}, policy: imagePolicy(4)}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	path := writeTestFile(t, "pic.png", pngSignatureBytes) // larger than the 4-byte policy limit
	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Attachments: []string{path}}

	if _, _, err := svc.Send(context.Background(), out, true); err == nil {
		t.Fatal("Send dry-run error = nil, want a policy rejection for an oversized file")
	}
	if _, _, err := svc.Send(context.Background(), out, false); err == nil {
		t.Fatal("Send error = nil, want a policy rejection for an oversized file")
	}
	if spy.sendMediaCalls != 0 {
		t.Fatalf("adapter.SendMedia called %d times, want 0", spy.sendMediaCalls)
	}
}

// TestServiceSendWithoutAttachmentsStillUsesPlainSend is the byte-for-byte
// regression guard: an adapter implementing MediaSender must still take
// the plain Send path when Outgoing.Attachments is empty.
func TestServiceSendWithoutAttachmentsStillUsesPlainSend(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyMediaAdapter{spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"5511999"}, Body: "hola"}
	_, _, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if spy.sendCalls != 1 || spy.sendMediaCalls != 0 {
		t.Fatalf("Send=%d SendMedia=%d, want 1/0", spy.sendCalls, spy.sendMediaCalls)
	}
}

func TestServiceOrganizeDryRunNeverCallsAdapter(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl"}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	op := core.OrganizeOp{AddLabels: []string{"vip"}}
	plan, err := svc.Organize(context.Background(), item.ID, op, true)
	if err != nil {
		t.Fatalf("Organize dry-run returned error: %v", err)
	}
	if spy.organizeCalls != 0 {
		t.Fatalf("dry-run called adapter.Organize %d times, want 0", spy.organizeCalls)
	}
	if plan.Action != "organize" {
		t.Fatalf("plan.Action = %q, want organize", plan.Action)
	}
}

func TestServiceOrganizeExecutes(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl"}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	op := core.OrganizeOp{AddLabels: []string{"vip"}}
	_, err := svc.Organize(context.Background(), item.ID, op, false)
	if err != nil {
		t.Fatalf("Organize returned error: %v", err)
	}
	if spy.organizeCalls != 1 {
		t.Fatalf("adapter.Organize called %d times, want 1", spy.organizeCalls)
	}
	if spy.lastOrganizeID != item.ID {
		t.Fatalf("organized id = %q, want %q", spy.lastOrganizeID, item.ID)
	}
}

// TestServiceOrganizeReconcilesSeenAndLabelsGenerically covers the
// channel-agnostic half of T9(a): a plain core.Organizer (no
// FolderMover) has no notion of relocating the item, so Service itself
// applies Seen/labels to the stored copy after a successful call.
func TestServiceOrganizeReconcilesSeenAndLabelsGenerically(t *testing.T) {
	item := core.Item{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl",
		Unread: true, Labels: []string{"old"},
	}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	seen := true
	op := core.OrganizeOp{Seen: &seen, AddLabels: []string{"vip"}, RemoveLabels: []string{"old"}}
	if _, err := svc.Organize(context.Background(), item.ID, op, false); err != nil {
		t.Fatalf("Organize returned error: %v", err)
	}

	got, err := store.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("store.Get after Organize: %v", err)
	}
	if got.Unread {
		t.Error("Unread = true after Seen=true, want false")
	}
	if len(got.Labels) != 1 || got.Labels[0] != "vip" {
		t.Errorf("Labels = %v, want [vip]", got.Labels)
	}
}

// TestServiceOrganizeMoveRekeysStoreViaFolderMover covers T9(a)'s move
// case: FolderMover reports the item's new address, and Service must
// rekey the store to it (deleting the old id) instead of leaving a
// stale copy in the folder view it moved out of — this is the exact bug
// found live on 2026-09-25 (organize --move Archives left INBOX still
// showing the moved mail as unread).
func TestServiceOrganizeMoveRekeysStoreViaFolderMover(t *testing.T) {
	oldID := "mail:cl:100.4"
	newID := "mail:cl:100.9"
	item := core.Item{
		ID: oldID, Channel: core.ChannelMail, Account: "cl",
		Unread: true, Meta: map[string]string{"folder": "INBOX"},
	}
	store := newMemStore(item)
	reg := core.NewRegistry()
	mover := &spyMover{
		channel: core.ChannelMail, account: "cl",
		move: core.OrganizeMove{ID: newID, Folder: "INBOX.Archives"},
	}
	reg.Register(mover)
	svc := core.NewService(store, reg)

	if _, err := svc.Organize(context.Background(), oldID, core.OrganizeOp{MoveTo: "Archives"}, false); err != nil {
		t.Fatalf("Organize returned error: %v", err)
	}
	if mover.moveCalls != 1 {
		t.Fatalf("adapter.OrganizeMove called %d times, want 1", mover.moveCalls)
	}

	if _, err := store.Get(context.Background(), oldID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("store still has the old id %q after move: err = %v, want ErrNotFound", oldID, err)
	}
	got, err := store.Get(context.Background(), newID)
	if err != nil {
		t.Fatalf("store.Get(new id) after move: %v", err)
	}
	if got.Meta["folder"] != "INBOX.Archives" {
		t.Errorf("Meta[folder] = %q, want INBOX.Archives", got.Meta["folder"])
	}
}

func TestServiceOrganizeDryRunNeverTouchesStoreOnMove(t *testing.T) {
	oldID := "mail:cl:100.4"
	item := core.Item{ID: oldID, Channel: core.ChannelMail, Account: "cl"}
	store := newMemStore(item)
	reg := core.NewRegistry()
	mover := &spyMover{
		channel: core.ChannelMail, account: "cl",
		move: core.OrganizeMove{ID: "mail:cl:100.9", Folder: "INBOX.Archives"},
	}
	reg.Register(mover)
	svc := core.NewService(store, reg)

	if _, err := svc.Organize(context.Background(), oldID, core.OrganizeOp{MoveTo: "Archives"}, true); err != nil {
		t.Fatalf("Organize dry-run returned error: %v", err)
	}
	if mover.moveCalls != 0 {
		t.Fatalf("dry-run called adapter.OrganizeMove %d times, want 0", mover.moveCalls)
	}
	if _, err := store.Get(context.Background(), oldID); err != nil {
		t.Fatalf("store lost the item on a dry-run: %v", err)
	}
}

func TestServicePostStatusDryRunNeverCallsAdapter(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	status := core.Status{Text: "good morning"}
	plan, receipt, err := svc.PostStatus(context.Background(), core.ChannelWhatsApp, "personal", status, true)
	if err != nil {
		t.Fatalf("PostStatus dry-run returned error: %v", err)
	}
	if spy.postStatusCalls != 0 {
		t.Fatalf("dry-run called adapter.PostStatus %d times, want 0", spy.postStatusCalls)
	}
	if !isZeroReceipt(receipt) {
		t.Fatalf("dry-run returned non-zero receipt: %+v", receipt)
	}
	if plan.Action != "status" {
		t.Fatalf("plan.Action = %q, want status", plan.Action)
	}
}

func TestServicePostStatusExecutes(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	status := core.Status{Text: "good morning"}
	_, receipt, err := svc.PostStatus(context.Background(), core.ChannelWhatsApp, "personal", status, false)
	if err != nil {
		t.Fatalf("PostStatus returned error: %v", err)
	}
	if spy.postStatusCalls != 1 {
		t.Fatalf("adapter.PostStatus called %d times, want 1", spy.postStatusCalls)
	}
	if receipt.ID != "status-1" {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestServiceFetchUsesFetcherCapability(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Body: "preview"}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	got, err := svc.Fetch(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if spy.fetchCalls != 1 {
		t.Fatalf("adapter.Fetch called %d times, want 1", spy.fetchCalls)
	}
	if got.Body != "fetched" {
		t.Fatalf("Fetch body = %q, want fetched", got.Body)
	}
}

func TestServiceListAndCounts(t *testing.T) {
	unread := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Unread: true}
	read := core.Item{ID: "mail:cl:2", Channel: core.ChannelMail, Account: "cl", Unread: false}
	store := newMemStore(unread, read)
	reg := core.NewRegistry()
	svc := core.NewService(store, reg)

	items, err := svc.List(context.Background(), core.Filter{Channel: core.ChannelMail})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("List len = %d, want 2", len(items))
	}

	counts, err := svc.Counts(context.Background())
	if err != nil {
		t.Fatalf("Counts returned error: %v", err)
	}
	if counts[core.ChannelMail]["cl"] != 1 {
		t.Fatalf("counts = %+v, want mail/cl=1", counts)
	}
}

// TestServiceSendAttachmentPolicyWildcardAcceptsAnyType: mail accepts any
// file type; the "*/*" key is the fallback limit for MIME types without an
// exact entry.
func TestServiceSendAttachmentPolicyWildcardAcceptsAnyType(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	policy := core.AttachmentPolicy{MaxBytes: map[string]int64{core.AnyMIME: 1 << 20}}
	spy := &spyMediaAdapter{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}, policy: policy}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	path := writeTestFile(t, "notes.txt", []byte("plain text attachment"))
	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"a@b.cl"}, Attachments: []string{path}}
	if _, _, err := svc.Send(context.Background(), out, false); err != nil {
		t.Fatalf("Send error = %v, want the wildcard policy to accept text/plain", err)
	}
	if spy.sendMediaCalls != 1 {
		t.Fatalf("adapter.SendMedia called %d times, want 1", spy.sendMediaCalls)
	}
}

// TestServiceSendAttachmentPolicyRejectsOverTotal: MaxTotalBytes caps the
// sum of all attachments (a mail message size limit), checked before
// SendMedia and on dry-run too.
func TestServiceSendAttachmentPolicyRejectsOverTotal(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	policy := core.AttachmentPolicy{MaxBytes: map[string]int64{core.AnyMIME: 1 << 20}, MaxTotalBytes: 30}
	spy := &spyMediaAdapter{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}, policy: policy}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	a := writeTestFile(t, "a.txt", []byte("twenty bytes of text"))
	b := writeTestFile(t, "b.txt", []byte("twenty more bytes..."))
	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"a@b.cl"}, Attachments: []string{a, b}}
	for _, dry := range []bool{true, false} {
		_, _, err := svc.Send(context.Background(), out, dry)
		if err == nil || !strings.Contains(err.Error(), "total") {
			t.Fatalf("Send(dryRun=%v) error = %v, want a total-size rejection", dry, err)
		}
	}
	if spy.sendMediaCalls != 0 {
		t.Fatalf("adapter.SendMedia called %d times, want 0", spy.sendMediaCalls)
	}
}

// forgetfulFetcher is an adapter whose Fetcher only knows items seen by the
// current process (like WhatsApp's in-memory cache after a daemon restart).
type forgetfulFetcher struct{ spyAdapter }

func (f *forgetfulFetcher) Fetch(ctx context.Context, id string) (core.Item, error) {
	return core.Item{}, fmt.Errorf("whatsapp: fetch %s: %w", id, core.ErrNotFound)
}

// TestServiceFetchFallsBackToStoreWhenAdapterForgot: live bug 2026-09-26,
// `bunker read` on a stored WhatsApp item failed with "item not found"
// after a daemon restart. The stored copy must be returned instead.
func TestServiceFetchFallsBackToStoreWhenAdapterForgot(t *testing.T) {
	stored := core.Item{ID: "whatsapp:wa:1@s.whatsapp.net/M1", Channel: core.ChannelWhatsApp, Account: "wa", Body: "hola"}
	reg := core.NewRegistry()
	reg.Register(&forgetfulFetcher{spyAdapter{channel: core.ChannelWhatsApp, account: "wa"}})
	svc := core.NewService(newMemStore(stored), reg)

	got, err := svc.Fetch(context.Background(), stored.ID)
	if err != nil {
		t.Fatalf("Fetch error = %v, want the stored item", err)
	}
	if got.Body != "hola" {
		t.Fatalf("Fetch = %+v, want the stored copy", got)
	}
}

// TestServiceFetchFallsBackToServerWhenStoreLacksItem: mail-history H1.
// A caller may need INBOX mails at UIDs older than the store's first
// synced UID; those ids 404 from store.Get, but the
// mail adapter's own Fetch (BODY.PEEK) can still get them from the
// server. Fetch must resolve the (channel, account) from the id prefix,
// fetch through the registered Fetcher, upsert the result into the
// store, and return it.
func TestServiceFetchFallsBackToServerWhenStoreLacksItem(t *testing.T) {
	store := newMemStore() // empty: id "mail:cl:5" was never synced
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	got, err := svc.Fetch(context.Background(), "mail:cl:5")
	if err != nil {
		t.Fatalf("Fetch error = %v, want the server-fetched item", err)
	}
	if spy.fetchCalls != 1 {
		t.Fatalf("adapter.Fetch called %d times, want 1", spy.fetchCalls)
	}
	if got.Body != "fetched" {
		t.Fatalf("Fetch body = %q, want fetched", got.Body)
	}

	stored, err := store.Get(context.Background(), "mail:cl:5")
	if err != nil {
		t.Fatalf("store.Get after Fetch error = %v, want the item to be upserted", err)
	}
	if stored.Body != "fetched" {
		t.Fatalf("stored.Body = %q, want fetched", stored.Body)
	}
}

// TestServiceFetchUnknownAccountStaysErrNotFound: an id whose account has
// no registered adapter must never fall back to a different account's
// adapter, and must stay ErrNotFound.
func TestServiceFetchUnknownAccountStaysErrNotFound(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	reg.Register(&spyAdapter{channel: core.ChannelMail, account: "cl"})
	svc := core.NewService(store, reg)

	_, err := svc.Fetch(context.Background(), "mail:unknown:5")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Fetch err = %v, want ErrNotFound", err)
	}
}

// missingFetcher is a Fetcher whose server genuinely doesn't have the
// requested id (e.g. a UID the mailbox never contained).
type missingFetcher struct{ spyAdapter }

func (f *missingFetcher) Fetch(ctx context.Context, id string) (core.Item, error) {
	f.fetchCalls++
	return core.Item{}, fmt.Errorf("mail: fetch %s: %w", id, core.ErrNotFound)
}

// TestServiceFetchServerMissingUIDStaysErrNotFound: the store lacks the
// item AND the server reports it doesn't exist either (not merely "this
// adapter instance forgot it", see TestServiceFetchFallsBackToStoreWhenAdapterForgot,
// which is the opposite case of an adapter Fetcher that never actually
// hits the server). Nothing is upserted.
func TestServiceFetchServerMissingUIDStaysErrNotFound(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	reg.Register(&missingFetcher{spyAdapter{channel: core.ChannelMail, account: "cl"}})
	svc := core.NewService(store, reg)

	_, err := svc.Fetch(context.Background(), "mail:cl:999")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Fetch err = %v, want ErrNotFound", err)
	}
	if _, err := store.Get(context.Background(), "mail:cl:999"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("store.Get = %v, want ErrNotFound (nothing should be upserted)", err)
	}
}

// spyBackfiller is a core.Backfiller double, mail-history H2's core-layer
// dispatch tests: Service.Backfill must resolve the (channel, account)
// adapter, require its Backfiller capability, and pass its own Store
// through untouched (the adapter checks/upserts against it directly).
type spyBackfiller struct {
	spyAdapter
	calls     int
	gotStore  core.Store
	gotFolder string
	gotSince  time.Time
	gotDryRun bool
	result    core.BackfillResult
	err       error
}

func (b *spyBackfiller) Backfill(ctx context.Context, store core.Store, folder string, since time.Time, dryRun bool) (core.BackfillResult, error) {
	b.calls++
	b.gotStore = store
	b.gotFolder = folder
	b.gotSince = since
	b.gotDryRun = dryRun
	return b.result, b.err
}

func TestServiceBackfillDelegatesToAdapter(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	backfiller := &spyBackfiller{
		spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"},
		result:     core.BackfillResult{Count: 3, FirstID: "mail:cl:1.1", LastID: "mail:cl:1.3"},
	}
	reg.Register(backfiller)
	svc := core.NewService(store, reg)

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	result, err := svc.Backfill(context.Background(), core.ChannelMail, "cl", "INBOX", since, false)
	if err != nil {
		t.Fatalf("Backfill error = %v", err)
	}
	if backfiller.calls != 1 {
		t.Fatalf("adapter.Backfill called %d times, want 1", backfiller.calls)
	}
	if backfiller.gotFolder != "INBOX" || !backfiller.gotSince.Equal(since) || backfiller.gotDryRun {
		t.Errorf("Backfill args = folder=%q since=%v dryRun=%v, want INBOX/%v/false", backfiller.gotFolder, backfiller.gotSince, backfiller.gotDryRun, since)
	}
	if backfiller.gotStore == nil {
		t.Error("Backfill did not receive the Service's store")
	}
	if result.Count != 3 || result.FirstID != "mail:cl:1.1" || result.LastID != "mail:cl:1.3" {
		t.Errorf("result = %+v, want the adapter's BackfillResult verbatim", result)
	}
}

func TestServiceBackfillUnsupportedCapability(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	reg.Register(&spyAdapter{channel: core.ChannelMail, account: "cl"})
	svc := core.NewService(store, reg)

	_, err := svc.Backfill(context.Background(), core.ChannelMail, "cl", "INBOX", time.Now(), false)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("Backfill err = %v, want ErrUnsupported", err)
	}
}

func TestServiceBackfillUnknownAccountIsUnsupported(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	svc := core.NewService(store, reg)

	_, err := svc.Backfill(context.Background(), core.ChannelMail, "ghost", "INBOX", time.Now(), false)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("Backfill err = %v, want ErrUnsupported", err)
	}
}

// spySearcher is a core.Searcher double: Service.Search must resolve the
// adapter, require its Searcher capability, and pass its own Store
// through untouched.
type spySearcher struct {
	spyAdapter
	calls       int
	gotStore    core.Store
	gotCriteria core.SearchCriteria
	result      []core.Item
	err         error
}

func (s *spySearcher) Search(ctx context.Context, store core.Store, criteria core.SearchCriteria) ([]core.Item, error) {
	s.calls++
	s.gotStore = store
	s.gotCriteria = criteria
	return s.result, s.err
}

func TestServiceSearchDelegatesToAdapter(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	want := []core.Item{{ID: "mail:cl:1.1", Channel: core.ChannelMail, Account: "cl", Subject: "hi"}}
	searcher := &spySearcher{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}, result: want}
	reg.Register(searcher)
	svc := core.NewService(store, reg)

	criteria := core.SearchCriteria{Folder: "INBOX", From: "alice@x"}
	got, err := svc.Search(context.Background(), core.ChannelMail, "cl", criteria)
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if searcher.calls != 1 {
		t.Fatalf("adapter.Search called %d times, want 1", searcher.calls)
	}
	if searcher.gotCriteria != criteria {
		t.Errorf("Search criteria = %+v, want %+v", searcher.gotCriteria, criteria)
	}
	if searcher.gotStore == nil {
		t.Error("Search did not receive the Service's store")
	}
	if len(got) != 1 || got[0].ID != want[0].ID {
		t.Errorf("Search result = %+v, want %+v", got, want)
	}
}

func TestServiceSearchUnsupportedCapability(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	reg.Register(&spyAdapter{channel: core.ChannelMail, account: "cl"})
	svc := core.NewService(store, reg)

	_, err := svc.Search(context.Background(), core.ChannelMail, "cl", core.SearchCriteria{})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("Search err = %v, want ErrUnsupported", err)
	}
}
