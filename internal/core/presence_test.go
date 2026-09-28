package core_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// fakeAfterFunc is a deterministic, controllable stand-in for
// time.AfterFunc: tests fire a scheduled callback by calling fire()
// directly instead of waiting a real duration, and observe whether it
// was ever canceled (stopped) before firing.
type fakeAfterFunc struct {
	mu        sync.Mutex
	scheduled []*fakeTimer
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
	fired   bool
}

func (fa *fakeAfterFunc) after(d time.Duration, f func()) func() bool {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	t := &fakeTimer{d: d, f: f}
	fa.scheduled = append(fa.scheduled, t)
	return func() bool {
		fa.mu.Lock()
		defer fa.mu.Unlock()
		if t.stopped || t.fired {
			return false
		}
		t.stopped = true
		return true
	}
}

// fireLatest invokes the most recently scheduled, still-pending timer's
// callback synchronously, as if its duration had elapsed. It fails the
// test if there is no such timer.
func (fa *fakeAfterFunc) fireLatest(t *testing.T) {
	t.Helper()
	fa.mu.Lock()
	var target *fakeTimer
	for i := len(fa.scheduled) - 1; i >= 0; i-- {
		if !fa.scheduled[i].stopped && !fa.scheduled[i].fired {
			target = fa.scheduled[i]
			break
		}
	}
	if target != nil {
		target.fired = true
	}
	fa.mu.Unlock()
	if target == nil {
		t.Fatal("fireLatest: no pending timer")
	}
	target.f()
}

// presenceAdapter is a minimal core.Adapter that also implements
// core.PresenceAvailabilityController, recording every
// SetPresenceAvailable call's (available, thread) pair in order — the
// "exact SendPresence sequence" the daemon must produce.
type presenceAdapter struct {
	channel core.Channel
	account string

	mu    sync.Mutex
	calls []presenceCall
	err   error
}

type presenceCall struct {
	Available bool
	Thread    string
}

func (p *presenceAdapter) Channel() core.Channel                { return p.channel }
func (p *presenceAdapter) Account() string                      { return p.account }
func (p *presenceAdapter) Run(context.Context, core.Sink) error { return nil }

func (p *presenceAdapter) SetPresenceAvailable(_ context.Context, available bool, thread string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.calls = append(p.calls, presenceCall{Available: available, Thread: thread})
	return nil
}

func (p *presenceAdapter) callLog() []presenceCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]presenceCall, len(p.calls))
	copy(out, p.calls)
	return out
}

func newPresenceService(t *testing.T, adapter *presenceAdapter) (*core.Service, *fakeAfterFunc) {
	t.Helper()
	reg := core.NewRegistry()
	reg.Register(adapter)
	svc := core.NewService(newMemStore(), reg)
	fa := &fakeAfterFunc{}
	svc.SetPresenceAfterFunc(fa.after)
	return svc, fa
}

// TestPresenceKeepaliveFocusedGrantsAvailabilityAndSubscribes proves the
// default-unavailable lease (odd/tasks/conversation-view.md's privacy
// Decision): the very first call, focused=true, must broadcast available
// and subscribe to thread — never anything before that first call.
func TestPresenceKeepaliveFocusedGrantsAvailabilityAndSubscribes(t *testing.T) {
	adapter := &presenceAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	svc, _ := newPresenceService(t, adapter)

	if got := adapter.callLog(); len(got) != 0 {
		t.Fatalf("before any keepalive, calls = %+v, want none (default unavailable)", got)
	}

	if err := svc.PresenceKeepalive(context.Background(), "whatsapp", "personal", "5511999999999@s.whatsapp.net", true); err != nil {
		t.Fatalf("PresenceKeepalive: %v", err)
	}

	want := []presenceCall{{Available: true, Thread: "5511999999999@s.whatsapp.net"}}
	if got := adapter.callLog(); !equalPresenceCalls(got, want) {
		t.Fatalf("calls = %+v, want %+v", got, want)
	}
}

// TestPresenceKeepaliveBlurRevokesImmediately covers "it reverts ... on
// leave, blur, quit": focused=false must broadcast unavailable right
// away, without waiting for the 60s timeout, and must not fire again
// once already unavailable.
func TestPresenceKeepaliveBlurRevokesImmediately(t *testing.T) {
	adapter := &presenceAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	svc, _ := newPresenceService(t, adapter)
	ctx := context.Background()
	thread := "5511999999999@s.whatsapp.net"

	if err := svc.PresenceKeepalive(ctx, "whatsapp", "personal", thread, true); err != nil {
		t.Fatalf("keepalive(focused=true): %v", err)
	}
	if err := svc.PresenceKeepalive(ctx, "whatsapp", "personal", thread, false); err != nil {
		t.Fatalf("keepalive(focused=false): %v", err)
	}

	want := []presenceCall{
		{Available: true, Thread: thread},
		{Available: false, Thread: thread},
	}
	if got := adapter.callLog(); !equalPresenceCalls(got, want) {
		t.Fatalf("calls = %+v, want %+v", got, want)
	}
}

