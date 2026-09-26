package whatsapp

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/reyer3/bunker-go/internal/core"
)

// defaultMinSendInterval is the minimum pause between two outgoing sends
// when the account's config does not set one: humane pacing, never a bulk
// API.
const defaultMinSendInterval = 3 * time.Second

// Adapter is bunker-go's core.Adapter for one linked WhatsApp account. It
// implements core.Adapter, core.Fetcher, core.Sender, core.MediaSender,
// core.Organizer, core.StatusPublisher, core.ReadMarker and
// core.FanoutConfigurer against a waClient, so it is fully testable
// without ever dialing WhatsApp.
type Adapter struct {
	account         string
	cli             waClient
	minSendInterval time.Duration
	names           NameResolver
	historyLimit    int
	fanout          core.FanoutPolicy

	// sleep and rand01 drive the human-emulation choreography (T13b):
	// they default to the real time.Sleep and math/rand.Float64, so
	// production sends really do pause; tests override them (see
	// newTestAdapter in fake_client_test.go) so a send test never sleeps
	// for real (T13f).
	sleep  func(time.Duration)
	rand01 func() float64

	mu         sync.Mutex
	items      map[string]core.Item
	lastSend   time.Time
	groupNames map[string]groupNameCacheEntry
}

var (
	_ core.Adapter          = (*Adapter)(nil)
	_ core.Fetcher          = (*Adapter)(nil)
	_ core.Sender           = (*Adapter)(nil)
	_ core.MediaSender      = (*Adapter)(nil)
	_ core.Organizer        = (*Adapter)(nil)
	_ core.StatusPublisher  = (*Adapter)(nil)
	_ core.ReadMarker       = (*Adapter)(nil)
	_ core.FanoutConfigurer = (*Adapter)(nil)
)

// NewAdapter builds an Adapter for account, driving cli. minSendInterval,
// when zero, defaults to defaultMinSendInterval.
func NewAdapter(account string, cli waClient, minSendInterval ...time.Duration) *Adapter {
	interval := defaultMinSendInterval
	if len(minSendInterval) > 0 && minSendInterval[0] > 0 {
		interval = minSendInterval[0]
	}
	return &Adapter{
		account:         account,
		cli:             cli,
		minSendInterval: interval,
		items:           make(map[string]core.Item),
		sleep:           time.Sleep,
		rand01:          rand.Float64,
	}
}

// SetSleeper overrides how Send/SendMedia/MarkRead wait during the
// human-emulation choreography (composing/typing, pre-read presence).
// Tests inject a fake that records durations without blocking.
func (a *Adapter) SetSleeper(sleep func(time.Duration)) { a.sleep = sleep }

// SetRand01 overrides the [0,1) random source composingDuration uses for
// jitter and the media-only composing window. Tests inject a fixed value
// for deterministic durations.
func (a *Adapter) SetRand01(rand01 func() float64) { a.rand01 = rand01 }

// SetFanoutPolicy configures this account's broadcast limits/pacing (see
// core.FanoutConfigurer), read from config.Account.Options by
// NewFromAccount. The zero value keeps core.Service's own defaults.
func (a *Adapter) SetFanoutPolicy(policy core.FanoutPolicy) { a.fanout = policy }

// FanoutPolicy implements core.FanoutConfigurer.
func (a *Adapter) FanoutPolicy() core.FanoutPolicy { return a.fanout }

// SetNameResolver configures how thread and sender names are resolved
// for incoming messages (see NameResolver). It is optional: a nil (the
// default) resolver falls back to the event's own push name or bare JID
// user part. NewFromAccount wires a real, store-backed resolver; tests
// wire a fake.
func (a *Adapter) SetNameResolver(r NameResolver) { a.names = r }

// SetHistoryLimit sets how many of the most recent messages per
// unread conversation a *events.HistorySync import keeps (see
// handleHistorySync). n <= 0 leaves the default (defaultHistoryMessagesPerChat)
// in place.
func (a *Adapter) SetHistoryLimit(n int) {
	if n > 0 {
		a.historyLimit = n
	}
}

// Channel returns core.ChannelWhatsApp.
func (a *Adapter) Channel() core.Channel { return core.ChannelWhatsApp }

// Account returns the configured account name.
func (a *Adapter) Account() string { return a.account }

