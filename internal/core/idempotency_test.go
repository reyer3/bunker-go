package core_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// gatedSender is a Sender whose sends can be held open (gate) and made
// to fail (fail), counting every call that reached it.
type gatedSender struct {
	calls atomic.Int32
	gate  chan struct{} // nil: never blocks
	fail  atomic.Bool
	// entered, when non-nil, gets a signal for every call that reaches
	// Send, so a test can wait for a send to be in flight without polling.
	entered chan struct{}
}

func (g *gatedSender) Channel() core.Channel                      { return core.ChannelWhatsApp }
func (g *gatedSender) Account() string                            { return "personal" }
func (g *gatedSender) Run(ctx context.Context, _ core.Sink) error { return nil }

func (g *gatedSender) Send(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	n := g.calls.Add(1)
	if g.entered != nil {
		select {
		case g.entered <- struct{}{}:
		default:
		}
	}
	if g.gate != nil {
		<-g.gate
	}
	if g.fail.Load() {
		return core.Receipt{}, errors.New("whatsapp: not connected")
	}
	return core.Receipt{ID: "R" + strconv.Itoa(int(n)), Channel: core.ChannelWhatsApp, At: time.Unix(int64(n), 0)}, nil
}

func idempotencyService(t *testing.T, sender *gatedSender) *core.Service {
	t.Helper()
	reg := core.NewRegistry()
	reg.Register(sender)
	return core.NewService(newMemStore(), reg)
}

func hola() core.Outgoing {
	return core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"51900@s.whatsapp.net"}, Body: "hola"}
}

func TestIdempotencyDuplicateKeySendsOnce(t *testing.T) {
	sender := &gatedSender{}
	svc := idempotencyService(t, sender)
	ctx := core.WithIdempotencyKey(context.Background(), "k1")

	_, first, err := svc.Send(ctx, hola(), false)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := svc.Send(ctx, hola(), false)
	if err != nil {
		t.Fatal(err)
	}
	if n := sender.calls.Load(); n != 1 {
		t.Fatalf("sent %d times, want 1", n)
	}
	if first.Replayed || !second.Replayed || second.ID != first.ID {
		t.Fatalf("first %+v, second %+v: the repeat must replay the first receipt", first, second)
	}

	// A dry run never consults or fills the cache, and no key means no
	// dedupe at all.
	if _, _, err := svc.Send(ctx, hola(), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Send(context.Background(), hola(), false); err != nil {
		t.Fatal(err)
	}
	if n := sender.calls.Load(); n != 2 {
		t.Fatalf("sent %d times, want 2 (the keyless send only)", n)
	}
}

func TestIdempotencyKeyReusedForDifferentMessageFails(t *testing.T) {
	sender := &gatedSender{}
	svc := idempotencyService(t, sender)
	ctx := core.WithIdempotencyKey(context.Background(), "k1")
	if _, _, err := svc.Send(ctx, hola(), false); err != nil {
		t.Fatal(err)
	}
	other := hola()
	other.Body = "chau"
	_, _, err := svc.Send(ctx, other, false)
	if err == nil || !strings.Contains(err.Error(), "different message") {
		t.Fatalf("err = %v, want a loud mismatch", err)
	}
	if n := sender.calls.Load(); n != 1 {
		t.Fatalf("sent %d times, want 1", n)
	}
}

func TestIdempotencyConcurrentDuplicatesSendOnce(t *testing.T) {
	sender := &gatedSender{gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	svc := idempotencyService(t, sender)
	ctx := core.WithIdempotencyKey(context.Background(), "k1")

	const callers = 5
	var wg sync.WaitGroup
	receipts := make([]core.Receipt, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, receipts[i], errs[i] = svc.Send(ctx, hola(), false)
		}()
	}
	// Let the first send reach the adapter before releasing it, so the
	// others find it in flight.
	<-sender.entered
	// Waiting for the other callers to block on the in-flight send is not
	// observable from outside, so give them a moment; correctness does
	// not depend on it (a late caller replays the finished receipt).
	time.Sleep(20 * time.Millisecond)
	close(sender.gate)
	wg.Wait()

	if n := sender.calls.Load(); n != 1 {
		t.Fatalf("sent %d times, want 1", n)
	}
	replayed := 0
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if receipts[i].ID != "R1" {
			t.Fatalf("caller %d got receipt %+v", i, receipts[i])
		}
		if receipts[i].Replayed {
			replayed++
		}
	}
	if replayed != callers-1 {
		t.Fatalf("%d replayed receipts, want %d", replayed, callers-1)
	}
}

