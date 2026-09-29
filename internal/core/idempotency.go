package core

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Idempotency keys (issue #67) make a retried send safe. A client that
// gives up waiting (the MCP server's call timeout, a killed CLI) cannot
// tell whether the daemon went on to deliver, and retrying blind would
// send the message twice. With a key, the daemon remembers the receipt
// of every successful send and answers a repeat with it instead.
//
// The cache lives in memory only: a daemon restart forgets every key.
// That is acceptable because the window it protects is a client's retry
// (seconds to minutes), not a durable dedupe log, and persisting it
// would mean a schema change for a best-effort guard.
const (
	// IdempotencyTTL is how long a successful send's receipt is
	// remembered. A day covers an agent retrying after a timeout and a
	// person re-running the same command later that day, without keeping
	// yesterday's "ok" around to swallow a new identical one tomorrow.
	IdempotencyTTL = 24 * time.Hour
	// IdempotencyMaxKeys bounds the cache so a client minting a new key
	// per call cannot grow the daemon's memory without limit. Human-paced
	// sending never gets near it in a day; when it is full the least
	// recently used receipts go first.
	IdempotencyMaxKeys = 1024
)

type idempotencyKeyCtx struct{}

// WithIdempotencyKey returns ctx carrying key for the next Send or Reply.
// The key travels in the context rather than as a parameter so every
// Backend (RPC client, in-process Service, the TUI's clients and test
// fakes) keeps its signature: only the RPC client, which puts it on the
// wire, and Service, which honors it, need to know about it.
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return context.WithValue(ctx, idempotencyKeyCtx{}, key)
}

// IdempotencyKey returns the key WithIdempotencyKey put in ctx, or "".
func IdempotencyKey(ctx context.Context) string {
	key, _ := ctx.Value(idempotencyKeyCtx{}).(string)
	return key
}

// SetIdempotencyClock overrides the clock the idempotency cache expires
// entries by. Tests inject a fake one so TTL expiry needs no real wait.
func (s *Service) SetIdempotencyClock(now func() time.Time) {
	s.idempotency.mu.Lock()
	defer s.idempotency.mu.Unlock()
	s.idempotency.now = now
}

// idempotencyEntry is one key's send: in flight until done is closed,
// then either remembered (err == nil) or already dropped from the cache.
type idempotencyEntry struct {
	key         string
	fingerprint string
	done        chan struct{}
	plan        Plan
	receipt     Receipt
	err         error
	expires     time.Time // zero while in flight
	elem        *list.Element
}

// idempotencyCache is an LRU of send results keyed by idempotency key,
// with a TTL. In-flight entries are never evicted: a waiter needs them,
// and there can only be as many as there are concurrent sends.
type idempotencyCache struct {
	mu      sync.Mutex
	now     func() time.Time
	ttl     time.Duration
	max     int
	entries map[string]*idempotencyEntry
	order   *list.List // front = most recently used
}

func newIdempotencyCache() *idempotencyCache {
	return &idempotencyCache{
		now:     time.Now,
		ttl:     IdempotencyTTL,
		max:     IdempotencyMaxKeys,
		entries: make(map[string]*idempotencyEntry),
		order:   list.New(),
	}
}

// requestFingerprint hashes what a send is about, so a key reused for a
// different message fails loudly instead of returning an unrelated
// receipt and silently dropping the new message. Each part is length
// prefixed so ("ab","c") and ("a","bc") differ.
func requestFingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s;", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// do runs send at most once per key. A repeat of a remembered key gets
// the stored plan and receipt (with Receipt.Replayed set); a repeat of a
// key still in flight waits for the first call and shares its result; a
// failed send is forgotten so the next call with the key sends again.
func (c *idempotencyCache) do(ctx context.Context, key, fingerprint string, send func() (Plan, Receipt, error)) (Plan, Receipt, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		if !e.expires.IsZero() && !c.now().Before(e.expires) {
			c.removeLocked(e)
		} else {
			if e.fingerprint != fingerprint {
				c.mu.Unlock()
				return Plan{}, Receipt{}, fmt.Errorf("core: idempotency key %q was already used for a different message", key)
			}
			c.order.MoveToFront(e.elem)
			c.mu.Unlock()
			return waitIdempotent(ctx, e)
		}
	}
	e := &idempotencyEntry{key: key, fingerprint: fingerprint, done: make(chan struct{})}
	e.elem = c.order.PushFront(e)
	c.entries[key] = e
	c.evictLocked()
	c.mu.Unlock()

	plan, receipt, err := send()

	c.mu.Lock()
	e.plan, e.receipt, e.err = plan, receipt, err
	if err != nil {
		c.removeLocked(e)
	} else {
		e.expires = c.now().Add(c.ttl)
	}
	close(e.done)
	c.evictLocked()
	c.mu.Unlock()
	return plan, receipt, err
}

func waitIdempotent(ctx context.Context, e *idempotencyEntry) (Plan, Receipt, error) {
	select {
	case <-e.done:
	case <-ctx.Done():
		return Plan{}, Receipt{}, fmt.Errorf("core: waiting for the send already in flight with idempotency key %q: %w", e.key, ctx.Err())
	}
	if e.err != nil {
		return Plan{}, Receipt{}, e.err
	}
	receipt := e.receipt
	receipt.Replayed = true
	return e.plan, receipt, nil
}

// removeLocked drops e if it is still the entry for its key (a failed
// send may race a new entry for the same key after an expiry).
func (c *idempotencyCache) removeLocked(e *idempotencyEntry) {
	if c.entries[e.key] == e {
		delete(c.entries, e.key)
	}
	if e.elem != nil {
		c.order.Remove(e.elem)
		e.elem = nil
	}
}

// evictLocked drops expired entries and then the least recently used
// finished ones until the cache fits its bound.
func (c *idempotencyCache) evictLocked() {
	now := c.now()
	for el := c.order.Back(); el != nil; {
		prev := el.Prev()
		e := el.Value.(*idempotencyEntry)
		if !e.expires.IsZero() && !now.Before(e.expires) {
			c.removeLocked(e)
		}
		el = prev
	}
	for el := c.order.Back(); el != nil && len(c.entries) > c.max; {
		prev := el.Prev()
		if e := el.Value.(*idempotencyEntry); !e.expires.IsZero() {
			c.removeLocked(e)
		}
		el = prev
	}
}
