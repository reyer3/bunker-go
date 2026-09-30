package mail

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

// syncBuffer is an io.Writer safe for one goroutine to write (Run's slog
// handler) while another concurrently reads String() (the test's poll
// loop), avoiding a data race on a plain bytes.Buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// failingDial is a dialFunc that always fails immediately with err,
// without touching any network, so Run's runOnce loop can be exercised
// (R2's backoff/reset/logging policy) at real-clock speed regardless of
// how long a real IMAP connect would take.
func failingDial(err error) dialFunc {
	return func(_ context.Context, _ AccountConfig, _ PasswordSource, _ TokenSource, _ *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
		return nil, err
	}
}

// TestAdapterRunResetsBackoffAfterHealthySession proves R2: after a
// session (one runOnce call) lasts at least mailHealthySession, Run's
// attempt count resets, so the next reconnect waits the base backoff
// again instead of staying at whatever the cap had grown to.
func TestAdapterRunResetsBackoffAfterHealthySession(t *testing.T) {
	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, failingDial(errors.New("dial refused")))

	var attemptsMu attemptsRecorder
	adapter.backoff = attemptsMu.record

	t0 := time.Unix(0, 0)
	t1 := t0.Add(mailHealthySession)
	times := []time.Time{
		t0, t0, // session0: start,end (elapsed 0)                     -> attempt 1
		t0, t0, // session1: start,end (elapsed 0)                     -> attempt 2
		t0, t1, // session2: start,end (elapsed == mailHealthySession) -> healthy, reset to attempt 1
		t1, // session3's start (Run keeps going until ctx is canceled)
	}
	var timesMu sync.Mutex
	idx := 0
	adapter.now = func() time.Time {
		timesMu.Lock()
		defer timesMu.Unlock()
		tm := times[idx]
		if idx < len(times)-1 {
			idx++
		}
		return tm
	}

	ctx, cancel := context.WithCancel(context.Background())
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	pollUntil(2*time.Second, func() bool { return len(attemptsMu.seen()) >= 3 })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	got := attemptsMu.seen()
	if len(got) < 3 {
		t.Fatalf("attempts = %v, want at least 3 recorded", got)
	}
	if want := ([]int{1, 2, 1}); got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("attempts = %v, want %v (grow, then reset to 1 after the healthy session)", got[:3], want)
	}
}

// attemptsRecorder records every attempt Run's backoff is called with.
// Run calls it from its own goroutine while the test polls seen() from
// the main one, so both methods lock.
type attemptsRecorder struct {
	mu       sync.Mutex
	attempts []int
}

func (r *attemptsRecorder) record(attempt int) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, attempt)
	return time.Millisecond
}

func (r *attemptsRecorder) seen() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int, len(r.attempts))
	copy(out, r.attempts)
	return out
}

// TestAdapterRunLogsRunOnceErrorWithChannelAccountAttrs proves R2's other
// half: a runOnce failure (previously discarded with `_ = err`) is logged
// at error level with channel/account attributes.
func TestAdapterRunLogsRunOnceErrorWithChannelAccountAttrs(t *testing.T) {
	cfg := AccountConfig{Name: "cl2", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, failingDial(errors.New("boom-dial")))
	adapter.backoff = func(int) time.Duration { return time.Millisecond }

	logBuf := &syncBuffer{}
	adapter.logger = slog.New(slog.NewTextHandler(logBuf, nil))

	ctx, cancel := context.WithCancel(context.Background())
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	pollUntil(2*time.Second, func() bool { return strings.Contains(logBuf.String(), "boom-dial") })
	cancel()
	<-done

	out := logBuf.String()
	if !strings.Contains(out, "level=ERROR") {
		t.Fatalf("log output = %q, want an ERROR level entry", out)
	}
	if !strings.Contains(out, "channel=mail") {
		t.Fatalf("log output = %q, want a channel=mail attribute", out)
	}
	if !strings.Contains(out, "account=cl2") {
		t.Fatalf("log output = %q, want an account=cl2 attribute", out)
	}
	if !strings.Contains(out, "boom-dial") {
		t.Fatalf("log output = %q, want the runOnce error message", out)
	}
}