// TestPresenceKeepaliveExpiresAfterTimeoutWithoutRenewal covers "or
// after 60s without a keepalive from the TUI": the fake timer firing
// (simulating PresenceLeaseTimeout elapsing) must revoke availability on
// its own, with no further calls needed.
func TestPresenceKeepaliveExpiresAfterTimeoutWithoutRenewal(t *testing.T) {
	adapter := &presenceAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	svc, fa := newPresenceService(t, adapter)
	ctx := context.Background()
	thread := "5511999999999@s.whatsapp.net"

	if err := svc.PresenceKeepalive(ctx, "whatsapp", "personal", thread, true); err != nil {
		t.Fatalf("keepalive: %v", err)
	}
	fa.fireLatest(t) // simulate 60s elapsing with no renewal

	want := []presenceCall{
		{Available: true, Thread: thread},
		{Available: false, Thread: thread},
	}
	if got := adapter.callLog(); !equalPresenceCalls(got, want) {
		t.Fatalf("calls = %+v, want %+v", got, want)
	}
}

// TestPresenceKeepaliveRenewalStopsThePreviousTimer proves each
// focused=true renewal cancels the previous 60s timer instead of
// stacking a second one that would otherwise fire and incorrectly
// revoke availability out from under a still-open, still-focused chat
// view.
func TestPresenceKeepaliveRenewalStopsThePreviousTimer(t *testing.T) {
	adapter := &presenceAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	svc, fa := newPresenceService(t, adapter)
	ctx := context.Background()
	thread := "5511999999999@s.whatsapp.net"

	if err := svc.PresenceKeepalive(ctx, "whatsapp", "personal", thread, true); err != nil {
		t.Fatalf("keepalive 1: %v", err)
	}
	if err := svc.PresenceKeepalive(ctx, "whatsapp", "personal", thread, true); err != nil {
		t.Fatalf("keepalive 2 (renewal): %v", err)
	}

	// Only one SetPresenceAvailable(true, ...) call: the second keepalive
	// renews the same grant, it does not re-broadcast available.
	want := []presenceCall{{Available: true, Thread: thread}}
	if got := adapter.callLog(); !equalPresenceCalls(got, want) {
		t.Fatalf("calls after two focused keepalives = %+v, want %+v", got, want)
	}

	fa.mu.Lock()
	n := len(fa.scheduled)
	fa.mu.Unlock()
	if n != 2 {
		t.Fatalf("scheduled timers = %d, want 2 (one per keepalive, the first stopped)", n)
	}
	fa.mu.Lock()
	firstStopped := fa.scheduled[0].stopped
	fa.mu.Unlock()
	if !firstStopped {
		t.Fatalf("the first keepalive's timer was not stopped by the renewal")
	}
}

// TestShutdownPresenceRevokesEveryAvailableLease covers "or daemon
// shutdown": a lease left available must be revoked once, exactly like
// an explicit blur.
func TestShutdownPresenceRevokesEveryAvailableLease(t *testing.T) {
	adapter := &presenceAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	svc, _ := newPresenceService(t, adapter)
	thread := "5511999999999@s.whatsapp.net"

	if err := svc.PresenceKeepalive(context.Background(), "whatsapp", "personal", thread, true); err != nil {
		t.Fatalf("keepalive: %v", err)
	}
	svc.ShutdownPresence()

	want := []presenceCall{
		{Available: true, Thread: thread},
		{Available: false, Thread: thread},
	}
	if got := adapter.callLog(); !equalPresenceCalls(got, want) {
		t.Fatalf("calls = %+v, want %+v", got, want)
	}

	// Shutdown again must not re-fire an already-revoked lease.
	svc.ShutdownPresence()
	if got := adapter.callLog(); !equalPresenceCalls(got, want) {
		t.Fatalf("calls after second Shutdown = %+v, want unchanged %+v", got, want)
	}
}

// TestPresenceKeepaliveWithoutControllerIsNoop covers Matrix (and any
// other channel without PresenceAvailabilityController): the
// availability lease has nothing to gate, so PresenceKeepalive succeeds
// without ever calling anything.
func TestPresenceKeepaliveWithoutControllerIsNoop(t *testing.T) {
	reg := core.NewRegistry()
	reg.Register(bareAdapter{channel: core.ChannelMatrix, account: "work"})
	svc := core.NewService(newMemStore(), reg)

	if err := svc.PresenceKeepalive(context.Background(), "matrix", "work", "!room:example.org", true); err != nil {
		t.Fatalf("PresenceKeepalive: %v", err)
	}
}

// TestTypingRequiresThreadAndCapability covers Service.Typing's
// validation: an empty thread and a channel without TypingSender (mail)
// must both fail with ErrUnsupported, never silently succeed.
func TestTypingRequiresThreadAndCapability(t *testing.T) {
	reg := core.NewRegistry()
	reg.Register(bareAdapter{channel: core.ChannelMail, account: "cl"})
	svc := core.NewService(newMemStore(), reg)

	if err := svc.Typing(context.Background(), "mail", "cl", "", true); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("Typing with empty thread: err = %v, want ErrUnsupported", err)
	}
	if err := svc.Typing(context.Background(), "mail", "cl", "t1", true); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("Typing on mail (no TypingSender): err = %v, want ErrUnsupported", err)
	}
}

// TestPresenceWithoutProviderReportsUnknown covers mail (and any
// channel without PresenceProvider): Service.Presence never errors for
// this, it reports State "unknown".
func TestPresenceWithoutProviderReportsUnknown(t *testing.T) {
	reg := core.NewRegistry()
	reg.Register(bareAdapter{channel: core.ChannelMail, account: "cl"})
	svc := core.NewService(newMemStore(), reg)

	p, err := svc.Presence(context.Background(), "mail", "cl", "t1")
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.State != "unknown" {
		t.Fatalf("State = %q, want unknown", p.State)
	}
}

func equalPresenceCalls(got, want []presenceCall) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
