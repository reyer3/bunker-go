package tui

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

type closeInterruptClient struct {
	inboxClient
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	closes  atomic.Int32
}

func (c *closeInterruptClient) List(context.Context, core.Filter) ([]core.Item, error) {
	close(c.entered)
	<-c.release
	return nil, net.ErrClosed
}

func (c *closeInterruptClient) Close() error {
	c.closes.Add(1)
	c.once.Do(func() { close(c.release) })
	return nil
}

func TestQueryClientCloseInterruptsActiveAndQueuedQueries(t *testing.T) {
	base := &closeInterruptClient{entered: make(chan struct{}), release: make(chan struct{})}
	dials := atomic.Int32{}
	client := NewQueryClient(base, func(context.Context) (Client, error) {
		dials.Add(1)
		return &inboxClient{}, nil
	})
	active := make(chan error, 1)
	go func() { _, err := client.List(context.Background(), core.Filter{}); active <- err }()
	<-base.entered
	queued := make(chan error, 1)
	go func() { _, err := client.Counts(context.Background()); queued <- err }()
	closed := make(chan error, 1)
	go func() { closed <- client.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		base.once.Do(func() { close(base.release) })
		t.Error("Close waited for active query instead of interrupting it")
		<-closed
	}
	if err := <-active; !errors.Is(err, net.ErrClosed) {
		t.Errorf("active query error = %v, want closed", err)
	}
	if err := <-queued; !errors.Is(err, net.ErrClosed) {
		t.Errorf("queued query error = %v, want closed", err)
	}
	if _, err := client.List(context.Background(), core.Filter{}); !errors.Is(err, net.ErrClosed) {
		t.Errorf("post-close query error = %v, want closed", err)
	}
	if err := client.Close(); err != nil {
		t.Errorf("second Close error: %v", err)
	}
	if got := base.closes.Load(); got != 1 {
		t.Errorf("underlying close count = %d, want 1", got)
	}
	if got := dials.Load(); got != 0 {
		t.Errorf("dial count = %d after Close, want 0", got)
	}
}

type cancelOnFailureClient struct {
	inboxClient
	cancel context.CancelFunc
}

func (c *cancelOnFailureClient) List(context.Context, core.Filter) ([]core.Item, error) {
	c.cancel()
	return nil, net.ErrClosed
}

func TestQueryClientDoesNotDialAfterQueryDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := &cancelOnFailureClient{cancel: cancel}
	dials := 0
	client := NewQueryClient(base, func(context.Context) (Client, error) {
		dials++
		return &inboxClient{}, nil
	})
	_, err := client.List(ctx, core.Filter{})
	if dials != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("expired query re-dialed: dials=%d err=%v", dials, err)
	}
}

