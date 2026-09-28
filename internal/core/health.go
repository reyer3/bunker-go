package core

import (
	"sort"
	"sync"
	"time"
)

// AdapterState is one adapter's high-level connection state (R4), as the
// daemon's adapter supervisor (cmd/bunker) tracks it.
type AdapterState string

const (
	AdapterConnecting AdapterState = "connecting"
	AdapterConnected  AdapterState = "connected"
	AdapterBackoff    AdapterState = "backoff"
	AdapterStopped    AdapterState = "stopped"
)

// AdapterHealth is one adapter's current health snapshot, returned by
// the health RPC method and `bunker health`.
type AdapterHealth struct {
	Channel Channel      `json:"channel"`
	Account string       `json:"account"`
	State   AdapterState `json:"state"`
	// Since is when the adapter entered its current State.
	Since time.Time `json:"since"`
	// LastError is the error that caused the most recent Backoff/Stopped
	// transition, empty otherwise.
	LastError string `json:"lastError,omitempty"`
	// Restarts counts how many times this adapter's Run has been
	// restarted after returning while the daemon was still live.
	Restarts int `json:"restarts"`
}

type healthKey struct {
	channel Channel
	account string
}

// HealthTracker is the daemon's shared, thread-safe record of every
// adapter's current AdapterHealth (R4). It lives in internal/core rather
// than cmd/bunker, where the adapter supervisor that writes it runs,
// so internal/rpc's Server (which depends only on core, never on
// cmd/bunker) can serve it through Service.Health without an import
// cycle.
type HealthTracker struct {
	mu    sync.Mutex
	state map[healthKey]AdapterHealth
}

// NewHealthTracker returns an empty tracker.
func NewHealthTracker() *HealthTracker {
	return &HealthTracker{state: make(map[healthKey]AdapterHealth)}
}

// SetConnecting records that channel/account is (re)starting its Run
// loop. It never touches LastError or Restarts: a reconnect attempt in
// progress keeps its prior restart count and last error visible until it
// either succeeds (SetConnected clears LastError) or fails again
// (SetBackoff records the new one).
func (t *HealthTracker) SetConnecting(channel Channel, account string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h := t.entry(channel, account)
	h.State, h.Since = AdapterConnecting, now
	t.state[healthKey{channel, account}] = h
}

// SetConnected records that channel/account reached a live session,
// clearing LastError (a fresh good connection has no current error).
func (t *HealthTracker) SetConnected(channel Channel, account string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h := t.entry(channel, account)
	h.State, h.Since, h.LastError = AdapterConnected, now, ""
	t.state[healthKey{channel, account}] = h
}

// SetBackoff records that channel/account's Run returned (err, possibly
// nil) while the daemon is still live and is now waiting to restart. It
// increments Restarts: unlike SetConnecting/SetConnected, this is the one
// transition that actually happened because Run stopped.
func (t *HealthTracker) SetBackoff(channel Channel, account string, now time.Time, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h := t.entry(channel, account)
	h.State, h.Since, h.Restarts = AdapterBackoff, now, h.Restarts+1
	h.LastError = errString(err)
	t.state[healthKey{channel, account}] = h
}

// SetStopped records that channel/account's supervisor loop has ended
// for good (the daemon itself is shutting down).
func (t *HealthTracker) SetStopped(channel Channel, account string, now time.Time, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	h := t.entry(channel, account)
	h.State, h.Since = AdapterStopped, now
	if err != nil {
		h.LastError = errString(err)
	}
	t.state[healthKey{channel, account}] = h
}

// entry returns channel/account's current health, seeded with
// Channel/Account if this is its first transition. Callers hold t.mu.
func (t *HealthTracker) entry(channel Channel, account string) AdapterHealth {
	h := t.state[healthKey{channel, account}]
	h.Channel, h.Account = channel, account
	return h
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Snapshot returns every tracked adapter's current health, ordered by
// (channel, account) for stable output.
func (t *HealthTracker) Snapshot() []AdapterHealth {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]AdapterHealth, 0, len(t.state))
	for _, h := range t.state {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Channel != out[j].Channel {
			return out[i].Channel < out[j].Channel
		}
		return out[i].Account < out[j].Account
	})
	return out
}
