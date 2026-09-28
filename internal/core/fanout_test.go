package core_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// spyFanoutAdapter is a plain Sender/MediaSender (never
// core.MultiRecipientSender) used to prove core.Service fans out
// len(Outgoing.To) > 1 into N sequential single-recipient calls (T13a):
// each recorded call's Outgoing.To has exactly one address, in order, and
// a per-recipient failure can be scripted by address.
type spyFanoutAdapter struct {
	channel core.Channel
	account string

	calls       []core.Outgoing
	errFor      map[string]error
	mediaPolicy core.AttachmentPolicy
	seq         int
}

func (s *spyFanoutAdapter) Channel() core.Channel                         { return s.channel }
func (s *spyFanoutAdapter) Account() string                               { return s.account }
func (s *spyFanoutAdapter) Run(ctx context.Context, sink core.Sink) error { return nil }

func (s *spyFanoutAdapter) Send(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	s.calls = append(s.calls, out)
	s.seq++
	if len(out.To) != 1 {
		return core.Receipt{}, fmt.Errorf("spyFanoutAdapter.Send got %d To, want exactly 1 (fan-out must call once per recipient)", len(out.To))
	}
	if err, ok := s.errFor[out.To[0]]; ok {
		return core.Receipt{}, err
	}
	return core.Receipt{ID: fmt.Sprintf("r-%s", out.To[0]), Channel: s.channel, At: time.Unix(int64(s.seq), 0)}, nil
}

func (s *spyFanoutAdapter) SendMedia(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	return s.Send(ctx, out)
}

func (s *spyFanoutAdapter) AttachmentPolicy() core.AttachmentPolicy { return s.mediaPolicy }

var (
	_ core.Sender      = (*spyFanoutAdapter)(nil)
	_ core.MediaSender = (*spyFanoutAdapter)(nil)
)

// spyFanoutAdapterWithPolicy embeds spyFanoutAdapter and additionally
// implements core.FanoutConfigurer, proving Service honors a
// per-account-configured FanoutPolicy instead of its own defaults.
type spyFanoutAdapterWithPolicy struct {
	spyFanoutAdapter
	policy core.FanoutPolicy
}

func (s *spyFanoutAdapterWithPolicy) FanoutPolicy() core.FanoutPolicy { return s.policy }

var _ core.FanoutConfigurer = (*spyFanoutAdapterWithPolicy)(nil)

// spyNativeMultiRecipientAdapter implements core.MultiRecipientSender
// (like mail): Service must never fan this out, regardless of how many
// recipients are given.
type spyNativeMultiRecipientAdapter struct {
	spyAdapter
}

func (s *spyNativeMultiRecipientAdapter) NativeMultiRecipient() {}

var _ core.MultiRecipientSender = (*spyNativeMultiRecipientAdapter)(nil)

// isZeroReceipt reports whether r carries no result at all — the zero
// Receipt a dry-run must return. Receipt now carries a Recipients slice
// (T13a), so plain struct comparison (r != core.Receipt{}) no longer
// compiles; this file and service_test.go (same core_test package)
// share this helper.
func isZeroReceipt(r core.Receipt) bool {
	return r.ID == "" && r.Channel == "" && r.At.IsZero() && len(r.Recipients) == 0
}

func fixedSleeper(t *testing.T, sleeps *[]time.Duration) func(time.Duration) {
	t.Helper()
	return func(d time.Duration) { *sleeps = append(*sleeps, d) }
}

func fixedChooser(d time.Duration) func(min, max time.Duration) time.Duration {
	return func(min, max time.Duration) time.Duration { return d }
}

