package main

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// adapterBackoffBase and adapterBackoffCap bound the daemon's restart
// delay (R1): 1s, doubling, capped at 5 minutes.
const (
	adapterBackoffBase = time.Second
	adapterBackoffCap  = 5 * time.Minute

	// adapterHealthyRun is how long a Run must have lasted for
	// adapterSupervisor to treat the adapter as healthy again and reset
	// its attempt count to zero, rather than keep growing the backoff as
	// if it were still crash-looping.
	adapterHealthyRun = 2 * time.Minute

	// defaultConnectGrace is how long a (re)started Run must keep running
	// without returning before R4's health tracking marks the adapter
	// "connected" rather than "connecting": real adapters have no
	// dedicated "I'm authenticated now" signal back to the supervisor, so
	// this is the default heuristic (still running past a real dial's
	// typical duration = probably connected). It only takes effect when a
	// HealthTracker is wired; supervise's original R1 behavior (call Run
	// directly, no extra goroutine) is unchanged otherwise.
	defaultConnectGrace = 2 * time.Second
)

// adapterSupervisor restarts one adapter's Run loop for as long as ctx is
// live (R1). Before this, startAdapters logged "stopped" and let the
// goroutine end for good: systemd's Restart=on-failure only covers the
// whole process, so a single adapter's Run returning (a Matrix sync
// error, an IMAP connection drop past its own retry loop, ...) silently
// dropped that channel for the rest of the process's life. now/after/
// backoff are injectable so tests can prove the growing/reset backoff
// deterministically, without a real sleep.
type adapterSupervisor struct {
	now     func() time.Time
	after   func(d time.Duration) <-chan time.Time
	backoff func(attempt int) time.Duration
	logger  *slog.Logger

	// health and connectGrace back R4's health tracking. health is nil
	// in every R1 test (and any caller that does not need it): supervise
	// then skips the tracker entirely and, critically, also skips the
	// extra goroutine+timer race connectGrace would otherwise need,
	// calling a.Run directly exactly as R1 originally did. connectGrace
	// <= 0 has the same effect even with a non-nil health, so a test can
	// wire a tracker without opting into the grace-period race.
	health       *core.HealthTracker
	connectGrace time.Duration
}

// newAdapterSupervisor returns the production adapterSupervisor: a real
// clock, real timers, the capped-exponential-with-jitter backoff, and R4
// health tracking through health (nil disables it), logging through
// logger.
func newAdapterSupervisor(logger *slog.Logger, health *core.HealthTracker) *adapterSupervisor {
	return &adapterSupervisor{
		now:          time.Now,
		after:        time.After,
		backoff:      defaultAdapterBackoff,
		logger:       logger,
		health:       health,
		connectGrace: defaultConnectGrace,
	}
}

// defaultAdapterBackoff is the production restart delay: exponential from
// adapterBackoffBase, capped at adapterBackoffCap, with up to 50% jitter
// so several adapters restarting together (e.g. after a shared network
// blip) don't all retry in lockstep.
func defaultAdapterBackoff(attempt int) time.Duration {
	d := adapterBackoffBase
	for i := 1; i < attempt && d < adapterBackoffCap; i++ {
		d *= 2
	}
	if d > adapterBackoffCap {
		d = adapterBackoffCap
	}
	jitter := time.Duration(rand.Int63n(int64(d)/2 + 1))
	return d - jitter
}

// supervise gives a one chance at RetryUndecryptable (if it implements
// core.Retrier), then runs a, and keeps restarting it after a backoff for
// as long as ctx is live. It returns only once ctx is done. When a
// HealthTracker is wired (R4), it also records channel/account's
// connecting/connected/backoff/stopped transitions.
func (s *adapterSupervisor) supervise(ctx context.Context, a core.Adapter, st core.Store) {
	channel, account := a.Channel(), a.Account()
	logger := s.logger.With("channel", string(channel), "account", account)

	if retrier, ok := a.(core.Retrier); ok {
		// The startup retry can take minutes; report the adapter as
		// connecting meanwhile instead of leaving it out of health.
		if s.health != nil {
			s.health.SetConnecting(channel, account, s.now())
		}
		if err := retrier.RetryUndecryptable(ctx, st); err != nil {
			logger.Error("retry undecryptable", "error", err)
		}
	}

	attempt := 0
	for {
		if s.health != nil {
			s.health.SetConnecting(channel, account, s.now())
		}

		start := s.now()
		err := s.runOnce(ctx, a, st, channel, account)

		if ctx.Err() != nil {
			if s.health != nil {
				s.health.SetStopped(channel, account, s.now(), nil)
			}
			return
		}
		if s.now().Sub(start) >= adapterHealthyRun {
			attempt = 0
		}
		attempt++

		if err != nil {
			logger.Error("adapter run stopped, restarting", "error", err, "attempt", attempt)
		} else {
			logger.Warn("adapter run stopped, restarting", "attempt", attempt)
		}
		if s.health != nil {
			s.health.SetBackoff(channel, account, s.now(), err)
		}

		d := s.backoff(attempt)
		select {
		case <-ctx.Done():
			if s.health != nil {
				s.health.SetStopped(channel, account, s.now(), nil)
			}
			return
		case <-s.after(d):
		}
	}
}

// runOnce calls a.Run once. Without health tracking (or with
// connectGrace <= 0) it calls a.Run directly in the caller's own
// goroutine, exactly as R1 originally did. With health tracking enabled,
// it races a.Run (in its own goroutine) against connectGrace: if Run is
// still running once the grace period elapses, the adapter is marked
// connected before waiting for Run's eventual return.
func (s *adapterSupervisor) runOnce(ctx context.Context, a core.Adapter, st core.Store, channel core.Channel, account string) error {
	if s.health == nil || s.connectGrace <= 0 {
		return a.Run(ctx, st)
	}

	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(ctx, st) }()

	select {
	case err := <-runDone:
		return err
	case <-s.after(s.connectGrace):
		s.health.SetConnected(channel, account, s.now())
		return <-runDone
	}
}