func TestIdempotencyWaiterGivesUpWithItsContext(t *testing.T) {
	sender := &gatedSender{gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	svc := idempotencyService(t, sender)
	ctx := core.WithIdempotencyKey(context.Background(), "k1")

	done := make(chan error, 1)
	go func() {
		_, _, err := svc.Send(ctx, hola(), false)
		done <- err
	}()
	<-sender.entered
	short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, _, err := svc.Send(short, hola(), false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter err = %v, want its own deadline", err)
	}
	close(sender.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := sender.calls.Load(); n != 1 {
		t.Fatalf("sent %d times, want 1", n)
	}
}

func TestIdempotencyFailedSendCanBeRetried(t *testing.T) {
	sender := &gatedSender{}
	sender.fail.Store(true)
	svc := idempotencyService(t, sender)
	ctx := core.WithIdempotencyKey(context.Background(), "k1")

	if _, _, err := svc.Send(ctx, hola(), false); err == nil {
		t.Fatal("want the first send to fail")
	}
	sender.fail.Store(false)
	_, receipt, err := svc.Send(ctx, hola(), false)
	if err != nil {
		t.Fatal(err)
	}
	if n := sender.calls.Load(); n != 2 || receipt.Replayed {
		t.Fatalf("calls %d, receipt %+v: a failed send must not be remembered", n, receipt)
	}
}

func TestIdempotencyKeyExpires(t *testing.T) {
	sender := &gatedSender{}
	svc := idempotencyService(t, sender)
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	svc.SetIdempotencyClock(func() time.Time { return now })
	ctx := core.WithIdempotencyKey(context.Background(), "k1")

	if _, _, err := svc.Send(ctx, hola(), false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(core.IdempotencyTTL - time.Second)
	if _, r, _ := svc.Send(ctx, hola(), false); !r.Replayed {
		t.Fatal("within the TTL the key must still be remembered")
	}
	now = now.Add(2 * time.Second)
	if _, r, _ := svc.Send(ctx, hola(), false); r.Replayed {
		t.Fatal("after the TTL the key must be forgotten")
	}
	if n := sender.calls.Load(); n != 2 {
		t.Fatalf("sent %d times, want 2", n)
	}
}

func TestIdempotencyCacheIsBounded(t *testing.T) {
	sender := &gatedSender{}
	svc := idempotencyService(t, sender)
	first := core.WithIdempotencyKey(context.Background(), "k-first")
	if _, _, err := svc.Send(first, hola(), false); err != nil {
		t.Fatal(err)
	}
	for i := range core.IdempotencyMaxKeys {
		ctx := core.WithIdempotencyKey(context.Background(), "k"+strconv.Itoa(i))
		if _, _, err := svc.Send(ctx, hola(), false); err != nil {
			t.Fatal(err)
		}
	}
	// The oldest key was evicted to keep the bound, so it sends again.
	if _, r, _ := svc.Send(first, hola(), false); r.Replayed {
		t.Fatal("the least recently used key should have been evicted")
	}
}

func TestIdempotencyReplySendsOnce(t *testing.T) {
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{ID: "them@example.org"}, Subject: "hi"}
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelMail, account: "cl"}
	reg.Register(spy)
	svc := core.NewService(newMemStore(item), reg)
	ctx := core.WithIdempotencyKey(context.Background(), "r1")

	for range 2 {
		if _, _, err := svc.Reply(ctx, item.ID, "ok", nil, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	if spy.sendCalls != 1 {
		t.Fatalf("replied %d times, want 1", spy.sendCalls)
	}
}
