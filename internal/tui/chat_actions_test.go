package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

type actionCall struct {
	kind, id, text string
	dryRun         bool
}

// actionClient is replyClient plus MessageClient, recording every edit,
// delete and react call.
type actionClient struct {
	replyClient
	actions []actionCall
}

func (c *actionClient) record(kind, id, text string, dryRun bool) (core.Plan, core.Receipt, error) {
	c.actions = append(c.actions, actionCall{kind, id, text, dryRun})
	plan := core.Plan{Action: kind, Target: id, Preview: text}
	if dryRun {
		return plan, core.Receipt{}, nil
	}
	return plan, core.Receipt{ID: "R-" + kind}, nil
}

func (c *actionClient) EditMessage(_ context.Context, id, text string, dryRun bool) (core.Plan, core.Receipt, error) {
	return c.record("edit", id, text, dryRun)
}

func (c *actionClient) DeleteMessage(_ context.Context, id string, dryRun bool) (core.Plan, core.Receipt, error) {
	return c.record("delete", id, "", dryRun)
}

func (c *actionClient) React(_ context.Context, id, emoji string, dryRun bool) (core.Plan, core.Receipt, error) {
	return c.record("react", id, emoji, dryRun)
}

// actionChat opens a chat whose last message is ours ("mio 2"), after
// one from the other side ("suyo").
func actionChat(t *testing.T) (Model, *actionClient) {
	t.Helper()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	client := &actionClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.now = func() time.Time { return now }
	model.width, model.height = 100, 30
	model, cmd := openChat(model)
	msg := cmd().(chatThreadLoadedMsg)
	msg.items = []core.Item{
		{ID: "whatsapp:personal:c/1", Channel: core.ChannelWhatsApp, Account: "personal", FromMe: true, Body: "mio 1", Timestamp: now.Add(-3 * time.Minute)},
		{ID: "whatsapp:personal:c/2", Channel: core.ChannelWhatsApp, Account: "personal", From: core.Address{Name: "Alice"}, Body: "suyo", Timestamp: now.Add(-2 * time.Minute)},
		{ID: "whatsapp:personal:c/3", Channel: core.ChannelWhatsApp, Account: "personal", FromMe: true, Body: "mio 2", Timestamp: now.Add(-time.Minute)},
	}
	updated, _ := model.Update(msg)
	return updated.(Model), client
}

// press sends key and feeds the command's message (one level) back.
func pressAction(t *testing.T, model Model, key tea.KeyMsg) Model {
	t.Helper()
	updated, cmd := model.Update(key)
	model = updated.(Model)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, batch := msg.(tea.BatchMsg); !batch {
				updated, _ = model.Update(msg)
				model = updated.(Model)
			}
		}
	}
	return model
}

func altKey(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true} }

