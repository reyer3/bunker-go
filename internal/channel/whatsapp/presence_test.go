package whatsapp

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// TestSetPresenceAvailableTrueSendsAvailableThenSubscribes asserts the
// exact sequence K3 requires: becoming available broadcasts
// PresenceAvailable first, then subscribes to thread — never the other
// order (subscribing while still unavailable would ask the server for
// updates it will not deliver).
func TestSetPresenceAvailableTrueSendsAvailableThenSubscribes(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli)

	if err := a.SetPresenceAvailable(context.Background(), true, "5511999999999@s.whatsapp.net"); err != nil {
		t.Fatalf("SetPresenceAvailable: %v", err)
	}

	want := []string{"presence:available", "subscribe:5511999999999@s.whatsapp.net"}
	if got := cli.callLog(); !equalStrings(got, want) {
		t.Fatalf("callLog = %v, want %v", got, want)
	}
}

// TestSetPresenceAvailableFalseOnlySendsUnavailable asserts going
// unavailable never subscribes to anything (whatsmeow has no explicit
// unsubscribe call; becoming unavailable is itself what stops WhatsApp
// delivering further updates, per the privacy Decision).
func TestSetPresenceAvailableFalseOnlySendsUnavailable(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli)

	if err := a.SetPresenceAvailable(context.Background(), false, "5511999999999@s.whatsapp.net"); err != nil {
		t.Fatalf("SetPresenceAvailable: %v", err)
	}

	want := []string{"presence:unavailable"}
	if got := cli.callLog(); !equalStrings(got, want) {
		t.Fatalf("callLog = %v, want %v", got, want)
	}
}

// TestPresenceReportsUnknownBeforeAnyEvent covers a thread never
// subscribed to (or not yet answered): Presence must report "unknown",
// never a zero-value guess.
func TestPresenceReportsUnknownBeforeAnyEvent(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli)

	p, err := a.Presence(context.Background(), "5511999999999@s.whatsapp.net")
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.State != "unknown" {
		t.Fatalf("State = %q, want unknown", p.State)
	}
}

// TestPresenceEventUpdatesCache proves an events.Presence received
// while subscribed updates what Presence answers, without any network
// round trip.
func TestPresenceEventUpdatesCache(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli)
	jid := mustJID(t, "5511999999999@s.whatsapp.net")

	a.handleEvent(context.Background(), nil, &events.Presence{From: jid, Unavailable: false}, nil)

	p, err := a.Presence(context.Background(), jid.String())
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.State != "online" {
		t.Fatalf("State = %q, want online", p.State)
	}

	a.handleEvent(context.Background(), nil, &events.Presence{From: jid, Unavailable: true}, nil)
	p, err = a.Presence(context.Background(), jid.String())
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.State != "offline" {
		t.Fatalf("State = %q, want offline", p.State)
	}
}

// TestChatPresenceEventTracksTypers proves a composing/paused
// events.ChatPresence updates State and Typers, and paused clears the
// sender back out of Typers.
func TestChatPresenceEventTracksTypers(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli)
	chat := mustJID(t, "5511999999999@s.whatsapp.net")

	a.handleEvent(context.Background(), nil, &events.ChatPresence{
		MessageSource: types.MessageSource{Chat: chat, Sender: chat},
		State:         types.ChatPresenceComposing,
	}, nil)
	p, err := a.Presence(context.Background(), chat.String())
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.State != "typing" || len(p.Typers) != 1 || p.Typers[0] != chat.String() {
		t.Fatalf("Presence after composing = %+v, want State=typing Typers=[%s]", p, chat)
	}

	a.handleEvent(context.Background(), nil, &events.ChatPresence{
		MessageSource: types.MessageSource{Chat: chat, Sender: chat},
		State:         types.ChatPresencePaused,
	}, nil)
	p, err = a.Presence(context.Background(), chat.String())
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if len(p.Typers) != 0 {
		t.Fatalf("Presence after paused Typers = %v, want none", p.Typers)
	}
}

// TestSendTypingForwardsComposingAndPaused proves SendTyping maps
// composing to types.ChatPresenceComposing and !composing to
// types.ChatPresencePaused, targeting thread's JID.
func TestSendTypingForwardsComposingAndPaused(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli)
	thread := "5511999999999@s.whatsapp.net"

	if err := a.SendTyping(context.Background(), thread, true); err != nil {
		t.Fatalf("SendTyping(composing): %v", err)
	}
	if err := a.SendTyping(context.Background(), thread, false); err != nil {
		t.Fatalf("SendTyping(paused): %v", err)
	}

	want := []string{"chatpresence:composing", "chatpresence:paused"}
	if got := cli.callLog(); !equalStrings(got, want) {
		t.Fatalf("callLog = %v, want %v", got, want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
