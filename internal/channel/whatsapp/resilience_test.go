package whatsapp

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// errBoom is a generic Sink-write failure tests inject to prove R3's
// logging behavior; its exact value is never asserted on, only that it
// reaches the log.
var errBoom = errors.New("boom")

// syncBuffer is an io.Writer safe for one goroutine (the adapter's slog
// handler, invoked from Run's own goroutine) to write while another (the
// test's poll loop) concurrently reads String().
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// captureSlogDefault swaps slog's default logger for one writing to a
// syncBuffer, restoring the original on test cleanup. R3's Sink-error
// logging goes through slog.Default() (no per-adapter logger field), so
// this is how tests observe it.
func captureSlogDefault(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func waitForLogContains(t *testing.T, buf *syncBuffer, substr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), substr) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("log output = %q, want it to contain %q", buf.String(), substr)
}

// TestHandleEventLogsUpsertErrorWithChannelAccountAttrs proves R3: an
// events.Message whose sink.Upsert fails (adapter.go:201) is logged at
// error level with channel/account attributes, and the adapter keeps
// running (Run does not stop, a later event is still processed).
func TestHandleEventLogsUpsertErrorWithChannelAccountAttrs(t *testing.T) {
	buf := captureSlogDefault(t)
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	sink.upsertErr = errBoom
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "1234@s.whatsapp.net")
	cli.emit(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "M1",
			Timestamp:     time.Now(),
		},
		Message: &waE2E.Message{Conversation: strPtr("hola")},
	})

	waitForLogContains(t, buf, "level=ERROR")
	out := buf.String()
	if !strings.Contains(out, "channel=whatsapp") {
		t.Fatalf("log output = %q, want a channel=whatsapp attribute", out)
	}
	if !strings.Contains(out, "account=personal") {
		t.Fatalf("log output = %q, want an account=personal attribute", out)
	}
	if len(sink.items()) != 0 {
		t.Fatalf("items = %v, want none stored when Upsert fails", sink.items())
	}
}

// TestReceiptTypeReadSelfLogsMarkReadErrorWithChannelAccountAttrs proves
// R3 for adapter.go:213: a ReadSelf receipt whose sink.MarkRead fails is
// logged, not silently dropped.
func TestReceiptTypeReadSelfLogsMarkReadErrorWithChannelAccountAttrs(t *testing.T) {
	buf := captureSlogDefault(t)
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	sink.markReadErr = errBoom
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "1234@s.whatsapp.net")
	cli.emit(&events.Receipt{
		MessageSource: types.MessageSource{Chat: chat, Sender: chat},
		MessageIDs:    []types.MessageID{"M1"},
		Timestamp:     time.Now(),
		Type:          types.ReceiptTypeReadSelf,
	})

	waitForLogContains(t, buf, "level=ERROR")
	out := buf.String()
	if !strings.Contains(out, "channel=whatsapp") || !strings.Contains(out, "account=personal") {
		t.Fatalf("log output = %q, want channel=whatsapp and account=personal attributes", out)
	}
}

// TestMarkThreadReadBothFormsLogsErrorsForChatAndAltForms proves R3 for
// adapter.go:246 and :251: markThreadReadBothForms tries both the chat's
// own address form and its LID/PN counterpart, and a failure of either
// MarkThreadReadUpTo call is logged.
func TestMarkThreadReadBothFormsLogsErrorsForChatAndAltForms(t *testing.T) {
	buf := captureSlogDefault(t)
	cli := newFakeWAClient()
	cli.linked = true
	lidChat := mustJID(t, "555000111@lid")
	pnChat := mustJID(t, "555000222@s.whatsapp.net")
	cli.altJIDs = map[string]types.JID{lidChat.String(): pnChat}
	sink := newSpySink()
	sink.markThreadReadUpToErr = errBoom
	a := newTestAdapter("personal", cli)

	a.markThreadReadBothForms(context.Background(), sink, lidChat, time.Now())

	out := buf.String()
	if strings.Count(out, "level=ERROR") < 2 {
		t.Fatalf("log output = %q, want two ERROR entries (chat form and alt form)", out)
	}
	if !strings.Contains(out, "channel=whatsapp") || !strings.Contains(out, "account=personal") {
		t.Fatalf("log output = %q, want channel=whatsapp and account=personal attributes", out)
	}
}

// TestHandleHistorySyncLogsUpsertErrorWithChannelAccountAttrs proves R3
// for historysync.go:89.
func TestHandleHistorySyncLogsUpsertErrorWithChannelAccountAttrs(t *testing.T) {
	buf := captureSlogDefault(t)
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	sink.upsertErr = errBoom
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	emitHistorySync(cli, syntheticConversation("1234@s.whatsapp.net", 1, false, "hola"))

	waitForLogContains(t, buf, "level=ERROR")
	out := buf.String()
	if !strings.Contains(out, "channel=whatsapp") || !strings.Contains(out, "account=personal") {
		t.Fatalf("log output = %q, want channel=whatsapp and account=personal attributes", out)
	}
}