func TestQueryClientDialRespectsDeadlineAndClose(t *testing.T) {
	t.Run("query deadline", func(t *testing.T) {
		base := &inboxClient{listErr: net.ErrClosed}
		entered := make(chan struct{})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		client := NewQueryClient(base, func(dialCtx context.Context) (Client, error) {
			if _, ok := dialCtx.Deadline(); !ok {
				t.Error("dial did not inherit query deadline")
			}
			close(entered)
			<-dialCtx.Done()
			return nil, dialCtx.Err()
		})
		result := make(chan error, 1)
		go func() { _, err := client.List(ctx, core.Filter{}); result <- err }()
		select {
		case err := <-result:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("blocked dial error = %v, want deadline", err)
			}
		case <-time.After(time.Second):
			t.Fatal("blocked dial escaped query deadline")
		}
		<-entered
	})

	t.Run("close during dial", func(t *testing.T) {
		base := &inboxClient{listErr: net.ErrClosed}
		entered := make(chan struct{})
		client := NewQueryClient(base, func(dialCtx context.Context) (Client, error) {
			close(entered)
			<-dialCtx.Done()
			return nil, dialCtx.Err()
		})
		result := make(chan error, 1)
		go func() { _, err := client.List(context.Background(), core.Filter{}); result <- err }()
		<-entered
		closed := make(chan error, 1)
		go func() { closed <- client.Close() }()
		select {
		case err := <-closed:
			if err != nil {
				t.Errorf("Close error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Close waited on blocked dial")
		}
		select {
		case err := <-result:
			if !errors.Is(err, net.ErrClosed) {
				t.Errorf("query error after Close = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("query stayed blocked after Close")
		}
	})
}

func TestQueryClientReconnectsOnlyFailedQueries(t *testing.T) {
	first := &inboxClient{listErr: net.ErrClosed}
	second := &inboxClient{items: []core.Item{{ID: "recovered"}}}
	dials := 0
	client := NewQueryClient(first, func(context.Context) (Client, error) {
		dials++
		return second, nil
	})
	items, err := client.List(context.Background(), core.Filter{})
	if err != nil || len(items) != 1 || items[0].ID != "recovered" || dials != 1 || first.listCalls != 1 || second.listCalls != 1 {
		t.Fatalf("query retry: items=%+v err=%v dials=%d calls=%d/%d", items, err, dials, first.listCalls, second.listCalls)
	}
	if _, err := client.Read(context.Background(), "recovered", false); err != nil || second.readReceipt {
		t.Fatalf("read after reconnect: err=%v receipt=%t", err, second.readReceipt)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

type blockedQueryClient struct {
	inboxClient
	entered chan struct{}
	release chan struct{}
}

func (c *blockedQueryClient) List(context.Context, core.Filter) ([]core.Item, error) {
	close(c.entered)
	<-c.release
	return nil, nil
}

func TestQueryClientDeadlineWhileAnotherQueryIsInFlight(t *testing.T) {
	base := &blockedQueryClient{entered: make(chan struct{}), release: make(chan struct{})}
	client := NewQueryClient(base, nil)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = client.List(context.Background(), core.Filter{})
	}()
	<-base.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() {
		_, err := client.Counts(ctx)
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("waiting query error = %v, want deadline", err)
		}
	case <-time.After(time.Second):
		t.Error("waiting query ignored its deadline")
	}
	close(base.release)
	<-firstDone
}

func TestQueryClientDoesNotRetryMutationsOrBusinessErrors(t *testing.T) {
	first := &inboxClient{listErr: errors.New("permission denied")}
	dials := 0
	client := NewQueryClient(first, func(context.Context) (Client, error) {
		dials++
		return &inboxClient{}, nil
	})
	if _, err := client.List(context.Background(), core.Filter{}); err == nil || dials != 0 {
		t.Fatalf("business error retried: err=%v dials=%d", err, dials)
	}
	_, _, _ = client.Reply(context.Background(), "id", "body", nil, nil, false)
	_, _ = client.Organize(context.Background(), "id", core.OrganizeOp{}, false)
	if dials != 0 || first.otherCalls != 2 {
		t.Fatalf("mutations retried or rerouted: dials=%d calls=%d", dials, first.otherCalls)
	}
}

func TestQueryClientRetriesReadWithoutReceiptButNeverReadWithReceipt(t *testing.T) {
	first := &inboxClient{readErr: net.ErrClosed}
	second := &inboxClient{readResult: core.Item{ID: "recovered"}}
	dials := 0
	client := NewQueryClient(first, func(context.Context) (Client, error) {
		dials++
		return second, nil
	})
	got, err := client.Read(context.Background(), "item", false)
	if err != nil || got.ID != "recovered" || dials != 1 || first.readCalls != 1 || second.readCalls != 1 || first.readReceipt || second.readReceipt {
		t.Fatalf("safe read retry: item=%+v err=%v dials=%d first=%d second=%d", got, err, dials, first.readCalls, second.readCalls)
	}
	second.readErr = net.ErrClosed
	_, err = client.Read(context.Background(), "item", true)
	if !errors.Is(err, net.ErrClosed) || dials != 1 || second.readCalls != 2 {
		t.Fatalf("receipt read retried: err=%v dials=%d calls=%d", err, dials, second.readCalls)
	}
}