func digitKey(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

func TestChatAltEEditsLastOwnMessageWithPreviewAndConfirm(t *testing.T) {
	model, client := actionChat(t)
	model = typeRunes(model, "borrador")

	model = pressAction(t, model, altKey('e'))
	if model.chatEditID != "whatsapp:personal:c/3" || model.composer.Value() != "mio 2" {
		t.Fatalf("edit mode: id %q, composer %q", model.chatEditID, model.composer.Value())
	}
	if !strings.Contains(model.View(), "Editando tu último mensaje") {
		t.Error("the tail does not say an edit is in progress")
	}
	model = typeRunes(model, "!")
	model = pressAction(t, model, tea.KeyMsg{Type: tea.KeyCtrlS})
	if len(client.actions) != 1 || client.actions[0] != (actionCall{"edit", "whatsapp:personal:c/3", "mio 2!", true}) {
		t.Fatalf("after Ctrl+S: %+v, want one dry-run edit", client.actions)
	}
	if !strings.Contains(model.View(), "¿Guardar la edición «mio 2!»?") {
		t.Fatalf("no confirm on screen:\n%s", model.View())
	}
	model = pressAction(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if len(client.actions) != 2 || client.actions[1].dryRun {
		t.Fatalf("after confirm: %+v, want the real edit", client.actions)
	}
	// The real call's result restores the set-aside draft.
	if model.chatEditID != "" || model.composer.Value() != "borrador" || model.chatAction != nil {
		t.Errorf("after the edit: id %q, composer %q, action %+v", model.chatEditID, model.composer.Value(), model.chatAction)
	}
}

func TestChatEditEscRestoresDraftAndSendsNothing(t *testing.T) {
	model, client := actionChat(t)
	model = typeRunes(model, "hola")
	model = pressAction(t, model, altKey('e'))
	model = pressAction(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.chatEditID != "" || model.composer.Value() != "hola" || !model.chatMode {
		t.Errorf("after Esc: id %q, composer %q, chatMode %v", model.chatEditID, model.composer.Value(), model.chatMode)
	}
	if len(client.actions) != 0 {
		t.Errorf("calls = %+v", client.actions)
	}
}

func TestChatAltXDeletesOnlyAfterConfirm(t *testing.T) {
	model, client := actionChat(t)
	model = pressAction(t, model, altKey('x'))
	if len(client.actions) != 1 || client.actions[0] != (actionCall{"delete", "whatsapp:personal:c/3", "", true}) {
		t.Fatalf("Alt+X: %+v, want a dry-run delete of our last message", client.actions)
	}
	if !strings.Contains(model.View(), "¿Eliminar para todos «mio 2»?") {
		t.Fatalf("no delete confirm:\n%s", model.View())
	}
	// Plain letters must not reach the draft (nor confirm) behind the prompt.
	model = pressAction(t, model, digitKey('s'))
	if model.composer.Value() != "" || len(client.actions) != 1 {
		t.Fatalf("a letter leaked: composer %q, calls %+v", model.composer.Value(), client.actions)
	}
	model = pressAction(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.chatAction != nil || len(client.actions) != 1 || !model.chatMode {
		t.Fatalf("Esc should cancel and stay in the chat: %+v", client.actions)
	}

	model = pressAction(t, model, altKey('x'))
	model = pressAction(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if len(client.actions) != 3 || client.actions[2].dryRun || client.actions[2].kind != "delete" {
		t.Fatalf("confirmed delete: %+v", client.actions)
	}
	if flash, ok := model.currentFlash(); !ok || !strings.Contains(flash, "eliminado") {
		t.Errorf("flash = %q, %v", flash, ok)
	}
}

func TestChatAltPlusReactsToLastReceivedMessage(t *testing.T) {
	model, client := actionChat(t)
	model = pressAction(t, model, altKey('+'))
	view := model.View()
	if !strings.Contains(view, "Reaccionar a «suyo»") || !strings.Contains(view, "1 👍") {
		t.Fatalf("no picker:\n%s", view)
	}
	model = pressAction(t, model, digitKey('2'))
	if len(client.actions) != 1 || client.actions[0] != (actionCall{"react", "whatsapp:personal:c/2", "❤️", true}) {
		t.Fatalf("picked 2: %+v", client.actions)
	}
	model = pressAction(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if len(client.actions) != 2 || client.actions[1].dryRun {
		t.Fatalf("confirmed reaction: %+v", client.actions)
	}

	// Alt+= is the same key without Shift; 0 removes ours.
	model = pressAction(t, model, altKey('='))
	model = pressAction(t, model, digitKey('0'))
	if last := client.actions[len(client.actions)-1]; last != (actionCall{"react", "whatsapp:personal:c/2", "", true}) {
		t.Errorf("remove: %+v", last)
	}
	if !strings.Contains(model.View(), "¿Quitar tu reacción de «suyo»?") {
		t.Errorf("no removal confirm:\n%s", model.View())
	}
}

func TestChatActionsWithoutCapabilityFailLoudly(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.now = func() time.Time { return now }
	model, cmd := openChat(model)
	msg := cmd().(chatThreadLoadedMsg)
	msg.items = []core.Item{{ID: "whatsapp:personal:c/1", Channel: core.ChannelWhatsApp, Account: "personal", FromMe: true, Body: "mio", Timestamp: now}}
	updated, _ := model.Update(msg)
	model = pressAction(t, updated.(Model), altKey('x'))
	if model.chatAction != nil || model.chatSendErr != errNoMessageActions {
		t.Errorf("action %+v, err %v; want errNoMessageActions", model.chatAction, model.chatSendErr)
	}
}

func TestChatEditWithNoOwnMessageSaysSo(t *testing.T) {
	model, _ := actionChat(t)
	model.chatItems = model.chatItems[1:2] // only theirs
	model = pressAction(t, model, altKey('e'))
	if model.chatEditID != "" || model.chatSendErr == nil || !strings.Contains(model.chatSendErr.Error(), "no hay un mensaje tuyo") {
		t.Errorf("edit id %q, err %v", model.chatEditID, model.chatSendErr)
	}
}
