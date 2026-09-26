package whatsapp

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/reyer3/bunker-go/internal/core"
)

func mustJID(t *testing.T, s string) types.JID {
	t.Helper()
	jid, err := types.ParseJID(s)
	if err != nil {
		t.Fatalf("ParseJID(%q): %v", s, err)
	}
	return jid
}

func TestToItemPlainConversation(t *testing.T) {
	chat := mustJID(t, "1234@s.whatsapp.net")
	ts := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "3EB0ABCDEF",
			PushName:      "Alice",
			Timestamp:     ts,
		},
		Message: &waE2E.Message{Conversation: strPtr("hola bunker")},
	}

	item := toItem("personal", evt)

	if want := "whatsapp:personal:1234@s.whatsapp.net/3EB0ABCDEF"; item.ID != want {
		t.Errorf("ID = %q, want %q", item.ID, want)
	}
	if item.Channel != core.ChannelWhatsApp {
		t.Errorf("Channel = %v, want %v", item.Channel, core.ChannelWhatsApp)
	}
	if item.Thread != "1234@s.whatsapp.net" {
		t.Errorf("Thread = %q", item.Thread)
	}
	if item.ThreadName != "Alice" {
		t.Errorf("ThreadName = %q, want %q", item.ThreadName, "Alice")
	}
	if item.Body != "hola bunker" {
		t.Errorf("Body = %q, want %q", item.Body, "hola bunker")
	}
	if !item.Unread {
		t.Errorf("Unread = false, want true for an incoming message")
	}
	if !item.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", item.Timestamp, ts)
	}
	if len(item.Attachments) != 0 {
		t.Errorf("Attachments = %v, want none", item.Attachments)
	}
}

func TestToItemExtendedText(t *testing.T) {
	chat := mustJID(t, "1234@s.whatsapp.net")
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "MSG2",
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: strPtr("con formato")},
		},
	}
	item := toItem("personal", evt)
	if item.Body != "con formato" {
		t.Errorf("Body = %q, want %q", item.Body, "con formato")
	}
}

func TestToItemFromMeIsRead(t *testing.T) {
	chat := mustJID(t, "1234@s.whatsapp.net")
	me := mustJID(t, "5551@s.whatsapp.net")
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: me, IsFromMe: true},
			ID:            "MSG3",
		},
		Message: &waE2E.Message{Conversation: strPtr("enviado por mi")},
	}
	item := toItem("personal", evt)
	if item.Unread {
		t.Errorf("Unread = true, want false for a message the account itself sent")
	}
}

func TestToItemImageWithCaptionAndQuote(t *testing.T) {
	chat := mustJID(t, "1234@s.whatsapp.net")
	quotedChat := mustJID(t, "1234@s.whatsapp.net")
	_ = quotedChat
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "MSG4",
		},
		Message: &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				Caption:    strPtr("mira esto"),
				Mimetype:   strPtr("image/jpeg"),
				FileLength: uint64Ptr(2048),
				DirectPath: strPtr("/v/t62.7118-24/abc"),
				ContextInfo: &waE2E.ContextInfo{
					StanzaID:    strPtr("QUOTED123"),
					Participant: strPtr("9999@s.whatsapp.net"),
				},
			},
		},
	}
	item := toItem("personal", evt)
	if item.Body != "mira esto" {
		t.Errorf("Body = %q, want caption %q", item.Body, "mira esto")
	}
	if len(item.Attachments) != 1 {
		t.Fatalf("Attachments = %v, want exactly one", item.Attachments)
	}
	att := item.Attachments[0]
	if att.MIME != "image/jpeg" || att.Size != 2048 || att.Ref != "/v/t62.7118-24/abc" {
		t.Errorf("Attachment = %+v, unexpected", att)
	}
	if item.Meta["wa_quoted_stanza_id"] != "QUOTED123" {
		t.Errorf("Meta[wa_quoted_stanza_id] = %q, want QUOTED123", item.Meta["wa_quoted_stanza_id"])
	}
	if item.Meta["wa_quoted_participant"] != "9999@s.whatsapp.net" {
		t.Errorf("Meta[wa_quoted_participant] = %q", item.Meta["wa_quoted_participant"])
	}
}

func strPtr(s string) *string    { return &s }
func uint64Ptr(v uint64) *uint64 { return &v }

// quotedMessageEvent builds a plain incoming text message event, used by
// send_test.go to seed the adapter's cache before testing a reply.
func quotedMessageEvent(chat, sender types.JID, msgID, body string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender},
			ID:            types.MessageID(msgID),
			Timestamp:     time.Now(),
		},
		Message: &waE2E.Message{Conversation: strPtr(body)},
	}
}
