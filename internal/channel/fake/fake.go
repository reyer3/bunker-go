// Package fake provides an in-memory core.Adapter implementing every
// optional capability. It backs unit tests across the repo and
// "bunker daemon --fake" demos; it never touches a real network.
package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// OrganizeCall records one Organize invocation for assertions.
type OrganizeCall struct {
	ID string
	Op core.OrganizeOp
}

// Adapter is an in-memory core.Adapter that also implements Sender,
// Organizer, StatusPublisher and Fetcher.
type Adapter struct {
	channel core.Channel
	account string

	mu        sync.Mutex
	items     map[string]core.Item
	sent      []core.Outgoing
	organized []OrganizeCall
	statuses  []core.Status
	seq       int
}

var (
	_ core.Adapter         = (*Adapter)(nil)
	_ core.Sender          = (*Adapter)(nil)
	_ core.Organizer       = (*Adapter)(nil)
	_ core.StatusPublisher = (*Adapter)(nil)
	_ core.Fetcher         = (*Adapter)(nil)
)

// New returns a fake adapter for (channel, account) preloaded with seed
// items, addressable by Fetch and streamed by Run.
func New(channel core.Channel, account string, seed ...core.Item) *Adapter {
	items := make(map[string]core.Item, len(seed))
	for _, it := range seed {
		items[it.ID] = it
	}
	return &Adapter{channel: channel, account: account, items: items}
}

// Channel returns the channel this adapter serves.
func (a *Adapter) Channel() core.Channel { return a.channel }

// Account returns the account this adapter serves.
func (a *Adapter) Account() string { return a.account }

// Run pushes every seed item into sink, then blocks until ctx is done,
// mirroring a real adapter's long-running receive loop.
func (a *Adapter) Run(ctx context.Context, sink core.Sink) error {
	a.mu.Lock()
	items := make([]core.Item, 0, len(a.items))
	for _, it := range a.items {
		items = append(items, it)
	}
	a.mu.Unlock()

	for _, it := range items {
		if err := sink.Upsert(ctx, it); err != nil {
			return fmt.Errorf("fake: upsert seed %s: %w", it.ID, err)
		}
	}

	<-ctx.Done()
	return ctx.Err()
}

// Send records out and returns a synthetic Receipt.
func (a *Adapter) Send(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, out)
	a.seq++
	return core.Receipt{ID: fmt.Sprintf("fake-send-%d", a.seq), Channel: a.channel, At: time.Now()}, nil
}

// SentMessages returns every Outgoing passed to Send, in order.
func (a *Adapter) SentMessages() []core.Outgoing {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]core.Outgoing, len(a.sent))
	copy(out, a.sent)
	return out
}

// Organize records the call against id.
func (a *Adapter) Organize(ctx context.Context, id string, op core.OrganizeOp) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.organized = append(a.organized, OrganizeCall{ID: id, Op: op})
	return nil
}

// OrganizeCalls returns every Organize call, in order.
func (a *Adapter) OrganizeCalls() []OrganizeCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]OrganizeCall, len(a.organized))
	copy(out, a.organized)
	return out
}

// PostStatus records status and returns a synthetic Receipt.
func (a *Adapter) PostStatus(ctx context.Context, status core.Status) (core.Receipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statuses = append(a.statuses, status)
	a.seq++
	return core.Receipt{ID: fmt.Sprintf("fake-status-%d", a.seq), Channel: a.channel, At: time.Now()}, nil
}

// PostedStatuses returns every status posted, in order.
func (a *Adapter) PostedStatuses() []core.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]core.Status, len(a.statuses))
	copy(out, a.statuses)
	return out
}

// Fetch returns the full item for id from the seed/observed set.
func (a *Adapter) Fetch(ctx context.Context, id string) (core.Item, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	it, ok := a.items[id]
	if !ok {
		return core.Item{}, fmt.Errorf("fake: fetch %s: %w", id, core.ErrNotFound)
	}
	return it, nil
}
