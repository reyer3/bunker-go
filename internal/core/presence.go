package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PresenceLeaseTimeout is how long Service keeps a
// PresenceAvailabilityController's account "available" without a fresh
// PresenceKeepalive(focused=true) before it revokes availability on its
// own — the privacy Decision's "or after 60s without a keepalive from
// the TUI" clause (odd/tasks/conversation-view.md).
const PresenceLeaseTimeout = 60 * time.Second

// presenceLease tracks one (channel, account)'s availability grant:
// available only while a chat view is open AND focused, never by
// default. thread is the conversation currently subscribed to; stop
// cancels the pending PresenceLeaseTimeout expiry, when one is
// scheduled.
type presenceLease struct {
	available bool
	thread    string
	stop      func() bool
}

// defaultPresenceAfterFunc is the production presenceAfterFunc: a real
// time.AfterFunc. Tests inject a fake (see SetPresenceAfterFunc) so the
// lease's 60s timeout is never actually waited for.
func defaultPresenceAfterFunc(d time.Duration, f func()) (stop func() bool) {
	t := time.AfterFunc(d, f)
	return t.Stop
}

// SetPresenceAfterFunc overrides how Service schedules a lease's
// PresenceLeaseTimeout expiry. It mirrors time.AfterFunc's shape
// (schedule f after d, return a stop function); tests inject a
// deterministic fake that fires on demand instead of after a real 60s
// wait.
func (s *Service) SetPresenceAfterFunc(after func(d time.Duration, f func()) (stop func() bool)) {
	s.presenceAfterFunc = after
}

func presenceLeaseKey(channel Channel, account string) string {
	return string(channel) + "/" + account
}

func splitPresenceLeaseKey(key string) (Channel, string, bool) {
	i := strings.IndexByte(key, '/')
	if i < 0 {
		return "", "", false
	}
	return Channel(key[:i]), key[i+1:], true
}

// Presence returns (channel, account, thread)'s live presence: the
// registered adapter's PresenceProvider when it has one, else State
// "unknown" — never an error for a channel that simply has no live
// presence (mail).
func (s *Service) Presence(ctx context.Context, channel, account, thread string) (Presence, error) {
	adapter, err := s.adapterFor(Channel(channel), account)
	if err != nil {
		return Presence{}, err
	}
	provider, ok := adapter.(PresenceProvider)
	if !ok {
		return Presence{State: "unknown"}, nil
	}
	return provider.Presence(ctx, thread)
}

// Typing forwards a typing/composing notification to the registered
// adapter's TypingSender. thread is required — there is no "typing on
// nothing" — and a channel without the capability (mail) reports
// ErrUnsupported instead of silently doing nothing. Throttling the
// actual send rate is the TUI's own responsibility (see
// odd/tasks/conversation-view.md's contract); this only validates and
// forwards.
func (s *Service) Typing(ctx context.Context, channel, account, thread string, composing bool) error {
	if thread == "" {
		return fmt.Errorf("core: typing: thread is required: %w", ErrUnsupported)
	}
	adapter, err := s.adapterFor(Channel(channel), account)
	if err != nil {
		return err
	}
	sender, ok := adapter.(TypingSender)
	if !ok {
		return fmt.Errorf("core: adapter %s/%s cannot send typing: %w", channel, account, ErrUnsupported)
	}
	return sender.SendTyping(ctx, thread, composing)
}

// PresenceKeepalive implements the availability lease: the daemon stays
// unavailable by default, and becomes available only while a chat view
// is open AND focused (odd/tasks/conversation-view.md's Decisions). The
// TUI calls this every <=20s while a chat view for thread is open.
// focused=true grants (or renews) availability for (channel, account)
// and subscribes to thread; focused=false revokes it immediately.
// Without a renewing call, the lease also revokes itself after
// PresenceLeaseTimeout. A channel without PresenceAvailabilityController
// (Matrix, mail) has nothing to gate, and this is a no-op success.
func (s *Service) PresenceKeepalive(ctx context.Context, channel, account, thread string, focused bool) error {
	adapter, err := s.adapterFor(Channel(channel), account)
	if err != nil {
		return err
	}
	controller, ok := adapter.(PresenceAvailabilityController)
	if !ok {
		return nil
	}

	key := presenceLeaseKey(Channel(channel), account)
	s.presenceMu.Lock()
	lease := s.presenceLeases[key]
	if lease == nil {
		lease = &presenceLease{}
		s.presenceLeases[key] = lease
	}
	wasAvailable, prevThread := lease.available, lease.thread
	if lease.stop != nil {
		lease.stop()
		lease.stop = nil
	}
	s.presenceMu.Unlock()

	if !focused {
		if wasAvailable {
			if err := controller.SetPresenceAvailable(ctx, false, prevThread); err != nil {
				return fmt.Errorf("core: presence keepalive: %w", err)
			}
		}
		s.presenceMu.Lock()
		lease.available, lease.thread = false, ""
		s.presenceMu.Unlock()
		return nil
	}

	if !wasAvailable || prevThread != thread {
		if err := controller.SetPresenceAvailable(ctx, true, thread); err != nil {
			return fmt.Errorf("core: presence keepalive: %w", err)
		}
	}
	s.presenceMu.Lock()
	lease.available, lease.thread = true, thread
	lease.stop = s.presenceAfterFunc(PresenceLeaseTimeout, func() { s.expirePresenceLease(key, controller) })
	s.presenceMu.Unlock()
	return nil
}

// expirePresenceLease fires when a lease's PresenceLeaseTimeout elapses
// without a renewing PresenceKeepalive(focused=true): it revokes
// availability exactly like an explicit focused=false call would, using
// context.Background() since the RPC call that started the timer has
// long since returned.
func (s *Service) expirePresenceLease(key string, controller PresenceAvailabilityController) {
	s.presenceMu.Lock()
	lease := s.presenceLeases[key]
	if lease == nil || !lease.available {
		s.presenceMu.Unlock()
		return
	}
	thread := lease.thread
	lease.available, lease.thread, lease.stop = false, "", nil
	s.presenceMu.Unlock()
	_ = controller.SetPresenceAvailable(context.Background(), false, thread)
}

// ShutdownPresence revokes every currently-available presence lease —
// the Decisions' "or daemon shutdown" clause. The daemon calls this
// during graceful shutdown.
func (s *Service) ShutdownPresence() {
	s.presenceMu.Lock()
	keys := make([]string, 0, len(s.presenceLeases))
	for k := range s.presenceLeases {
		keys = append(keys, k)
	}
	s.presenceMu.Unlock()

	for _, key := range keys {
		s.presenceMu.Lock()
		lease := s.presenceLeases[key]
		if lease == nil || !lease.available {
			s.presenceMu.Unlock()
			continue
		}
		thread := lease.thread
		if lease.stop != nil {
			lease.stop()
		}
		lease.available, lease.thread, lease.stop = false, "", nil
		s.presenceMu.Unlock()

		channel, account, ok := splitPresenceLeaseKey(key)
		if !ok {
			continue
		}
		adapter, ok := s.registry.Get(channel, account)
		if !ok {
			continue
		}
		controller, ok := adapter.(PresenceAvailabilityController)
		if !ok {
			continue
		}
		_ = controller.SetPresenceAvailable(context.Background(), false, thread)
	}
}
