package whatsapp

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestRunAppliesWhatsAppEditToStoredItem pins S2's edit model: a
// *events.Message carrying a ProtocolMessage of type MESSAGE_EDIT
// replaces the target item's stored body and sets Edited, instead of
// being dropped as unsurfaceable (message.go's former TODO).
func TestRunAppliesWhatsAppEditToStoredItem(t *testing.T) {
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
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "M1", Timestamp: time.Now()},
		Message: &waE2E.Message{Conversation: strPtr("hola")},
	})
	waitFor(t, func() bool { return len(sink.items()) == 1 })

	cli.emit(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "M2", Timestamp: time.Now()},
		Message: &waE2E.Message{
			ProtocolMessage: &waE2E.ProtocolMessage{
				Key:           &waCommon.MessageKey{ID: strPtr("M1")},
				Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
				EditedMessage: &waE2E.Message{Conversation: strPtr("hola editado")},
			},
		},
	})

	id := itemID("personal", chat.String(), "M1")
	waitFor(t, func() bool {
		for _, item := range sink.items() {
			if item.ID == id && item.Edited {
				return true
			}
		}
		return false
	})
	var edited core.Item
	for _, item := range sink.items() {
		if item.ID == id {
			edited = item
		}
	}
	if edited.Body != "hola editado" {
		t.Fatalf("edited item body = %q, want %q", edited.Body, "hola editado")
	}
	if len(sink.items()) != 1 {
		t.Fatalf("an edit must not upsert a second item, got %d items", len(sink.items()))
	}
}

// TestRunAppliesWhatsAppRevokeToStoredItem pins S2's revoke model: a
// ProtocolMessage of type REVOKE clears the target item's body and sets
// Deleted, keeping the row (spySink's items() still lists it).
func TestRunAppliesWhatsAppRevokeToStoredItem(t *testing.T) {
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
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "M1", Timestamp: time.Now()},
		Message: &waE2E.Message{Conversation: strPtr("hola")},
	})
	waitFor(t, func() bool { return len(sink.items()) == 1 })

	cli.emit(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "M3", Timestamp: time.Now()},
		Message: &waE2E.Message{
			ProtocolMessage: &waE2E.ProtocolMessage{
				Key:  &waCommon.MessageKey{ID: strPtr("M1")},
				Type: waE2E.ProtocolMessage_REVOKE.Enum(),
			},
		},
	})

	id := itemID("personal", chat.String(), "M1")
	waitFor(t, func() bool {
		for _, item := range sink.items() {
			if item.ID == id && item.Deleted {
				return true
			}
		}
		return false
	})
	for _, item := range sink.items() {
		if item.ID == id && item.Body != "" {
			t.Fatalf("revoked item body = %q, want empty", item.Body)
		}
	}
}

// TestRunAppliesWhatsAppReactionToStoredItem pins S2's reaction model: a
// ReactionMessage stores {sender, emoji} on the target item, and an
// empty text (a reaction removal) clears it again.
func TestRunAppliesWhatsAppReactionToStoredItem(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	chat := mustJID(t, "1234@s.whatsapp.net")
	sender := mustJID(t, "5511999999999@s.whatsapp.net")
	cli.emit(&events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "M1", Timestamp: time.Now()},
		Message: &waE2E.Message{Conversation: strPtr("hola")},
	})
	waitFor(t, func() bool { return len(sink.items()) == 1 })

	cli.emit(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: sender}, ID: "M4", Timestamp: time.Now()},
		Message: &waE2E.Message{
			ReactionMessage: &waE2E.ReactionMessage{
				Key:  &waCommon.MessageKey{ID: strPtr("M1")},
				Text: strPtr("👍"),
			},
		},
	})

	id := itemID("personal", chat.String(), "M1")
	waitFor(t, func() bool {
		for _, item := range sink.items() {
			if item.ID == id && len(item.Reactions) == 1 {
				return true
			}
		}
		return false
	})
	for _, item := range sink.items() {
		if item.ID == id {
			if len(item.Reactions) != 1 || item.Reactions[0] != (core.Reaction{Sender: sender.String(), Emoji: "👍"}) {
				t.Fatalf("Reactions = %+v, want one %s/👍 reaction", item.Reactions, sender.String())
			}
		}
	}

	// An empty-text reaction from the same sender removes it.
	cli.emit(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: sender}, ID: "M5", Timestamp: time.Now()},
		Message: &waE2E.Message{
			ReactionMessage: &waE2E.ReactionMessage{
				Key:  &waCommon.MessageKey{ID: strPtr("M1")},
				Text: strPtr(""),
			},
		},
	})
	waitFor(t, func() bool {
		for _, item := range sink.items() {
			if item.ID == id {
				return len(item.Reactions) == 0
			}
		}
		return false
	})
}