// TestServiceSendFanoutCallsAdapterOncePerRecipient covers T13(a): an
// adapter without MultiRecipientSender gets N sequential single-recipient
// Send calls instead of one call carrying every To address.
func TestServiceSendFanoutCallsAdapterOncePerRecipient(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyFanoutAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(spy)
	svc := core.NewService(store, reg)
	var sleeps []time.Duration
	svc.SetSleeper(fixedSleeper(t, &sleeps))
	svc.SetPauseChooser(fixedChooser(time.Millisecond))

	out := core.Outgoing{
		Channel: core.ChannelWhatsApp, Account: "wa",
		To:   []string{"+51111", "+51222", "+51333"},
		Body: "hola a todos",
	}
	plan, receipt, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if len(spy.calls) != 3 {
		t.Fatalf("adapter.Send called %d times, want 3", len(spy.calls))
	}
	for i, want := range []string{"+51111", "+51222", "+51333"} {
		if len(spy.calls[i].To) != 1 || spy.calls[i].To[0] != want {
			t.Fatalf("call %d To = %+v, want [%s]", i, spy.calls[i].To, want)
		}
	}
	// Every recipient must reach the door: N recipients means N-1 pauses.
	if len(sleeps) != 2 {
		t.Fatalf("sleeps = %+v, want 2 (N-1 pauses for 3 recipients)", sleeps)
	}
	if len(receipt.Recipients) != 3 {
		t.Fatalf("receipt.Recipients = %+v, want 3 entries", receipt.Recipients)
	}
	for i, want := range []string{"+51111", "+51222", "+51333"} {
		if receipt.Recipients[i].To != want || receipt.Recipients[i].Error != "" {
			t.Fatalf("receipt.Recipients[%d] = %+v, want To=%s and no error", i, receipt.Recipients[i], want)
		}
	}
	if receipt.ID != "r-+51111" {
		t.Fatalf("top-level receipt = %+v, want the FIRST successful recipient's receipt", receipt)
	}
	if len(plan.Recipients) != 3 {
		t.Fatalf("plan.Recipients = %+v, want 3 entries", plan.Recipients)
	}
}

// TestServiceSendFanoutStoresOneItemPerRecipient covers K7b's fan-out
// clause (conversation-view.md, Usability pass): each successful recipient
// of a broadcast gets its own stored FromMe item, keyed by its own
// receipt id and grouped under its own Thread; a failed recipient must
// not store anything (it never actually sent).
func TestServiceSendFanoutStoresOneItemPerRecipient(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyFanoutAdapter{channel: core.ChannelWhatsApp, account: "wa", errFor: map[string]error{"+51222": errors.New("boom")}}
	reg.Register(spy)
	svc := core.NewService(store, reg)
	var sleeps []time.Duration
	svc.SetSleeper(fixedSleeper(t, &sleeps))
	svc.SetPauseChooser(fixedChooser(time.Millisecond))

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "wa", To: []string{"+51111", "+51222", "+51333"}, Body: "hola a todos"}
	if _, _, err := svc.Send(context.Background(), out, false); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	for _, to := range []string{"+51111", "+51333"} {
		id := fmt.Sprintf("r-%s", to)
		stored, ok := store.items[id]
		if !ok {
			t.Fatalf("recipient %s: no stored item under id %q; store = %+v", to, id, store.items)
		}
		if !stored.FromMe || len(stored.To) != 1 || stored.To[0].ID != to || stored.Thread != to {
			t.Fatalf("recipient %s stored item = %+v", to, stored)
		}
	}
	if _, ok := store.items["r-+51222"]; ok {
		t.Fatal("the failed recipient must not have a stored sent item")
	}
	if len(store.items) != 2 {
		t.Fatalf("store has %d items, want exactly 2 (one per successful recipient): %+v", len(store.items), store.items)
	}
}

// TestServiceSendFanoutPausesBetweenRecipientsWithinPolicyBounds covers
// T13(a)'s 3-8s (default) randomized pause between recipients, using the
// injected chooser so the test never sleeps for real (T13f).
func TestServiceSendFanoutPausesBetweenRecipientsWithinPolicyBounds(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyFanoutAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	var gotMin, gotMax time.Duration
	svc.SetSleeper(func(time.Duration) {})
	svc.SetPauseChooser(func(min, max time.Duration) time.Duration {
		gotMin, gotMax = min, max
		return min
	})

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "wa", To: []string{"+51111", "+51222"}, Body: "hi"}
	if _, _, err := svc.Send(context.Background(), out, false); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if gotMin != 3*time.Second || gotMax != 8*time.Second {
		t.Fatalf("pause bounds = [%v,%v], want the 3-8s default", gotMin, gotMax)
	}
}

