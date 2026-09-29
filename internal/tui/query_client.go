package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// queryClient owns the TUI connection. Only idempotent queries can be retried
// after a transport failure; a write may have reached the daemon before its
// response was lost, so forwarding it once is mandatory.
type queryClient struct {
	gate      chan struct{}
	mu        sync.Mutex
	current   Client
	dial      func(context.Context) (Client, error)
	lifecycle context.Context
	cancel    context.CancelFunc
	closed    bool
}

func NewQueryClient(initial Client, dial func(context.Context) (Client, error)) Client {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	lifecycle, cancel := context.WithCancel(context.Background())
	return &queryClient{gate: gate, current: initial, dial: dial, lifecycle: lifecycle, cancel: cancel}
}

func (c *queryClient) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.lifecycle.Done():
		return net.ErrClosed
	case <-c.gate:
		if err := ctx.Err(); err != nil {
			c.release()
			return err
		}
		if c.isClosed() {
			c.release()
			return net.ErrClosed
		}
		return nil
	}
}

func (c *queryClient) release() { c.gate <- struct{}{} }

func (c *queryClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *queryClient) activeClient() (Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, net.ErrClosed
	}
	return c.current, nil
}

func query[T any](c *queryClient, ctx context.Context, call func(Client) (T, error)) (T, error) {
	var zero T
	if err := c.acquire(ctx); err != nil {
		return zero, err
	}
	defer c.release()
	client, err := c.activeClient()
	if err != nil {
		return zero, err
	}
	result, err := call(client)
	if c.isClosed() {
		return zero, net.ErrClosed
	}
	if err == nil || !transportFailure(err) || c.dial == nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	dialCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.lifecycle, cancel)
	replacement, dialErr := c.dial(dialCtx)
	stop()
	cancel()
	if dialErr != nil {
		if c.isClosed() {
			return zero, net.ErrClosed
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return result, errors.Join(err, dialErr)
	}
	c.mu.Lock()
	if c.closed || ctx.Err() != nil {
		c.mu.Unlock()
		_ = replacement.Close()
		if c.isClosed() {
			return zero, net.ErrClosed
		}
		return zero, ctx.Err()
	}
	old := c.current
	c.current = replacement
	c.mu.Unlock()
	_ = old.Close()
	if c.isClosed() {
		return zero, net.ErrClosed
	}
	return call(replacement)
}

func transportFailure(err error) bool {
	var netErr net.Error
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.As(err, &netErr) ||
		strings.HasPrefix(err.Error(), "rpc: connection closed")
}

func (c *queryClient) List(ctx context.Context, filter core.Filter) ([]core.Item, error) {
	return query(c, ctx, func(client Client) ([]core.Item, error) { return client.List(ctx, filter) })
}

func (c *queryClient) Counts(ctx context.Context) (map[core.Channel]map[string]int, error) {
	return query(c, ctx, func(client Client) (map[core.Channel]map[string]int, error) { return client.Counts(ctx) })
}

func (c *queryClient) Read(ctx context.Context, id string, receipt bool) (core.Item, error) {
	if !receipt {
		return query(c, ctx, func(client Client) (core.Item, error) { return client.Read(ctx, id, false) })
	}
	if err := c.acquire(ctx); err != nil {
		return core.Item{}, err
	}
	defer c.release()
	client, err := c.activeClient()
	if err != nil {
		return core.Item{}, err
	}
	return client.Read(ctx, id, true)
}

func (c *queryClient) Reply(ctx context.Context, id, body string, cc, attachments []string, dryRun bool) (core.Plan, core.Receipt, error) {
	if err := c.acquire(ctx); err != nil {
		return core.Plan{}, core.Receipt{}, err
	}
	defer c.release()
	client, err := c.activeClient()
	if err != nil {
		return core.Plan{}, core.Receipt{}, err
	}
	return client.Reply(ctx, id, body, cc, attachments, dryRun)
}

func (c *queryClient) Organize(ctx context.Context, id string, op core.OrganizeOp, dryRun bool) (core.Plan, error) {
	if err := c.acquire(ctx); err != nil {
		return core.Plan{}, err
	}
	defer c.release()
	client, err := c.activeClient()
	if err != nil {
		return core.Plan{}, err
	}
	return client.Organize(ctx, id, op, dryRun)
}

// Thread and Presence are read-only, so they retry-after-redial like List
// and Counts. PresenceKeepalive and Typing report a still-current fact
// ("still focused"/"still composing") rather than performing a one-shot
// action, so resending after a transport failure is harmless the same
// way; they are not forwarded-once-only like Reply/Organize.
func (c *queryClient) Thread(ctx context.Context, channel string, account, thread string, before time.Time, limit int) ([]core.Item, error) {
	return query(c, ctx, func(client Client) ([]core.Item, error) {
		return client.Thread(ctx, channel, account, thread, before, limit)
	})
}

// ReadThread is forwarded at most once, like Reply/Organize/Send: it
// sends read receipts/\Seen and mutates store state as a side effect, so
// silently retrying it after a lost response could double-send receipts.
func (c *queryClient) ReadThread(ctx context.Context, channel string, account, thread string, receipt bool) (int, error) {
	if err := c.acquire(ctx); err != nil {
		return 0, err
	}
	defer c.release()
	client, err := c.activeClient()
	if err != nil {
		return 0, err
	}
	return client.ReadThread(ctx, channel, account, thread, receipt)
}

// MarkUnread changes state, so like Organize it is forwarded once and
// never retried after a transport failure.
func (c *queryClient) MarkUnread(ctx context.Context, id string) (bool, error) {
	if err := c.acquire(ctx); err != nil {
		return false, err
	}
	defer c.release()
	client, err := c.activeClient()
	if err != nil {
		return false, err
	}
	marker, ok := client.(UnreadMarker)
	if !ok {
		return false, fmt.Errorf("tui: mark unread: %w", core.ErrUnsupported)
	}
	return marker.MarkUnread(ctx, id)
}

