package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// queueClock is a fake time.Now() source for adapterSupervisor tests: it
// returns each of times in order, then keeps returning the last one
// forever, so a test can script exactly how long each Run call "lasted"
// without ever sleeping for real.
type queueClock struct {
	mu    sync.Mutex
	times []time.Time
	idx   int
}

func (q *queueClock) now() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := q.times[q.idx]
	if q.idx < len(q.times)-1 {
		q.idx++
	}
	return t
}

// backoffSpy records every attempt adapterSupervisor.backoff is called
// with, and returns a tiny constant duration so tests never wait for a
// real production-sized backoff.
type backoffSpy struct {
	mu       sync.Mutex
	attempts []int
}

func (b *backoffSpy) call(attempt int) time.Duration {
	b.mu.Lock()
	b.attempts = append(b.attempts, attempt)
	b.mu.Unlock()
	return time.Millisecond
}

func (b *backoffSpy) seen() []int {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]int, len(b.attempts))
	copy(out, b.attempts)
	return out
}

// scriptedAdapter is a core.Adapter whose Run returns each of errs in
// order on successive calls, then blocks on ctx.Done() forever (like a
// real adapter that finally reconnected) so tests can assert on an exact,
// bounded number of restarts before canceling ctx.
type scriptedAdapter struct {
	channel core.Channel
	account string

	mu   sync.Mutex
	errs []error
	idx  int
}

func (a *scriptedAdapter) Channel() core.Channel { return a.channel }
func (a *scriptedAdapter) Account() string       { return a.account }

func (a *scriptedAdapter) Run(ctx context.Context, _ core.Sink) error {
	a.mu.Lock()
	i := a.idx
	a.idx++
	a.mu.Unlock()
	if i < len(a.errs) {
		return a.errs[i]
	}
	<-ctx.Done()
	return ctx.Err()
}

func (a *scriptedAdapter) runCalls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.idx
}

var _ core.Adapter = (*scriptedAdapter)(nil)

func waitForRunCalls(t *testing.T, a *scriptedAdapter, want int) {
	t.Helper()
	if pollUntil(2*time.Second, func() bool { return a.runCalls() >= want }) {
		return
	}
	t.Fatalf("runCalls = %d, want at least %d", a.runCalls(), want)
}

// TestAdapterSupervisorRestartsAndGrowsBackoffOnRepeatedFailures proves
// R1's core behavior: a Run that returns while ctx is live is restarted,
// and the backoff attempt handed to the injected backoff func keeps
// growing across back-to-back short-lived failures.
func TestAdapterSupervisorRestartsAndGrowsBackoffOnRepeatedFailures(t *testing.T) {
	a := &scriptedAdapter{channel: core.ChannelWhatsApp, account: "demo", errs: []error{
		errors.New("e1"), errors.New("e2"), errors.New("e3"), errors.New("e4"), errors.New("e5"),
	}}
	clock := &queueClock{times: []time.Time{time.Unix(0, 0)}}
	spy := &backoffSpy{}
	sup := &adapterSupervisor{
		now:     clock.now,
		after:   time.After,
		backoff: spy.call,
		logger:  slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sup.supervise(ctx, a, nil)
		close(done)
	}()

	waitForRunCalls(t, a, 6) // 5 scripted failures + the final ctx-blocking call
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise did not return after ctx cancel")
	}

	if got, want := spy.seen(), []int{1, 2, 3, 4, 5}; !equalInts(got, want) {
		t.Fatalf("backoff attempts = %v, want %v (monotonically growing)", got, want)
	}
	if calls := a.runCalls(); calls != 6 {
		t.Fatalf("Run was called %d times, want exactly 6 (no restart after cancel)", calls)
	}
}

// TestAdapterSupervisorResetsBackoffAfterHealthyRun proves the other half
// of R1's policy: a run that lasted at least the healthy threshold resets
// the attempt count, so the next restart waits the base backoff again
// instead of continuing to grow as if still crash-looping.
func TestAdapterSupervisorResetsBackoffAfterHealthyRun(t *testing.T) {
	a := &scriptedAdapter{channel: core.ChannelMail, account: "demo", errs: []error{
		errors.New("e1"), errors.New("e2"), errors.New("e3"), errors.New("e4"), errors.New("e5"),
	}}
	t0 := time.Unix(0, 0)
	t1 := t0.Add(adapterHealthyRun) // run index 2 (3rd call) lasts exactly the healthy threshold
	clock := &queueClock{times: []time.Time{
		t0, t0, // run0: start,end (elapsed 0)
		t0, t0, // run1: start,end (elapsed 0)
		t0, t1, // run2: start,end (elapsed == adapterHealthyRun -> healthy)
		t1, t1, // run3: start,end (elapsed 0)
		t1, t1, // run4: start,end (elapsed 0)
		t1, // run5 (final, ctx-blocking): start only
	}}
	spy := &backoffSpy{}
	sup := &adapterSupervisor{
		now:     clock.now,
		after:   time.After,
		backoff: spy.call,
		logger:  slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sup.supervise(ctx, a, nil)
		close(done)
	}()

	waitForRunCalls(t, a, 6)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise did not return after ctx cancel")
	}

	if got, want := spy.seen(), []int{1, 2, 1, 2, 3}; !equalInts(got, want) {
		t.Fatalf("backoff attempts = %v, want %v (reset to 1 after the healthy 3rd run)", got, want)
	}
}