// TestServiceSendFanoutOneFailureNeverStopsTheRest covers T13(a): a
// failing recipient is reported, not hidden, and every other recipient
// still gets sent.
func TestServiceSendFanoutOneFailureNeverStopsTheRest(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyFanoutAdapter{
		channel: core.ChannelWhatsApp, account: "wa",
		errFor: map[string]error{"+51222": errors.New("not on whatsapp")},
	}
	reg.Register(spy)
	svc := core.NewService(store, reg)
	svc.SetSleeper(func(time.Duration) {})
	svc.SetPauseChooser(fixedChooser(0))

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "wa", To: []string{"+51111", "+51222", "+51333"}, Body: "hi"}
	_, receipt, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send returned a top-level error = %v, want nil (per-recipient failures are reported, not propagated)", err)
	}
	if len(spy.calls) != 3 {
		t.Fatalf("adapter.Send called %d times, want 3 (one failure must not stop the rest)", len(spy.calls))
	}
	if len(receipt.Recipients) != 3 {
		t.Fatalf("receipt.Recipients = %+v, want 3 entries", receipt.Recipients)
	}
	if receipt.Recipients[1].Error == "" {
		t.Fatalf("receipt.Recipients[1] = %+v, want a non-empty Error for +51222", receipt.Recipients[1])
	}
	if receipt.Recipients[0].Error != "" || receipt.Recipients[2].Error != "" {
		t.Fatalf("receipt.Recipients = %+v, want +51111 and +51333 to succeed", receipt.Recipients)
	}
}

// TestServiceSendFanoutRejectsOverMaxRecipientsBeforeSendingAnything
// covers T13(a)'s limit: exceeding the broadcast recipient cap is an
// error before any recipient is contacted.
func TestServiceSendFanoutRejectsOverMaxRecipientsBeforeSendingAnything(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyFanoutAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	to := make([]string, 11) // over the default 10-recipient limit
	for i := range to {
		to[i] = fmt.Sprintf("+519%02d", i)
	}
	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "wa", To: to, Body: "hi"}
	_, _, err := svc.Send(context.Background(), out, false)
	if !errors.Is(err, core.ErrTooManyRecipients) {
		t.Fatalf("err = %v, want ErrTooManyRecipients", err)
	}
	if len(spy.calls) != 0 {
		t.Fatalf("adapter.Send called %d times, want 0 (must reject before sending anything)", len(spy.calls))
	}
}

// TestServiceSendFanoutDryRunNeverSendsOrSleepsButPlansEveryRecipient
// covers T13(a): dry-run shows the full per-recipient plan and estimated
// pause budget, and sends/sleeps nothing.
func TestServiceSendFanoutDryRunNeverSendsOrSleepsButPlansEveryRecipient(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyFanoutAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(spy)
	svc := core.NewService(store, reg)
	svc.SetSleeper(func(time.Duration) { t.Fatal("dry-run must never sleep") })

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "wa", To: []string{"+51111", "+51222", "+51333"}, Body: "hi"}
	plan, receipt, err := svc.Send(context.Background(), out, true)
	if err != nil {
		t.Fatalf("Send dry-run returned error: %v", err)
	}
	if len(spy.calls) != 0 {
		t.Fatalf("dry-run called adapter.Send %d times, want 0", len(spy.calls))
	}
	if !isZeroReceipt(receipt) {
		t.Fatalf("dry-run returned non-zero receipt: %+v", receipt)
	}
	if len(plan.Recipients) != 3 {
		t.Fatalf("plan.Recipients = %+v, want 3 entries", plan.Recipients)
	}
	if plan.FanoutPauseMin != 2*3*time.Second || plan.FanoutPauseMax != 2*8*time.Second {
		t.Fatalf("plan.FanoutPause[Min,Max] = [%v,%v], want [6s,16s] (2 pauses at the 3-8s default)", plan.FanoutPauseMin, plan.FanoutPauseMax)
	}
}

