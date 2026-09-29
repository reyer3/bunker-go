package whatsapp

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

const (
	testChat  = "1234@s.whatsapp.net"
	testGroup = "120363000000000001@g.us"
	testOwn   = "5550001@s.whatsapp.net"
)

func modifyClient() *fakeWAClient {
	cli := newFakeWAClient()
	cli.ownJID = types.NewADJID("5550001", 0, 7) // this linked device
	cli.sendResp = whatsmeow.SendResponse{ID: "PROTO1", Timestamp: time.Unix(2000, 0)}
	return cli
}

func TestEditMessageSendsEditWithComposing(t *testing.T) {
	cli := modifyClient()
	a := newTestAdapter("personal", cli, time.Millisecond)
	item := core.Item{ID: itemID("personal", testChat, "M1"), FromMe: true, Body: "hola"}

	receipt, err := a.EditMessage(context.Background(), item, "hola, corregido")
	if err != nil {
		t.Fatal(err)
	}
	if want := itemID("personal", testChat, "PROTO1"); receipt.ID != want {
		t.Errorf("receipt ID = %q, want %q", receipt.ID, want)
	}
	if len(cli.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(cli.sent))
	}
	proto := cli.sent[0].message.GetEditedMessage().GetMessage().GetProtocolMessage()
	if proto.GetType() != waE2E.ProtocolMessage_MESSAGE_EDIT || proto.GetKey().GetID() != "M1" || !proto.GetKey().GetFromMe() {
		t.Errorf("edit protocol message = %+v", proto)
	}
	if got := proto.GetEditedMessage().GetConversation(); got != "hola, corregido" {
		t.Errorf("edited text = %q", got)
	}
	want := []string{"presence:available", "chatpresence:composing", "chatpresence:paused", "send", "presence:unavailable"}
	if got := cli.callLog(); !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v (an edit is typed like a send)", got, want)
	}
}