// Run connects to WhatsApp and streams events into sink until ctx is
// canceled. It requires a device to already be linked (see Link); an
// unlinked account fails fast with ErrNotLinked instead of trying to
// pair, which only "bunker link whatsapp" does.
func (a *Adapter) Run(ctx context.Context, sink core.Sink) error {
	if !a.cli.IsLinked() {
		return ErrNotLinked
	}

	done := make(chan error, 1)
	handlerID := a.cli.AddEventHandler(func(evt any) {
		a.handleEvent(ctx, sink, evt, done)
	})
	defer a.cli.RemoveEventHandler(handlerID)

	if err := a.cli.Connect(); err != nil {
		return fmt.Errorf("whatsapp: connect: %w", err)
	}
	defer a.cli.Disconnect()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// handleEvent dispatches one whatsmeow event. Transient connectivity
// events (Connected/Disconnected) are not fatal: whatsmeow reconnects
// them internally. Only LoggedOut ends Run, since the device is no
// longer usable at that point.
func (a *Adapter) handleEvent(ctx context.Context, sink core.Sink, evt any, done chan<- error) {
	switch e := evt.(type) {
	case *events.Message:
		// Contacts' status updates are feed noise, not conversations.
		if e.Info.Chat.String() == statusBroadcastJID {
			return
		}
		item := toItem(a.account, e)
		if !isSurfaceable(item) {
			return
		}
		item = a.enrichItem(ctx, item, e.Info.Chat, e.Info.Sender, string(e.Info.ID), e.Info.PushName)
		a.cacheItem(item)
		_ = sink.Upsert(ctx, item)

	case *events.Receipt:
		if e.Type != types.ReceiptTypeRead && e.Type != types.ReceiptTypeReadSelf {
			return
		}
		for _, id := range e.MessageIDs {
			full := itemID(a.account, e.Chat.String(), string(id))
			_ = sink.MarkRead(ctx, full, true)
		}

	case *events.HistorySync:
		a.handleHistorySync(ctx, sink, e.Data)

	case *events.LoggedOut:
		select {
		case done <- ErrLoggedOut:
		default:
		}
	}
}

func (a *Adapter) cacheItem(item core.Item) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.items[item.ID] = item
}

func (a *Adapter) cachedItem(id string) (core.Item, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	item, ok := a.items[id]
	return item, ok
}

// Fetch returns the cached item for id. WhatsApp has no server-side
// refetch: an item this adapter has not seen via Run cannot be recovered.
func (a *Adapter) Fetch(_ context.Context, id string) (core.Item, error) {
	item, ok := a.cachedItem(id)
	if !ok {
		return core.Item{}, fmt.Errorf("whatsapp: fetch %s: %w", id, core.ErrNotFound)
	}
	return item, nil
}

// resolveTarget turns an Outgoing's Thread/To into the JID to send to.
// A reply or a send within a known thread targets that chat directly; a
// fresh send accepts either a full JID or a +E164 phone number, resolved
// with IsOnWhatsApp.
func (a *Adapter) resolveTarget(ctx context.Context, out core.Outgoing) (types.JID, error) {
	if len(out.To) > 1 || len(out.Cc) > 0 {
		return types.JID{}, fmt.Errorf("whatsapp: send: only one recipient is supported, got %d To and %d Cc: %w", len(out.To), len(out.Cc), core.ErrUnsupported)
	}
	if out.Thread != "" {
		jid, err := types.ParseJID(out.Thread)
		if err != nil {
			return types.JID{}, fmt.Errorf("whatsapp: send: thread %q: %w", out.Thread, err)
		}
		return jid, nil
	}
	if len(out.To) == 0 {
		return types.JID{}, fmt.Errorf("whatsapp: send: no recipient given")
	}
	to := out.To[0]
	if strings.Contains(to, "@") {
		jid, err := types.ParseJID(to)
		if err != nil {
			return types.JID{}, fmt.Errorf("whatsapp: send: %q: %w", to, err)
		}
		return jid, nil
	}
	results, err := a.cli.IsOnWhatsApp(ctx, []string{to})
	if err != nil {
		return types.JID{}, fmt.Errorf("whatsapp: send: resolve %q: %w", to, err)
	}
	if len(results) == 0 || !results[0].IsIn {
		return types.JID{}, fmt.Errorf("whatsapp: send: %q is not on WhatsApp", to)
	}
	return results[0].JID, nil
}
