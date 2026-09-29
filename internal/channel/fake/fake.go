// Package fake provides an in-memory core.Adapter implementing every
// optional capability. It backs unit tests across the repo and
// "bunker daemon --fake" demos; it never touches a real network.
package fake

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// OrganizeCall records one Organize invocation for assertions.
type OrganizeCall struct {
	ID string
	Op core.OrganizeOp
}

// attachmentKey identifies one attachment within an item, for
// SetAttachmentData/DownloadAttachment.
type attachmentKey struct {
	id    string
	index int
}

// Adapter is an in-memory core.Adapter that also implements Sender,
// Organizer, StatusPublisher, Fetcher, AttachmentDownloader, Editor,
// Deleter and Reactor.
type Adapter struct {
	channel core.Channel
	account string

	mu             sync.Mutex
	items          map[string]core.Item
	sent           []core.Outgoing
	organized      []OrganizeCall
	statuses       []core.Status
	seq            int
	attachmentData map[attachmentKey][]byte
	actions        []MessageAction
}

var (
	_ core.Adapter              = (*Adapter)(nil)
	_ core.Sender               = (*Adapter)(nil)
	_ core.Organizer            = (*Adapter)(nil)
	_ core.StatusPublisher      = (*Adapter)(nil)
	_ core.Fetcher              = (*Adapter)(nil)
	_ core.AttachmentDownloader = (*Adapter)(nil)
	_ core.Editor               = (*Adapter)(nil)
	_ core.Deleter              = (*Adapter)(nil)
	_ core.Reactor              = (*Adapter)(nil)
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

// SetAttachmentData registers the bytes DownloadAttachment returns for
// item id's attachment at index. Tests call this before exercising
// Service/RPC/CLI download.
func (a *Adapter) SetAttachmentData(id string, index int, data []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.attachmentData == nil {
		a.attachmentData = make(map[attachmentKey][]byte)
	}
	a.attachmentData[attachmentKey{id, index}] = data
}

// DownloadAttachment implements core.AttachmentDownloader against the
// bytes a test registered with SetAttachmentData.
func (a *Adapter) DownloadAttachment(_ context.Context, item core.Item, index int) (io.ReadCloser, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	data, ok := a.attachmentData[attachmentKey{item.ID, index}]
	if !ok {
		return nil, fmt.Errorf("fake: download %s attachment %d: %w", item.ID, index, core.ErrNotFound)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// MessageAction records one EditMessage, DeleteMessage or React call.
type MessageAction struct {
	Action string // "edit", "delete" or "react"
	ID     string
	Text   string // the new text, or the emoji ("" removes it)
}

// ReactionSender is the Reaction.Sender this adapter reports for its own
// reactions (core.Reactor.OwnReactionSender).
const ReactionSender = "me"

// EditMessage records the edit and returns a synthetic Receipt.
func (a *Adapter) EditMessage(_ context.Context, item core.Item, newText string) (core.Receipt, error) {
	return a.recordAction(MessageAction{Action: "edit", ID: item.ID, Text: newText})
}

// DeleteMessage records the delete and returns a synthetic Receipt.
func (a *Adapter) DeleteMessage(_ context.Context, item core.Item) (core.Receipt, error) {
	return a.recordAction(MessageAction{Action: "delete", ID: item.ID})
}

// React records the reaction and returns a synthetic Receipt.
func (a *Adapter) React(_ context.Context, item core.Item, emoji string) (core.Receipt, error) {
	return a.recordAction(MessageAction{Action: "react", ID: item.ID, Text: emoji})
}

// OwnReactionSender implements core.Reactor.
func (a *Adapter) OwnReactionSender() string { return ReactionSender }

// MessageActions returns every edit, delete and react call, in order.
func (a *Adapter) MessageActions() []MessageAction {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]MessageAction(nil), a.actions...)
}

func (a *Adapter) recordAction(action MessageAction) (core.Receipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, action)
	a.seq++
	return core.Receipt{ID: fmt.Sprintf("fake-%s-%d", action.Action, a.seq), Channel: a.channel, At: time.Now()}, nil
}