// TestServiceSendFanoutUsesAdapterConfiguredPolicy covers T13(a): an
// adapter implementing core.FanoutConfigurer overrides Service's built-in
// defaults (per-account configured limits/pacing).
func TestServiceSendFanoutUsesAdapterConfiguredPolicy(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyFanoutAdapterWithPolicy{
		spyFanoutAdapter: spyFanoutAdapter{channel: core.ChannelWhatsApp, account: "wa"},
		policy:           core.FanoutPolicy{MaxRecipients: 2, PauseMin: time.Second, PauseMax: 2 * time.Second},
	}
	reg.Register(spy)
	svc := core.NewService(store, reg)
	svc.SetSleeper(func(time.Duration) {})

	var gotMin, gotMax time.Duration
	svc.SetPauseChooser(func(min, max time.Duration) time.Duration {
		gotMin, gotMax = min, max
		return min
	})

	out := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "wa", To: []string{"+51111", "+51222"}, Body: "hi"}
	if _, _, err := svc.Send(context.Background(), out, false); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if gotMin != time.Second || gotMax != 2*time.Second {
		t.Fatalf("pause bounds = [%v,%v], want the adapter-configured [1s,2s]", gotMin, gotMax)
	}

	// Exceeding the adapter's own configured MaxRecipients (2) must still
	// be rejected before anything is sent.
	out.To = []string{"+51111", "+51222", "+51333"}
	if _, _, err := svc.Send(context.Background(), out, false); !errors.Is(err, core.ErrTooManyRecipients) {
		t.Fatalf("err = %v, want ErrTooManyRecipients for the adapter-configured limit of 2", err)
	}
}

// TestServiceSendNativeMultiRecipientSenderGetsOneCall covers T13(a): an
// adapter implementing core.MultiRecipientSender (mail) must never be
// fanned out, regardless of recipient count.
func TestServiceSendNativeMultiRecipientSenderGetsOneCall(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyNativeMultiRecipientAdapter{spyAdapter{channel: core.ChannelMail, account: "cl"}}
	reg.Register(spy)
	svc := core.NewService(store, reg)
	svc.SetSleeper(func(time.Duration) { t.Fatal("a native multi-recipient send must never sleep between recipients") })

	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"a@b.cl", "c@d.cl"}, Cc: []string{"e@f.cl"}, Body: "hi"}
	_, receipt, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if spy.sendCalls != 1 {
		t.Fatalf("adapter.Send called %d times, want 1 (native multi-recipient, never fanned out)", spy.sendCalls)
	}
	if len(spy.lastOutgoing.To) != 2 {
		t.Fatalf("adapter Outgoing.To = %+v, want both recipients in one call", spy.lastOutgoing.To)
	}
	if len(receipt.Recipients) != 0 {
		t.Fatalf("receipt.Recipients = %+v, want empty for a native multi-recipient call", receipt.Recipients)
	}
}

// TestServiceSendFanoutMediaUsesSendMediaPerRecipient covers T13(a):
// attachments fan out too, using the adapter's MediaSender capability for
// every recipient.
func TestServiceSendFanoutMediaUsesSendMediaPerRecipient(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyFanoutAdapter{channel: core.ChannelWhatsApp, account: "wa", mediaPolicy: imagePolicy(1 << 20)}
	reg.Register(spy)
	svc := core.NewService(store, reg)
	svc.SetSleeper(func(time.Duration) {})

	path := writeTestFile(t, "pic.png", pngSignatureBytes)
	out := core.Outgoing{
		Channel: core.ChannelWhatsApp, Account: "wa",
		To: []string{"+51111", "+51222"}, Body: "mira",
		Attachments: []string{path},
	}
	plan, receipt, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if len(spy.calls) != 2 {
		t.Fatalf("adapter.SendMedia called %d times, want 2", len(spy.calls))
	}
	if len(plan.Attachments) != 1 {
		t.Fatalf("plan.Attachments = %+v, want 1 entry", plan.Attachments)
	}
	if len(receipt.Recipients) != 2 {
		t.Fatalf("receipt.Recipients = %+v, want 2 entries", receipt.Recipients)
	}
}