// Health is an idempotent query; it errors with core.ErrUnsupported when
// the connection underneath has no health listing.
func (c *queryClient) Health(ctx context.Context) ([]core.AdapterHealth, error) {
	return query(c, ctx, func(client Client) ([]core.AdapterHealth, error) {
		hc, ok := client.(HealthClient)
		if !ok {
			return nil, fmt.Errorf("tui: health: %w", core.ErrUnsupported)
		}
		return hc.Health(ctx)
	})
}

// ListPage is an idempotent query (the / filter's daemon search, issue
// #62); it errors with core.ErrUnsupported when the connection
// underneath cannot page, so the filter falls back to memory.
func (c *queryClient) ListPage(ctx context.Context, filter core.Filter, text string) (core.Page, error) {
	return query(c, ctx, func(client Client) (core.Page, error) {
		pc, ok := client.(PageClient)
		if !ok {
			return core.Page{}, fmt.Errorf("tui: list page: %w", core.ErrUnsupported)
		}
		return pc.ListPage(ctx, filter, text)
	})
}

// Contacts is an idempotent query; it errors with core.ErrUnsupported when
// the connection underneath has no contact listing.
func (c *queryClient) Contacts(ctx context.Context, filter core.ContactFilter) ([]core.Contact, error) {
	return query(c, ctx, func(client Client) ([]core.Contact, error) {
		lister, ok := client.(ContactsClient)
		if !ok {
			return nil, fmt.Errorf("tui: contacts: %w", core.ErrUnsupported)
		}
		return lister.Contacts(ctx, filter)
	})
}

func (c *queryClient) Presence(ctx context.Context, channel string, account, thread string) (core.Presence, error) {
	return query(c, ctx, func(client Client) (core.Presence, error) { return client.Presence(ctx, channel, account, thread) })
}

func (c *queryClient) PresenceKeepalive(ctx context.Context, channel string, account, thread string, focused bool) error {
	_, err := query(c, ctx, func(client Client) (struct{}, error) {
		return struct{}{}, client.PresenceKeepalive(ctx, channel, account, thread, focused)
	})
	return err
}

func (c *queryClient) Typing(ctx context.Context, channel string, account, thread string, composing bool) error {
	_, err := query(c, ctx, func(client Client) (struct{}, error) {
		return struct{}{}, client.Typing(ctx, channel, account, thread, composing)
	})
	return err
}

// Send is forwarded at most once, like Reply/Organize: it is a one-shot
// action, not an idempotent query.
func (c *queryClient) Send(ctx context.Context, out core.Outgoing, dryRun bool) (core.Plan, core.Receipt, error) {
	if err := c.acquire(ctx); err != nil {
		return core.Plan{}, core.Receipt{}, err
	}
	defer c.release()
	client, err := c.activeClient()
	if err != nil {
		return core.Plan{}, core.Receipt{}, err
	}
	return client.Send(ctx, out, dryRun)
}

// Download is forwarded at most once, like Send/Reply/Organize: it writes
// a file as a side effect, so silently retrying it after a lost response
// could either double the work or (without Force) fail on the file the
// first attempt already wrote — the caller sees the error and decides.
func (c *queryClient) Download(ctx context.Context, id string, index int, destPath string, opts core.DownloadOptions) (core.DownloadResult, error) {
	if err := c.acquire(ctx); err != nil {
		return core.DownloadResult{}, err
	}
	defer c.release()
	client, err := c.activeClient()
	if err != nil {
		return core.DownloadResult{}, err
	}
	return client.Download(ctx, id, index, destPath, opts)
}

// EditMessage, DeleteMessage and React are forwarded at most once, like
// Send: they change a message the other side already has. A connection
// without the capability reports core.ErrUnsupported.
func (c *queryClient) EditMessage(ctx context.Context, id, text string, dryRun bool) (core.Plan, core.Receipt, error) {
	return c.messageAction(ctx, func(mc MessageClient) (core.Plan, core.Receipt, error) {
		return mc.EditMessage(ctx, id, text, dryRun)
	})
}

func (c *queryClient) DeleteMessage(ctx context.Context, id string, dryRun bool) (core.Plan, core.Receipt, error) {
	return c.messageAction(ctx, func(mc MessageClient) (core.Plan, core.Receipt, error) {
		return mc.DeleteMessage(ctx, id, dryRun)
	})
}

func (c *queryClient) React(ctx context.Context, id, emoji string, dryRun bool) (core.Plan, core.Receipt, error) {
	return c.messageAction(ctx, func(mc MessageClient) (core.Plan, core.Receipt, error) {
		return mc.React(ctx, id, emoji, dryRun)
	})
}

func (c *queryClient) messageAction(ctx context.Context, do func(MessageClient) (core.Plan, core.Receipt, error)) (core.Plan, core.Receipt, error) {
	if err := c.acquire(ctx); err != nil {
		return core.Plan{}, core.Receipt{}, err
	}
	defer c.release()
	client, err := c.activeClient()
	if err != nil {
		return core.Plan{}, core.Receipt{}, err
	}
	mc, ok := client.(MessageClient)
	if !ok {
		return core.Plan{}, core.Receipt{}, fmt.Errorf("tui: edit, delete or react: %w", core.ErrUnsupported)
	}
	return do(mc)
}

func (c *queryClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.cancel()
	client := c.current
	c.current = nil
	c.mu.Unlock()
	return client.Close()
}

var (
	_ Client        = (*queryClient)(nil)
	_ PageClient    = (*queryClient)(nil)
	_ MessageClient = (*queryClient)(nil)
)