// TestAdapterSupervisorLogsRestartWithChannelAccountAttrs proves the
// slog lifecycle logging R1 requires: every restart is logged at error
// level (the run failed) with channel/account attributes.
func TestAdapterSupervisorLogsRestartWithChannelAccountAttrs(t *testing.T) {
	a := &scriptedAdapter{channel: core.ChannelWhatsApp, account: "demo-acct", errs: []error{errors.New("boom")}}
	clock := &queueClock{times: []time.Time{time.Unix(0, 0)}}
	var logBuf bytes.Buffer
	sup := &adapterSupervisor{
		now:     clock.now,
		after:   time.After,
		backoff: func(int) time.Duration { return time.Millisecond },
		logger:  slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sup.supervise(ctx, a, nil)
		close(done)
	}()

	waitForRunCalls(t, a, 2)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise did not return after ctx cancel")
	}

	out := logBuf.String()
	if !strings.Contains(out, "level=ERROR") {
		t.Fatalf("log output = %q, want an ERROR level entry", out)
	}
	if !strings.Contains(out, "channel=whatsapp") {
		t.Fatalf("log output = %q, want a channel=whatsapp attribute", out)
	}
	if !strings.Contains(out, "account=demo-acct") {
		t.Fatalf("log output = %q, want an account=demo-acct attribute", out)
	}
}

// TestAdapterSupervisorTracksHealthTransitions proves R4: with a
// HealthTracker wired and a positive connectGrace, supervise records
// connecting -> backoff (the first run fails almost immediately, well
// before connectGrace elapses) -> connected (the second run keeps going
// past connectGrace) -> stopped (ctx canceled while the second run is
// still live). This exercises the real connectGrace/Run race, so it uses
// real time (generous margins) rather than the fake clock the other
// supervisor tests use.
func TestAdapterSupervisorTracksHealthTransitions(t *testing.T) {
	a := &scriptedAdapter{channel: core.ChannelWhatsApp, account: "demo", errs: []error{errors.New("boom")}}
	health := core.NewHealthTracker()
	// The backoff wait is held open until the test has seen the backoff
	// state, so polling cannot miss a state that would otherwise last
	// only a few milliseconds.
	backoffGate := make(chan time.Time)
	const grace = 100 * time.Millisecond
	sup := &adapterSupervisor{
		now: time.Now,
		after: func(d time.Duration) <-chan time.Time {
			if d == grace {
				return time.After(d)
			}
			return backoffGate
		},
		backoff:      func(int) time.Duration { return 5 * time.Millisecond },
		logger:       slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		health:       health,
		connectGrace: grace,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sup.supervise(ctx, a, nil)
		close(done)
	}()

	waitForHealthState(t, health, core.AdapterBackoff)
	snap := health.Snapshot()
	if len(snap) != 1 || snap[0].LastError == "" || snap[0].Restarts != 1 {
		t.Fatalf("health after first failure = %+v, want one backoff entry with Restarts=1 and a LastError", snap)
	}

	close(backoffGate)

	waitForHealthState(t, health, core.AdapterConnected)
	snap = health.Snapshot()
	if snap[0].Restarts != 1 {
		t.Fatalf("health after reaching connected = %+v, want Restarts still 1", snap)
	}

	cancel()
	waitForHealthState(t, health, core.AdapterStopped)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise did not return after ctx cancel")
	}
}

func waitForHealthState(t *testing.T, health *core.HealthTracker, want core.AdapterState) {
	t.Helper()
	if pollUntil(2*time.Second, func() bool {
		snap := health.Snapshot()
		return len(snap) == 1 && snap[0].State == want
	}) {
		return
	}
	t.Fatalf("health never reached state %q, snapshot = %+v", want, health.Snapshot())
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// blockingRetrierAdapter blocks in RetryUndecryptable until released,
// like the live Matrix adapter re-fetching hundreds of undecryptable
// events at startup.
type blockingRetrierAdapter struct {
	scriptedAdapter
	release chan struct{}
}

func (a *blockingRetrierAdapter) RetryUndecryptable(ctx context.Context, _ core.Store) error {
	select {
	case <-a.release:
	case <-ctx.Done():
	}
	return nil
}

// TestAdapterSupervisorReportsConnectingDuringRetryUndecryptable: live
// finding after install — Matrix was missing from `bunker health` for
// as long as its startup retry ran, because the tracker only learned of
// the adapter once Run started.
func TestAdapterSupervisorReportsConnectingDuringRetryUndecryptable(t *testing.T) {
	a := &blockingRetrierAdapter{
		scriptedAdapter: scriptedAdapter{channel: core.ChannelMatrix, account: "demo"},
		release:         make(chan struct{}),
	}
	health := core.NewHealthTracker()
	sup := &adapterSupervisor{
		now:          time.Now,
		after:        time.After,
		backoff:      func(int) time.Duration { return 5 * time.Millisecond },
		logger:       slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		health:       health,
		connectGrace: time.Hour,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sup.supervise(ctx, a, nil)

	waitForHealthState(t, health, core.AdapterConnecting)
	if a.runCalls() != 0 {
		t.Fatalf("Run called %d times while RetryUndecryptable still blocks, want 0", a.runCalls())
	}
	close(a.release)
}