// TestServiceSendSetsSubjectFromOutgoing covers the Plan.Subject
// addendum: for a fresh send, Plan.Subject mirrors Outgoing.Subject as
// given (empty when none).
func TestServiceSendSetsSubjectFromOutgoing(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"a@b.cl"}, Subject: "hello", Body: "hi"}
	plan, _, err := svc.Send(context.Background(), out, true)
	if err != nil {
		t.Fatalf("Send dry-run returned error: %v", err)
	}
	if plan.Subject != "hello" {
		t.Fatalf("plan.Subject = %q, want %q", plan.Subject, "hello")
	}
}

// TestServiceReplySetsComputedReplySubject covers the Plan.Subject
// addendum: for a reply, Plan.Subject is the original item's subject with
// a computed "Re: " prefix, for approval visibility only (it does not
// change what the adapter actually sends).
func TestServiceReplySetsComputedReplySubject(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", From: core.Address{ID: "them@x.cl"}, Subject: "Meeting notes"}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	plan, _, err := svc.Reply(context.Background(), item.ID, "ack", nil, nil, true)
	if err != nil {
		t.Fatalf("Reply dry-run returned error: %v", err)
	}
	if plan.Subject != "Re: Meeting notes" {
		t.Fatalf("plan.Subject = %q, want %q", plan.Subject, "Re: Meeting notes")
	}
}

// TestServiceReplyAlreadyPrefixedSubjectIsNotDoubled mirrors mail's own
// ReplySubject rule: a subject already carrying a reply prefix (checked
// case-insensitively) is left as-is.
func TestServiceReplyAlreadyPrefixedSubjectIsNotDoubled(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", From: core.Address{ID: "them@x.cl"}, Subject: "RE: Meeting notes"}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	plan, _, err := svc.Reply(context.Background(), item.ID, "ack", nil, nil, true)
	if err != nil {
		t.Fatalf("Reply dry-run returned error: %v", err)
	}
	if plan.Subject != "RE: Meeting notes" {
		t.Fatalf("plan.Subject = %q, want %q (no doubled prefix)", plan.Subject, "RE: Meeting notes")
	}
}

// TestServiceReplySubjectEmptyWhenOriginalHasNone covers "else empty":
// WhatsApp/Matrix items have no Subject, so Plan.Subject stays empty
// rather than becoming a bare "Re:".
func TestServiceReplySubjectEmptyWhenOriginalHasNone(t *testing.T) {
	item := core.Item{ID: "whatsapp:wa:1", Channel: core.ChannelWhatsApp, Account: "wa", From: core.Address{ID: "51999@s.whatsapp.net"}}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	plan, _, err := svc.Reply(context.Background(), item.ID, "ack", nil, nil, true)
	if err != nil {
		t.Fatalf("Reply dry-run returned error: %v", err)
	}
	if plan.Subject != "" {
		t.Fatalf("plan.Subject = %q, want empty", plan.Subject)
	}
}

// TestServiceReplySetsPlanRecipientsToOriginalSender covers T13(g)'s
// generalization: Plan.Recipients is populated for reply too (the
// original sender), so the CLI's human dry-run line can show "to [...]"
// consistently for both send and reply.
func TestServiceReplySetsPlanRecipientsToOriginalSender(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", From: core.Address{ID: "them@x.cl"}}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	plan, _, err := svc.Reply(context.Background(), item.ID, "ack", nil, nil, true)
	if err != nil {
		t.Fatalf("Reply dry-run returned error: %v", err)
	}
	if len(plan.Recipients) != 1 || plan.Recipients[0] != "them@x.cl" {
		t.Fatalf("plan.Recipients = %+v, want [them@x.cl]", plan.Recipients)
	}
}

// --- T13(c): core.ReadMarker / Service.Read ---

// spyReadMarkerAdapter implements Fetcher + ReadMarker, used to prove
// Service.Read marks an item read on the channel and in the store when
// the adapter supports it.
type spyReadMarkerAdapter struct {
	spyAdapter
	markReadCalls []string
	markReadErr   error
}