func TestDeleteMessageRevokesOwnMessageWithTapPause(t *testing.T) {
	cli := modifyClient()
	a := newTestAdapter("personal", cli, time.Millisecond)
	var slept []time.Duration
	a.SetSleeper(func(d time.Duration) { slept = append(slept, d) })
	item := core.Item{ID: itemID("personal", testChat, "M1"), FromMe: true, Body: "ups"}

	if _, err := a.DeleteMessage(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	proto := cli.sent[0].message.GetProtocolMessage()
	if proto.GetType() != waE2E.ProtocolMessage_REVOKE || proto.GetKey().GetID() != "M1" || !proto.GetKey().GetFromMe() {
		t.Errorf("revoke = %+v", proto)
	}
	if want := []string{"presence:available", "send", "presence:unavailable"}; !slices.Equal(cli.callLog(), want) {
		t.Errorf("calls = %v, want %v", cli.callLog(), want)
	}
	if len(slept) != 1 || slept[0] < tapPauseMin || slept[0] > tapPauseMax {
		t.Errorf("paused %v, want one pause within [%v, %v]", slept, tapPauseMin, tapPauseMax)
	}
}

func TestReactKeysOtherPeoplesGroupMessageBySender(t *testing.T) {
	cli := modifyClient()
	a := newTestAdapter("personal", cli, time.Millisecond)
	item := core.Item{ID: itemID("personal", testGroup, "G1"), From: core.Address{ID: "5550002:3@s.whatsapp.net"}}

	if _, err := a.React(context.Background(), item, "👍"); err != nil {
		t.Fatal(err)
	}
	reaction := cli.sent[0].message.GetReactionMessage()
	key := reaction.GetKey()
	if reaction.GetText() != "👍" || key.GetFromMe() || key.GetParticipant() != "5550002@s.whatsapp.net" || key.GetID() != "G1" {
		t.Errorf("reaction = %+v", reaction)
	}
}

func TestReactEmptyEmojiRemovesReaction(t *testing.T) {
	cli := modifyClient()
	a := newTestAdapter("personal", cli, time.Millisecond)
	item := core.Item{ID: itemID("personal", testChat, "M1"), FromMe: true}

	if _, err := a.React(context.Background(), item, ""); err != nil {
		t.Fatal(err)
	}
	reaction := cli.sent[0].message.GetReactionMessage()
	if reaction.Text == nil || reaction.GetText() != "" || !reaction.GetKey().GetFromMe() {
		t.Errorf("removal = %+v, want an empty reaction text on our own message", reaction)
	}
}

func TestReactRefusesGroupMessageWithoutSender(t *testing.T) {
	cli := modifyClient()
	a := newTestAdapter("personal", cli, time.Millisecond)
	if _, err := a.React(context.Background(), core.Item{ID: itemID("personal", testGroup, "G1")}, "👍"); err == nil {
		t.Fatal("reacted to a group message with no known sender")
	}
	if len(cli.sent) != 0 {
		t.Fatal("something was sent")
	}
}

func TestModifyFailsLoudlyWhenSendFails(t *testing.T) {
	cli := modifyClient()
	cli.sendErr = errors.New("not connected")
	a := newTestAdapter("personal", cli, time.Millisecond)
	if _, err := a.DeleteMessage(context.Background(), core.Item{ID: itemID("personal", testChat, "M1"), FromMe: true}); err == nil {
		t.Fatal("a failed revoke reported success")
	}
	if got := cli.callLog(); got[len(got)-1] != "presence:unavailable" {
		t.Errorf("calls = %v: presence must go back to unavailable even on failure", got)
	}
}

// TestServiceEditHonorsWhatsAppEditWindow drives the edit window end to
// end: the adapter declares whatsmeow's EditWindow and Service refuses a
// later edit before anything is sent, on dry-run too.
func TestServiceEditHonorsWhatsAppEditWindow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	fresh := core.Item{ID: itemID("personal", testChat, "FRESH"), Channel: core.ChannelWhatsApp, Account: "personal", Thread: testChat, Body: "hola", FromMe: true, Timestamp: now.Add(-whatsmeow.EditWindow + time.Minute)}
	stale := fresh
	stale.ID = itemID("personal", testChat, "STALE")
	stale.Timestamp = now.Add(-whatsmeow.EditWindow - time.Minute)
	for _, it := range []core.Item{fresh, stale} {
		if err := st.Upsert(context.Background(), it); err != nil {
			t.Fatal(err)
		}
	}
	cli := modifyClient()
	reg := core.NewRegistry()
	reg.Register(newTestAdapter("personal", cli, time.Millisecond))
	svc := core.NewService(st, reg)
	svc.SetMessageClock(func() time.Time { return now })

	for _, dryRun := range []bool{true, false} {
		if _, _, err := svc.EditMessage(context.Background(), stale.ID, "tarde", dryRun); !errors.Is(err, core.ErrWindowExpired) {
			t.Errorf("stale edit (dryRun=%v): %v, want ErrWindowExpired", dryRun, err)
		}
	}
	if len(cli.sent) != 0 {
		t.Fatal("a stale edit was sent")
	}
	if _, _, err := svc.EditMessage(context.Background(), fresh.ID, "a tiempo", false); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(context.Background(), fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "a tiempo" || !got.Edited {
		t.Errorf("stored = %+v, want the edited body", got)
	}

	if _, _, err := svc.React(context.Background(), fresh.ID, "🙏", false); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Get(context.Background(), fresh.ID)
	if len(got.Reactions) != 1 || got.Reactions[0] != (core.Reaction{Sender: testOwn, Emoji: "🙏"}) {
		t.Errorf("reactions = %+v, want ours keyed by our JID without device", got.Reactions)
	}
}

// TestRunKeysOwnReactionFromPhoneLikeReact checks the inbound half of
// "no double reaction": our reaction made on the phone (another device
// of ours) is stored under OwnReactionSender, the same row React writes.
func TestRunKeysOwnReactionFromPhoneLikeReact(t *testing.T) {
	cli := modifyClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, testChat)
	cli.emit(&events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "M1", Timestamp: time.Now()},
		Message: &waE2E.Message{Conversation: strPtr("hola")},
	})
	waitFor(t, func() bool { return len(sink.items()) == 1 })
	phone := types.NewADJID("5550001", 0, 0)
	cli.emit(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: phone, IsFromMe: true}, ID: "R1", Timestamp: time.Now()},
		Message: &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
			Key:  &waCommon.MessageKey{ID: strPtr("M1")},
			Text: strPtr("😂"),
		}},
	})
	waitFor(t, func() bool { return len(sink.items()[0].Reactions) == 1 })
	if got := sink.items()[0].Reactions[0]; got != (core.Reaction{Sender: testOwn, Emoji: "😂"}) {
		t.Errorf("reaction = %+v, want it keyed by %s", got, testOwn)
	}
}
