package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// unreadClient is a markClient that can also mark unread.
type unreadClient struct {
	markClient
	unread []string
	local  bool
}

func (c *unreadClient) MarkUnread(_ context.Context, id string) (bool, error) {
	c.unread = append(c.unread, id)
	return c.local, nil
}

func TestUndoMarkRead(t *testing.T) {
	client := &unreadClient{markClient: markClient{previewOut: core.Plan{Channel: core.ChannelWhatsApp, Account: "wa"}}, local: true}
	model := readyModel(client, "mail:a:1")

	// u with nothing to undo says so.
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	if cmd != nil || !strings.Contains(updated.(Model).View(), "no hay nada que deshacer") {
		t.Fatalf("empty undo:\n%s", updated.(Model).View())
	}

	// m still previews and confirms (read receipts are outbound)...
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	updated, _ = updated.(Model).Update(cmd())
	updated, cmd = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)
	if client.realCalls() != 1 || !strings.Contains(model.View(), "u deshacer") {
		t.Fatalf("after confirm: real calls %d, view:\n%s", client.realCalls(), model.View())
	}

	// ...and u undoes it, saying when it only changed bunker.
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	if cmd == nil {
		t.Fatal("u should mark the last read item unread")
	}
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)
	if len(client.unread) != 1 || client.unread[0] != "mail:a:1" {
		t.Fatalf("MarkUnread calls = %v", client.unread)
	}
	if view := model.View(); !strings.Contains(view, "vuelve a estar sin leer") || !strings.Contains(view, "solo en bunker") {
		t.Fatalf("undo notice:\n%s", view)
	}
	if len(model.readUndo) != 0 {
		t.Fatal("the undone mark should leave the stack")
	}
}

func TestOpeningAChatCanBeUndone(t *testing.T) {
	client := &unreadClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd().(chatThreadLoadedMsg))
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	_, cmd = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	if cmd == nil {
		t.Fatal("u after reading a chat should put it back to unread")
	}
	cmd()
	if len(client.unread) != 1 || client.unread[0] != "whatsapp:personal:1" {
		t.Fatalf("MarkUnread calls = %v", client.unread)
	}
}

func TestChatDraftSurvivesLeaving(t *testing.T) {
	model, _ := openedChat(t)
	model = typeIntoChat(t, model, "a medio escribir")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if !strings.Contains(model.View(), "borrador guardado") {
		t.Fatalf("leaving with a draft should say it was kept:\n%s", model.View())
	}
	model, cmd := openChat(model)
	updated, _ = model.Update(cmd().(chatThreadLoadedMsg))
	if got := updated.(Model).composer.Value(); got != "a medio escribir" {
		t.Fatalf("reopened chat draft = %q", got)
	}
}

func TestMailEditorDraftSurvivesEsc(t *testing.T) {
	model, _ := pickerModel(t)
	model = typePicker(t, model, "carla")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	model.mailSubject.SetValue("Reunión")
	model.composer.SetValue("hola Carla")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.mailComposing {
		t.Fatal("Esc should close the editor")
	}
	restored := model.openNewMail(core.Contact{Channel: core.ChannelMail, Account: "cl", Name: "Carla", Address: "carla@example.cl"})
	if restored.mailSubject.Value() != "Reunión" || restored.composer.Value() != "hola Carla" {
		t.Fatalf("restored subject %q body %q", restored.mailSubject.Value(), restored.composer.Value())
	}
}