func (s *spyReadMarkerAdapter) MarkRead(ctx context.Context, id string) error {
	s.markReadCalls = append(s.markReadCalls, id)
	return s.markReadErr
}

var _ core.ReadMarker = (*spyReadMarkerAdapter)(nil)

// TestServiceReadMarksReadWhenAdapterSupportsIt covers T13(c): Read
// fetches the full body and, when the adapter implements ReadMarker,
// marks it read both on the channel and in the store.
func TestServiceReadMarksReadWhenAdapterSupportsIt(t *testing.T) {
	item := core.Item{ID: "whatsapp:wa:1", Channel: core.ChannelWhatsApp, Account: "wa", Unread: true}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyReadMarkerAdapter{spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "wa"}}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	got, err := svc.Read(context.Background(), item.ID, true)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if got.Unread {
		t.Error("Read() item.Unread = true, want false")
	}
	if len(spy.markReadCalls) != 1 || spy.markReadCalls[0] != item.ID {
		t.Fatalf("markReadCalls = %+v, want [%s]", spy.markReadCalls, item.ID)
	}
	stored, err := store.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if stored.Unread {
		t.Error("stored item.Unread = true after Read, want false")
	}
}

// TestServiceReadWithMarkReceiptFalseNeverMarksAnything covers --no-receipt:
// Read still fetches the body but skips the mark-read step entirely.
func TestServiceReadWithMarkReceiptFalseNeverMarksAnything(t *testing.T) {
	item := core.Item{ID: "whatsapp:wa:1", Channel: core.ChannelWhatsApp, Account: "wa", Unread: true}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyReadMarkerAdapter{spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "wa"}}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	if _, err := svc.Read(context.Background(), item.ID, false); err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if len(spy.markReadCalls) != 0 {
		t.Fatalf("markReadCalls = %+v, want none with markReceipt=false", spy.markReadCalls)
	}
	// The store, not the Fetcher's own returned item, is the reliable
	// witness that nothing was marked read.
	stored, err := store.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if !stored.Unread {
		t.Error("stored item.Unread = false after Read(markReceipt=false), want it untouched")
	}
}

// TestServiceReadNeverMarksReadWithoutReadMarkerCapability covers mail's
// PEEK guarantee: an adapter that does not implement ReadMarker (mail)
// must never have anything marked read, even with markReceipt=true — the
// store's Unread flag stays exactly as Fetch found it.
func TestServiceReadNeverMarksReadWithoutReadMarkerCapability(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Unread: true}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"} // no ReadMarker
	reg.Register(spy)
	svc := core.NewService(store, reg)

	if _, err := svc.Read(context.Background(), item.ID, true); err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	stored, err := store.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if !stored.Unread {
		t.Error("stored mail item.Unread = false after Read, want it untouched (PEEK)")
	}
}

// TestServiceSendFanoutRejectsCcBeforeSendingAnything: a chat broadcast has
// no "carbon copy"; silently stripping Cc would drop recipients the user
// asked for, so it must be an error before any recipient is sent to.
func TestServiceSendFanoutRejectsCcBeforeSendingAnything(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	spy := &spyFanoutAdapter{channel: core.ChannelWhatsApp, account: "wa"}
	reg.Register(spy)
	svc := core.NewService(store, reg)
	var sleeps []time.Duration
	svc.SetSleeper(fixedSleeper(t, &sleeps))
	svc.SetPauseChooser(fixedChooser(time.Millisecond))

	out := core.Outgoing{
		Channel: core.ChannelWhatsApp, Account: "wa",
		To:   []string{"+51111", "+51222"},
		Cc:   []string{"+51333"},
		Body: "hola",
	}
	for _, dry := range []bool{true, false} {
		_, _, err := svc.Send(context.Background(), out, dry)
		if !errors.Is(err, core.ErrUnsupported) {
			t.Fatalf("Send(dryRun=%v) error = %v, want ErrUnsupported for Cc on a broadcast", dry, err)
		}
	}
	if len(spy.calls) != 0 {
		t.Fatalf("adapter.Send called %d times, want 0", len(spy.calls))
	}
}
