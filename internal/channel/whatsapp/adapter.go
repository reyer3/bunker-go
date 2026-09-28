package whatsapp

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
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
	// avatarTimeout bounds one profile-picture lookup (0 = default).
	avatarTimeout   time.Duration
	account         string
	cli             waClient
	minSendInterval time.Duration
	names           NameResolver
	directory       ContactDirectory
	historyLimit    int
	fanout          core.FanoutPolicy

	// sleep and rand01 drive the human-emulation choreography (T13b):
	// they default to the real time.Sleep and math/rand.Float64, so
	// production sends really do pause; tests override them (see
	// newTestAdapter in fake_client_test.go) so a send test never sleeps
	// for real (T13f).
	sleep  func(time.Duration)
	rand01 func() float64

	// httpGet fetches a plain HTTPS URL's bytes: WhatsApp profile/group
	// pictures (see Avatar) are served over an ordinary HTTPS GET, unlike
	// message media, which whatsmeow's own Download decrypts. It defaults
	// to a real http.Client; tests inject a fake so avatar tests never
	// touch the network.
	httpGet func(ctx context.Context, url string) ([]byte, error)

	mu         sync.Mutex
	items      map[string]core.Item
	lastSend   time.Time
	groupNames map[string]groupNameCacheEntry
	// presence caches the last known live presence per thread (K3),
	// updated from events.Presence/events.ChatPresence — WhatsApp only
	// delivers these while this account is itself "available" (see
	// SetPresenceAvailable), so Presence answers from this cache instead
	// of a network round trip.
	presence map[string]core.Presence
	// sink is set once, at the top of Run, so DownloadAttachment (called
	// independently, e.g. over RPC while Run is still active in the
	// daemon) can read/write the same core.Sink handleEvent and
	// handleHistorySync already persist media descriptors through. It is
	// nil until Run has been called at least once.
	sink core.Sink

	// callMu guards the voice-call fields below (see call.go); it is
	// separate from mu so a slow sink write never blocks call signaling.
	// calls is nil unless the account opted in (see EnableCalls).
	callMu      sync.Mutex
	calls       callEngine
	callAudio   callAudio
	liveCalls   map[string]*callRecord
	placingCall bool
}

var (
	_ core.Adapter              = (*Adapter)(nil)
	_ core.Fetcher              = (*Adapter)(nil)
	_ core.Sender               = (*Adapter)(nil)
	_ core.MediaSender          = (*Adapter)(nil)
	_ core.Organizer            = (*Adapter)(nil)
	_ core.StatusPublisher      = (*Adapter)(nil)
	_ core.ReadMarker           = (*Adapter)(nil)
	_ core.FanoutConfigurer     = (*Adapter)(nil)
	_ core.AttachmentDownloader = (*Adapter)(nil)
	_ core.AvatarProvider       = (*Adapter)(nil)
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
		presence:        make(map[string]core.Presence),
		sleep:           time.Sleep,
		rand01:          rand.Float64,
		httpGet:         httpGetURL,
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

// SetHTTPGet overrides how Avatar downloads a profile/group picture's
// bytes once GetProfilePictureInfo has resolved its URL. Tests inject a
// fake that never touches the network.
func (a *Adapter) SetHTTPGet(get func(ctx context.Context, url string) ([]byte, error)) {
	a.httpGet = get
}

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
	a.mu.Lock()
	a.sink = sink
	a.mu.Unlock()

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
	defer a.hangupAll()

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
		// Statuses and channel (newsletter) posts are feed noise, not
		// conversations.
		if isFeedChat(e.Info.Chat.String()) {
			return
		}
		if a.handleEditOrRevoke(ctx, sink, e) || a.handleReaction(ctx, sink, e) {
			return
		}
		item := toItem(a.account, e)
		if !isSurfaceable(item) {
			return
		}
		item = a.enrichItem(ctx, item, e.Info.Chat, e.Info.Sender, string(e.Info.ID), e.Info.PushName)
		a.persistMediaDescriptor(ctx, sink, item, e.Message)
		a.cacheItem(item)
		if err := sink.Upsert(ctx, item); err != nil {
			core.LogSinkError(core.ChannelWhatsApp, a.account, "upsert", err)
		}

	case *events.Receipt:
		// ReceiptTypeRead means someone ELSE read a message WE sent (blue
		// ticks on our own outgoing message): it says nothing about our
		// own unread state and must never clear it. Only ReceiptTypeReadSelf
		// (we read this chat from a different device) does.
		if e.Type != types.ReceiptTypeReadSelf {
			return
		}
		for _, id := range e.MessageIDs {
			full := itemID(a.account, e.Chat.String(), string(id))
			if err := sink.MarkRead(ctx, full, true); err != nil {
				core.LogSinkError(core.ChannelWhatsApp, a.account, "mark_read", err)
			}
		}
		// A ReadSelf does not always list every unread message of the
		// chat (Evidence gap (a)), so also mark the whole thread read up
		// to the receipt's timestamp.
		a.markThreadReadBothForms(ctx, sink, e.Chat, e.Timestamp)

	case *events.MarkChatAsRead:
		a.handleMarkChatAsRead(ctx, sink, e)

	case *events.HistorySync:
		a.handleHistorySync(ctx, sink, e.Data)

	case *events.Presence:
		a.handlePresence(e)

	case *events.ChatPresence:
		a.handleChatPresence(e)

	case *events.LoggedOut:
		select {
		case done <- ErrLoggedOut:
		default:
		}
	}
}

