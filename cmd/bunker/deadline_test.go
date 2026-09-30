package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

// TestCommandTimeoutSendAndReplyGetTheLongDeadline covers T13(e): send
// and reply (which may take minutes under human emulation and a
// broadcast's pauses) get a long deadline; every other command keeps the
// short one render already effectively proved necessary.
func TestCommandTimeoutSendAndReplyGetTheLongDeadline(t *testing.T) {
	for _, cmd := range []string{"send", "reply"} {
		if got := commandTimeout(cmd); got != sendReplyTimeout {
			t.Errorf("commandTimeout(%q) = %v, want the long %v deadline", cmd, got, sendReplyTimeout)
		}
	}
}

func TestCommandTimeoutOtherCommandsGetTheShortDeadline(t *testing.T) {
	for _, cmd := range []string{"list", "read", "counts", "organize", "status"} {
		if got := commandTimeout(cmd); got != shortCommandTimeout {
			t.Errorf("commandTimeout(%q) = %v, want the short %v deadline", cmd, got, shortCommandTimeout)
		}
	}
}

// slowSendAdapter is a core.Sender that sleeps briefly before replying,
// standing in for WhatsApp's real human-emulation delay without this
// test actually waiting minutes: it proves the long send/reply deadline
// is what lets a slow send finish instead of proving a specific
// duration.
type slowSendAdapter struct {
	channel core.Channel
	account string
	delay   time.Duration
}

func (s *slowSendAdapter) Channel() core.Channel                         { return s.channel }
func (s *slowSendAdapter) Account() string                               { return s.account }
func (s *slowSendAdapter) Run(ctx context.Context, sink core.Sink) error { return nil }
func (s *slowSendAdapter) Send(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	// The slow peer is the thing under test: the send must outlast the
	// caller deadline, so a real delay is the fixture.
	time.Sleep(s.delay)
	return core.Receipt{ID: "slow-1", Channel: s.channel, At: time.Now()}, nil
}

// TestRunSendDoesNotTimeOutOnASlowServer covers T13(e) end to end
// through run(): a send whose server-side handling takes longer than
// render's 200ms budget (and longer than a short default timeout would
// allow) still completes, because "send" gets the long deadline.
func TestRunSendDoesNotTimeOutOnASlowServer(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	reg := core.NewRegistry()
	reg.Register(&slowSendAdapter{channel: core.ChannelWhatsApp, account: "wa", delay: 300 * time.Millisecond})
	svc := core.NewService(st, reg)
	srv := rpc.NewServer(svc)
	socket := filepath.Join(dir, "bunker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, socket) }()
	t.Cleanup(func() { cancel(); <-serveErr })

	dialUntilReady(t, socket).Close()
	t.Setenv("BUNKER_SOCKET", socket)

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	code := run([]string{"send", "whatsapp", "wa", "+51999", "hola"}, os.Stdin, stdoutW, os.Stderr)
	stdoutW.Close()
	buf := make([]byte, 4096)
	stdoutR.Read(buf)
	if code != 0 {
		t.Fatalf("run() exit code = %d, want 0 (a 300ms server delay must not time out under the long send deadline)", code)
	}
}
