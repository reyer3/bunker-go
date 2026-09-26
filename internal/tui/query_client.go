package tui

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"

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

var _ Client = (*queryClient)(nil)
