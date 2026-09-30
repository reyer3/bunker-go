package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
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

// TestReceiptTypeReadDoesNotMarkOurItemsRead proves the decision that
// types.ReceiptTypeRead means OTHERS read OUR messages (delivery/blue
// ticks on something we sent) and must never affect our own unread
// state: it must not call sink.MarkRead nor sink.MarkThreadReadUpTo.
func TestReceiptTypeReadDoesNotMarkOurItemsRead(t *testing.T) {
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

	// fakeWAClient.emit runs the handler synchronously, so handleEvent
	// has already returned: nothing to wait for before asserting.
	if len(sink.markedRead) != 0 {
		t.Errorf("MarkRead calls = %+v, want none for ReceiptTypeRead", sink.markedRead)
	}
	if calls := sink.threadReadUpToCalls(); len(calls) != 0 {
		t.Errorf("MarkThreadReadUpTo calls = %+v, want none for ReceiptTypeRead", calls)
	}
}

// TestReceiptTypeReadSelfMarksIndividualIDsAndThread proves ReadSelf (we
// read a chat from a different device) still marks each listed message
// id read (existing per-ID behavior, T9-era) and additionally marks the
// whole thread read up to the receipt's timestamp, so a ReadSelf that
// does not list every unread message of the chat (Evidence gap (a))
// still clears the rest.
func TestReceiptTypeReadSelfMarksIndividualIDsAndThread(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "1234@s.whatsapp.net")
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cli.emit(&events.Receipt{
		MessageSource: types.MessageSource{Chat: chat, Sender: chat},
		MessageIDs:    []types.MessageID{"M1"},
		Timestamp:     ts,
		Type:          types.ReceiptTypeReadSelf,
	})

	waitFor(t, func() bool { return len(sink.markedRead) == 1 })
	want := itemID("personal", "1234@s.whatsapp.net", "M1")
	if sink.markedRead[0].id != want || !sink.markedRead[0].read {
		t.Errorf("MarkRead call = %+v, want id=%q read=true", sink.markedRead[0], want)
	}

	waitFor(t, func() bool { return len(sink.threadReadUpToCalls()) >= 1 })
	calls := sink.threadReadUpToCalls()
	found := false
	for _, c := range calls {
		if c.channel == core.ChannelWhatsApp && c.account == "personal" && c.thread == "1234@s.whatsapp.net" && c.upTo.Equal(ts) {
			found = true
		}
	}
	if !found {
		t.Errorf("MarkThreadReadUpTo calls = %+v, want one for thread 1234@s.whatsapp.net up to %v", calls, ts)
	}
}

// TestReceiptTypeReadSelfResolvesLIDPNThreadKey proves the LID/PN
// resolution decision: when the receipt's chat arrives in one address
// form, MarkThreadReadUpTo is also tried against its stored LID/PN
// counterpart, since the conversation's items may have been persisted
// under whichever form the first message used (Evidence gap (b)).
func TestReceiptTypeReadSelfResolvesLIDPNThreadKey(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	lidChat := mustJID(t, "555000111@lid")
	pnChat := mustJID(t, "555000222@s.whatsapp.net")
	cli.altJIDs = map[string]types.JID{lidChat.String(): pnChat}
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cli.emit(&events.Receipt{
		MessageSource: types.MessageSource{Chat: lidChat, Sender: lidChat},
		MessageIDs:    []types.MessageID{"M1"},
		Timestamp:     ts,
		Type:          types.ReceiptTypeReadSelf,
	})

	waitFor(t, func() bool { return len(sink.threadReadUpToCalls()) >= 2 })
	calls := sink.threadReadUpToCalls()
	var sawLID, sawPN bool
	for _, c := range calls {
		if c.thread == lidChat.String() {
			sawLID = true
		}
		if c.thread == pnChat.String() {
			sawPN = true
		}
	}
	if !sawLID || !sawPN {
		t.Errorf("MarkThreadReadUpTo calls = %+v, want both %q and %q", calls, lidChat.String(), pnChat.String())
	}
}

// TestMarkChatAsReadActionReadTrueMarksThread proves the decision for
// events.MarkChatAsRead (the phone's own "mark as read" app-state
// mutation, Evidence gap (c)): Action.Read == true marks the chat's
// thread read up to the action's message-range cutoff.
func TestMarkChatAsReadActionReadTrueMarksThread(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "1234@s.whatsapp.net")
	lastMsgTS := int64(1767322800) // fictional fixed instant
	read := true
	cli.emit(&events.MarkChatAsRead{
		JID:       chat,
		Timestamp: time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC),
		Action: &waSyncAction.MarkChatAsReadAction{
			Read:         &read,
			MessageRange: &waSyncAction.SyncActionMessageRange{LastMessageTimestamp: &lastMsgTS},
		},
	})

	waitFor(t, func() bool { return len(sink.threadReadUpToCalls()) >= 1 })
	calls := sink.threadReadUpToCalls()
	want := time.Unix(lastMsgTS, 0)
	if calls[0].channel != core.ChannelWhatsApp || calls[0].account != "personal" || calls[0].thread != "1234@s.whatsapp.net" || !calls[0].upTo.Equal(want) {
		t.Errorf("MarkThreadReadUpTo call = %+v, want channel=whatsapp account=personal thread=1234@s.whatsapp.net upTo=%v", calls[0], want)
	}
}

// TestMarkChatAsReadActionReadFalseIsNoOp proves the decision that
// unmarking a chat as read (Action.Read == false) never re-marks
// anything unread: bunker's Sink has no "mark unread" op for this path.
func TestMarkChatAsReadActionReadFalseIsNoOp(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "1234@s.whatsapp.net")
	read := false
	cli.emit(&events.MarkChatAsRead{
		JID:       chat,
		Timestamp: time.Now(),
		Action:    &waSyncAction.MarkChatAsReadAction{Read: &read},
	})

	// emit is synchronous: handleEvent has already returned.
	if calls := sink.threadReadUpToCalls(); len(calls) != 0 {
		t.Errorf("MarkThreadReadUpTo calls = %+v, want none for Action.Read == false", calls)
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

			// emit is synchronous: handleEvent has already returned, so
			// the count is final.
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

	// emit is synchronous: handleEvent has already returned.
	if got := len(sink.items()); got != 0 {
		t.Fatalf("sink items = %d, want 0 (status@broadcast must be dropped)", got)
	}
}

// TestRunDropsNewsletterMessages: posts from WhatsApp channels
// (newsletters) are feed noise like statuses, so they never reach the
// WhatsApp list.
func TestRunDropsNewsletterMessages(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	channel := mustJID(t, "120363000000000001@newsletter")
	cli.emit(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: channel, Sender: channel},
			ID:            types.MessageID("NEWS-1"),
			Timestamp:     time.Now(),
		},
		Message: &waE2E.Message{Conversation: strPtr("novedades de la semana")},
	})

	// emit is synchronous: handleEvent has already returned.
	if got := len(sink.items()); got != 0 {
		t.Fatalf("sink items = %d, want 0 (newsletter posts must be dropped)", got)
	}
}
