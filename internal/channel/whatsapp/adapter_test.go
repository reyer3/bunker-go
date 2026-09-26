package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestAdapterChannelAndAccount(t *testing.T) {
	a := newTestAdapter("personal", newFakeWAClient())
	if a.Channel() != core.ChannelWhatsApp {
		t.Errorf("Channel() = %v, want %v", a.Channel(), core.ChannelWhatsApp)
	}
	if a.Account() != "personal" {
		t.Errorf("Account() = %q, want %q", a.Account(), "personal")
	}
}

func TestRunReturnsErrNotLinkedWithoutADevice(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = false
	a := newTestAdapter("personal", cli)

	err := a.Run(context.Background(), newSpySink())
	if !errors.Is(err, ErrNotLinked) {
		t.Fatalf("Run() error = %v, want ErrNotLinked", err)
	}
	if cli.IsConnected() {
		t.Errorf("Connect() was called although the device is not linked")
	}
}

func TestRunConnectsUpsertsMessagesAndStopsOnContextCancel(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx, sink) }()

	// Give Run a moment to call Connect and register its handler.
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

	waitFor(t, func() bool { return len(sink.items()) == 1 })
	if got := sink.items()[0].Body; got != "hola" {
		t.Errorf("upserted body = %q, want %q", got, "hola")
	}

	cancel()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
	if cli.IsConnected() {
		t.Errorf("Disconnect() was not called on ctx cancel")
	}
}

func TestRunMarksReadOnReadReceipt(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "1234@s.whatsapp.net")
	cli.emit(&events.Receipt{
		MessageSource: types.MessageSource{Chat: chat, Sender: chat},
		MessageIDs:    []types.MessageID{"M1"},
		Type:          types.ReceiptTypeRead,
	})

	waitFor(t, func() bool { return len(sink.markedRead) == 1 })
	want := itemID("personal", "1234@s.whatsapp.net", "M1")
	if sink.markedRead[0].id != want || !sink.markedRead[0].read {
		t.Errorf("MarkRead call = %+v, want id=%q read=true", sink.markedRead[0], want)
	}
}

func TestRunReturnsErrLoggedOutOnLogout(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx, sink) }()
	waitFor(t, func() bool { return cli.IsConnected() })

	cli.emit(&events.LoggedOut{})

	select {
	case err := <-runErr:
		if !errors.Is(err, ErrLoggedOut) {
			t.Errorf("Run() error = %v, want ErrLoggedOut", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after LoggedOut event")
	}
}

func TestFetchReturnsCachedItem(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "1234@s.whatsapp.net")
	cli.emit(&events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "M1"},
		Message: &waE2E.Message{Conversation: strPtr("cacheado")},
	})
	id := itemID("personal", "1234@s.whatsapp.net", "M1")
	waitFor(t, func() bool {
		item, err := a.Fetch(context.Background(), id)
		return err == nil && item.Body == "cacheado"
	})
}

func TestFetchUnknownIDIsErrNotFound(t *testing.T) {
	a := newTestAdapter("personal", newFakeWAClient())
	_, err := a.Fetch(context.Background(), "whatsapp:personal:1234@s.whatsapp.net/UNKNOWN")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Fetch() error = %v, want core.ErrNotFound", err)
	}
}

// TestRunDropsNonContentMessages covers the first live-link finding
// (2026-09-25): a newly linked device receives plenty of *events.Message
// traffic that is not user content at all (app-state key distribution,
// history-sync notifications, sender-key distribution), and those must
// never reach the sink as empty Items. Reactions, edits and revokes are
// included here too: toItem does not extract a body or media from them
// either, so they are dropped the same way for now (see the TODO next to
// isSurfaceable in message.go).
func TestRunDropsNonContentMessages(t *testing.T) {
	chat := mustJID(t, "1234@s.whatsapp.net")

	cases := []struct {
		name    string
		message *waE2E.Message
		wantOne bool
	}{
		{
			name: "protocol message only",
			message: &waE2E.Message{
				ProtocolMessage: &waE2E.ProtocolMessage{
					Type: waE2E.ProtocolMessage_APP_STATE_SYNC_KEY_SHARE.Enum(),
				},
			},
			wantOne: false,
		},
		{
			name: "sender key distribution only",
			message: &waE2E.Message{
				SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{
					GroupID: strPtr("1234@g.us"),
				},
			},
			wantOne: false,
		},
		{
			name:    "empty message, no text, no media",
			message: &waE2E.Message{},
			wantOne: false,
		},
		{
			name:    "real text is surfaced",
			message: &waE2E.Message{Conversation: strPtr("hola de verdad")},
			wantOne: true,
		},
		{
			name: "media with no caption is still surfaced",
			message: &waE2E.Message{
				ImageMessage: &waE2E.ImageMessage{
					Mimetype:   strPtr("image/jpeg"),
					FileLength: uint64Ptr(10),
					DirectPath: strPtr("/v/abc"),
				},
			},
			wantOne: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli := newFakeWAClient()
			cli.linked = true
			sink := newSpySink()
			a := newTestAdapter("personal", cli)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go a.Run(ctx, sink)
			waitFor(t, func() bool { return cli.IsConnected() })

			cli.emit(&events.Message{
				Info: types.MessageInfo{
					MessageSource: types.MessageSource{Chat: chat, Sender: chat},
					ID:            types.MessageID("M-" + tc.name),
					Timestamp:     time.Now(),
				},
				Message: tc.message,
			})

			// Give the handler a moment to (not) upsert, then settle on
			// the final count instead of racing a fixed sleep.
			deadline := time.Now().Add(200 * time.Millisecond)
			for time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			got := len(sink.items())
			if tc.wantOne && got != 1 {
				t.Fatalf("sink items = %d, want 1 (content message must be upserted)", got)
			}
			if !tc.wantOne && got != 0 {
				t.Fatalf("sink items = %d, want 0 (non-content message must be dropped)", got)
			}
		})
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

// Contacts' status updates arrive as messages in status@broadcast. They
// are feed noise, not conversations, so they must never become Items even
// when they carry real text or media.
func TestRunDropsStatusBroadcastMessages(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	poster := mustJID(t, "5678@s.whatsapp.net")
	cli.emit(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.StatusBroadcastJID, Sender: poster},
			ID:            types.MessageID("STATUS-1"),
			Timestamp:     time.Now(),
		},
		Message: &waE2E.Message{Conversation: strPtr("mi estado de hoy")},
	})

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(sink.items()); got != 0 {
		t.Fatalf("sink items = %d, want 0 (status@broadcast must be dropped)", got)
	}
}