// markThreadReadBothForms marks chat's thread read up to upTo, both
// under chat's own address form and under its stored LID/PN counterpart
// (see waClient.GetAltJID): an item's Thread key is whichever form the
// message that created the conversation first arrived in (Evidence gap
// (b)), which a read receipt's chat address does not always match.
func (a *Adapter) markThreadReadBothForms(ctx context.Context, sink core.Sink, chat types.JID, upTo time.Time) {
	if err := sink.MarkThreadReadUpTo(ctx, core.ChannelWhatsApp, a.account, chat.String(), upTo); err != nil {
		core.LogSinkError(core.ChannelWhatsApp, a.account, "mark_thread_read_up_to", err)
	}
	alt, err := a.cli.GetAltJID(ctx, chat)
	if err != nil || alt.IsEmpty() || alt == chat {
		return
	}
	if err := sink.MarkThreadReadUpTo(ctx, core.ChannelWhatsApp, a.account, alt.String(), upTo); err != nil {
		core.LogSinkError(core.ChannelWhatsApp, a.account, "mark_thread_read_up_to", err)
	}
}

// handleMarkChatAsRead handles the phone's own "mark chat as read/unread"
// app-state mutation (Evidence gap (c)). Action.Read == true marks the
// chat's thread read up to the action's message-range cutoff (falling
// back to the event's own timestamp when the range carries none);
// Action.Read == false is a no-op, since bunker never re-marks an item
// unread from this signal.
func (a *Adapter) handleMarkChatAsRead(ctx context.Context, sink core.Sink, e *events.MarkChatAsRead) {
	if e.Action == nil || !e.Action.GetRead() {
		return
	}
	upTo := e.Timestamp
	if ts := e.Action.GetMessageRange().GetLastMessageTimestamp(); ts > 0 {
		upTo = time.Unix(ts, 0)
	}
	a.markThreadReadBothForms(ctx, sink, e.JID, upTo)
}

// handleEditOrRevoke handles a WhatsApp message edit or revoke (S2),
// both delivered as a ProtocolMessage referencing the target message's
// Key. It reports whether e was one of these (so handleEvent stops
// there instead of also falling through to toItem/Upsert).
func (a *Adapter) handleEditOrRevoke(ctx context.Context, sink core.Sink, e *events.Message) bool {
	proto := e.Message.GetProtocolMessage()
	if proto == nil || proto.GetKey() == nil {
		return false
	}
	id := itemID(a.account, e.Info.Chat.String(), proto.GetKey().GetID())
	switch proto.GetType() {
	case waE2E.ProtocolMessage_MESSAGE_EDIT:
		body, _, _ := bodyAndMedia(proto.GetEditedMessage())
		_ = sink.EditItem(ctx, id, body)
		return true
	case waE2E.ProtocolMessage_REVOKE:
		_ = sink.RevokeItem(ctx, id)
		return true
	default:
		return false
	}
}

// handleReaction handles a WhatsApp reaction (S2): stores {sender,
// emoji} on the target item (an empty emoji removes it), and reports
// whether e was a reaction at all.
func (a *Adapter) handleReaction(ctx context.Context, sink core.Sink, e *events.Message) bool {
	reaction := e.Message.GetReactionMessage()
	if reaction == nil || reaction.GetKey() == nil {
		return false
	}
	id := itemID(a.account, e.Info.Chat.String(), reaction.GetKey().GetID())
	_ = sink.SetReaction(ctx, id, core.Reaction{Sender: e.Info.Sender.String(), Emoji: reaction.GetText()})
	return true
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
