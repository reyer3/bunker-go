package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// slowSender is a WhatsApp adapter whose first real send is held open
// until the test releases it, like a send stuck in human pacing.
type slowSender struct {
	calls   atomic.Int32
	release chan struct{}
}

func (s *slowSender) Channel() core.Channel                      { return core.ChannelWhatsApp }
func (s *slowSender) Account() string                            { return "personal" }
func (s *slowSender) Run(ctx context.Context, _ core.Sink) error { return nil }
func (s *slowSender) Send(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	if s.calls.Add(1) == 1 {
		<-s.release
	}
	return core.Receipt{ID: fmt.Sprintf("R%d", s.calls.Load()), Channel: core.ChannelWhatsApp, At: time.Now()}, nil
}

// daemonBackend runs sends the way the daemon does behind the socket:
// on the daemon's own context, which outlives the client's deadline. A
// client that times out gets an error while the send carries on, which
// is exactly the case a retry must not double.
type daemonBackend struct {
	*core.Service
}

func (d daemonBackend) Send(ctx context.Context, out core.Outgoing, dryRun bool) (core.Plan, core.Receipt, error) {
	type result struct {
		plan    core.Plan
		receipt core.Receipt
		err     error
	}
	done := make(chan result, 1)
	daemonCtx := core.WithIdempotencyKey(context.Background(), core.IdempotencyKey(ctx))
	go func() {
		plan, receipt, err := d.Service.Send(daemonCtx, out, dryRun)
		done <- result{plan, receipt, err}
	}()
	select {
	case r := <-done:
		return r.plan, r.receipt, r.err
	case <-ctx.Done():
		return core.Plan{}, core.Receipt{}, fmt.Errorf("rpc: read response: %w", ctx.Err())
	}
}

func TestMCPSendRetryAfterTimeoutSendsOnce(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	adapter := &slowSender{release: make(chan struct{})}
	reg := core.NewRegistry()
	reg.Register(adapter)
	backend := daemonBackend{core.NewService(st, reg)}

	saved := mcpSendTimeout
	t.Cleanup(func() { mcpSendTimeout = saved })
	mcpSendTimeout = 50 * time.Millisecond

	dial := func(context.Context) (Backend, io.Closer, error) { return backend, nil, nil }
	server := newMCPServer(dial, true)
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), serverT, nil); err != nil {
		t.Fatal(err)
	}
	s, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	args := map[string]any{"channel": "whatsapp", "account": "personal", "to": "51900@s.whatsapp.net", "text": "hola", "confirm": true}
	res, _ := callTool(t, s, "send", args)
	if !res.IsError || !strings.Contains(toolText(res), "deadline") {
		t.Fatalf("the first confirm should time out: %s", toolText(res))
	}

	// The agent retries while the first send is still on its way: the
	// retry waits for it and gets its receipt instead of sending again.
	mcpSendTimeout = 5 * time.Second
	go func() {
		// The retry blocking on the in-flight send is not observable, so
		// give it a moment; the outcome holds either way (a retry that
		// arrives after the release replays the finished receipt).
		time.Sleep(20 * time.Millisecond)
		close(adapter.release)
	}()
	res, out := callTool(t, s, "send", args)
	if res.IsError || out["sent"] != true {
		t.Fatalf("retry: %v %s", out, toolText(res))
	}
	receipt, _ := out["receipt"].(map[string]any)
	if receipt["id"] != "R1" || receipt["replayed"] != true {
		t.Fatalf("retry receipt = %v, want the first send's, replayed", receipt)
	}

	// And once more after it finished.
	_, out = callTool(t, s, "send", args)
	if receipt, _ := out["receipt"].(map[string]any); receipt["id"] != "R1" {
		t.Fatalf("third call receipt = %v", receipt)
	}
	if n := adapter.calls.Load(); n != 1 {
		t.Fatalf("the adapter sent %d times, want 1", n)
	}
}

func TestMCPIdempotencyKeyFollowsThePlan(t *testing.T) {
	plan := core.Plan{Action: "send", Channel: core.ChannelWhatsApp, Account: "personal", Recipients: []string{"a"}, Preview: "hola"}
	same := plan
	if mcpIdempotencyKey(plan) != mcpIdempotencyKey(same) {
		t.Fatal("the key must be deterministic")
	}
	for name, change := range map[string]func(*core.Plan){
		"text":        func(p *core.Plan) { p.Preview = "chau" },
		"recipient":   func(p *core.Plan) { p.Recipients = []string{"b"} },
		"account":     func(p *core.Plan) { p.Account = "work" },
		"attachments": func(p *core.Plan) { p.Media = []string{"/tmp/a.png"} },
		"target":      func(p *core.Plan) { p.Target = "whatsapp:personal:2" },
	} {
		other := plan
		change(&other)
		if mcpIdempotencyKey(other) == mcpIdempotencyKey(plan) {
			t.Errorf("a different %s must give a different key", name)
		}
	}
}
