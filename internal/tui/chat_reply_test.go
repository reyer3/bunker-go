package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

func replyingClient() *replyClient {
	return &replyClient{previewOut: core.Plan{Recipients: []string{"alice"}}, sendRcpt: core.Receipt{ID: "whatsapp:personal:9"}}
}

// sendTyped types text in the open chat and sends it with the one-Enter
// flow (dry-run, then the real send), returning the model after the
// receipt.
func sendTyped(t *testing.T, model Model, text string) Model {
	t.Helper()
	model = typeRunes(model, text)
	model, previewCmd := uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if previewCmd == nil {
		t.Fatal("Enter returned no preview command")
	}
	updated, sendCmd := model.Update(previewCmd())
	model = updated.(Model)
	if sendCmd == nil {
		t.Fatalf("a clean plan did not send; chatConfirm=%v err=%v", model.chatConfirm, model.chatSendErr)
	}
	updated, _ = model.Update(sendCmd())
	return updated.(Model)
}

// A message typed in a chat is a plain message: it quotes nothing unless
// the user picked a message to answer.
func TestChatSendIsAPlainMessageNotAReply(t *testing.T) {
	client := replyingClient()
	model, _ := uxChat(t, client)

	sendTyped(t, model, "hola")

	if len(client.outgoingCalls) != 2 {
		t.Fatalf("Send calls = %d, want a dry-run and the real send", len(client.outgoingCalls))
	}
	for i, call := range client.outgoingCalls {
		out := call.out
		if out.ReplyTo != "" || out.Forward || out.Body != "hola" ||
			out.Thread != "5511999999999@s.whatsapp.net" || len(out.To) != 1 || out.To[0] != out.Thread {
			t.Errorf("call %d = %+v, want a plain message to the chat's own thread", i, out)
		}
	}
	for _, call := range client.calls {
		if call.id != "" {
			t.Errorf("a chat send quoted %q without being asked to", call.id)
		}
	}
}

func TestAltRRepliesToTheSelectedMessage(t *testing.T) {
	client := replyingClient()
	model, _ := uxChat(t, client)
	model, _ = uxPress(t, model, altUp)
	model, _ = uxPress(t, model, altUp) // "b", not the newest
	model, cmd := uxPress(t, model, keyAlt('r'))
	if cmd != nil {
		t.Fatal("Alt+R sent a command")
	}
	if model.chatReplyTo == nil || model.chatReplyTo.ID != "whatsapp:personal:b" {
		t.Fatalf("chatReplyTo = %+v, want the selected message", model.chatReplyTo)
	}
	view := model.View()
	if !strings.Contains(view, "Respondiendo a Alice") || !strings.Contains(view, "mira el informe") {
		t.Fatalf("no reply indicator naming who and what:\n%s", view)
	}

	model = sendTyped(t, model, "ok")

	if len(client.outgoingCalls) != 0 {
		t.Errorf("a reply went out as a fresh Send: %+v", client.outgoingCalls)
	}
	if len(client.calls) != 2 || client.calls[0].id != "whatsapp:personal:b" || client.calls[1].id != "whatsapp:personal:b" ||
		!client.calls[0].dryRun || client.calls[1].dryRun || client.calls[1].body != "ok" {
		t.Errorf("calls = %+v, want a dry-run then the real reply to b", client.calls)
	}
	if model.chatReplyTo != nil || strings.Contains(model.View(), "Respondiendo a") {
		t.Error("the reply target outlived its send")
	}

	// The next message goes back to being plain.
	model = sendTyped(t, model, "otra cosa")
	if got := len(client.outgoingCalls); got != 2 {
		t.Errorf("Send calls after the reply = %d, want the plain message's dry-run and send", got)
	}
}

func TestAltRWithoutSelectionRepliesToTheNewest(t *testing.T) {
	model, _ := uxChat(t, replyingClient())
	model, _ = uxPress(t, model, keyAlt('r'))
	if model.chatReplyTo == nil || model.chatReplyTo.ID != "whatsapp:personal:c" {
		t.Fatalf("chatReplyTo = %+v, want the newest message", model.chatReplyTo)
	}
	if !strings.Contains(model.View(), "Respondiendo a tu mensaje") {
		t.Fatalf("an own message should read \"tu mensaje\":\n%s", model.View())
	}
}

func TestEscCancelsTheReplyBeforeAnythingElse(t *testing.T) {
	client := replyingClient()
	model, _ := uxChat(t, client)
	model, _ = uxPress(t, model, altUp)
	model, _ = uxPress(t, model, keyAlt('r'))
	model, _ = uxPress(t, model, escKey)
	if model.chatReplyTo != nil || !model.chatMode {
		t.Fatalf("chatReplyTo=%v chatMode=%v, want the reply dropped and the chat kept", model.chatReplyTo, model.chatMode)
	}
	sendTyped(t, model, "hola")
	if len(client.calls) != 0 {
		t.Errorf("a cancelled reply was still sent as one: %+v", client.calls)
	}
}

func TestAltRRefusesAMessageThatCannotBeAnswered(t *testing.T) {
	model, _ := uxChat(t, replyingClient())
	for i := range model.chatItems {
		if model.chatItems[i].ID == "whatsapp:personal:b" {
			model.chatItems[i].Deleted = true
		}
	}
	model.chatFocus = "whatsapp:personal:b"
	model, _ = uxPress(t, model, keyAlt('r'))
	if model.chatReplyTo != nil {
		t.Fatalf("chatReplyTo = %+v, want none for a deleted message", model.chatReplyTo)
	}
	if model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "eliminado") {
		t.Fatalf("error = %v, want it to say the message was deleted", model.mediaErr)
	}
}

func TestLeavingTheChatDropsTheReplyTarget(t *testing.T) {
	model, _ := uxChat(t, replyingClient())
	model, _ = uxPress(t, model, keyAlt('r'))
	model, _ = model.leaveChat()
	if model.chatReplyTo != nil {
		t.Fatalf("chatReplyTo = %+v after leaving the chat", model.chatReplyTo)
	}
}

// A reply that fails keeps both its text and its target, so retrying
// still answers the same message.
func TestFailedReplyKeepsItsTarget(t *testing.T) {
	client := replyingClient()
	client.sendErr = errors.New("sin red")
	model, _ := uxChat(t, client)
	model, _ = uxPress(t, model, altUp)
	model, _ = uxPress(t, model, keyAlt('r'))
	model = sendTyped(t, model, "ok")
	if model.chatReplyTo == nil || model.chatReplyTo.ID != "whatsapp:personal:c" {
		t.Fatalf("chatReplyTo = %+v, want the target restored after the failure", model.chatReplyTo)
	}
	if model.composer.Value() != "ok" {
		t.Errorf("composer = %q, want the unsent text back", model.composer.Value())
	}
}
