package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// contactsClient is a replyClient with an address book.
type contactsClient struct {
	replyClient
	book    []core.Contact
	queries []string
	// channels records the channel scope of every request.
	channels []core.Channel
}

func (c *contactsClient) Contacts(_ context.Context, filter core.ContactFilter) ([]core.Contact, error) {
	c.queries = append(c.queries, filter.Query)
	c.channels = append(c.channels, filter.Channel)
	var out []core.Contact
	for _, ct := range c.book {
		if strings.Contains(strings.ToLower(ct.Name), strings.ToLower(filter.Query)) {
			out = append(out, ct)
		}
	}
	return out, nil
}

func pickerModel(t *testing.T) (Model, *contactsClient) {
	t.Helper()
	client := &contactsClient{book: []core.Contact{
		{Channel: core.ChannelWhatsApp, Account: "personal", Name: "Ana Díaz", Address: "51922@s.whatsapp.net"},
		{Channel: core.ChannelWhatsApp, Account: "personal", Name: "Bruno", Address: "51933@s.whatsapp.net", Thread: "51933@s.whatsapp.net"},
		{Channel: core.ChannelMail, Account: "cl", Name: "Carla", Address: "carla@example.cl"},
	}}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.width = 60
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	model = updated.(Model)
	if model.picker == nil || cmd == nil {
		t.Fatal("n should open the contact picker and load contacts")
	}
	updated, _ = model.Update(cmd())
	return updated.(Model), client
}

// typePicker types text into the picker, applying each reload.
func typePicker(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
		if cmd != nil {
			updated, _ = m.Update(cmd())
			m = updated.(Model)
		}
	}
	return m
}

func TestPickerFiltersAndStartsChat(t *testing.T) {
	model, client := pickerModel(t)
	view := model.View()
	if !strings.Contains(view, "Nuevo mensaje") || !strings.Contains(view, "Ana Díaz") || !strings.Contains(view, "Carla") {
		t.Fatalf("picker view:\n%s", view)
	}
	model = typePicker(t, model, "ana")
	if got := client.queries[len(client.queries)-1]; got != "ana" {
		t.Fatalf("last query = %q", got)
	}
	if view := model.View(); strings.Contains(view, "Bruno") || !strings.Contains(view, "Ana Díaz") {
		t.Fatalf("filtered view:\n%s", view)
	}

	updated, openCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.picker != nil || !model.chatMode || model.chatThread != "51922@s.whatsapp.net" || model.chatName == "" {
		t.Fatalf("chat not opened on the contact: mode=%v thread=%q name=%q", model.chatMode, model.chatThread, model.chatName)
	}
	updated, _ = model.Update(openCmd())
	model = updated.(Model)

	// A first message goes through Send (there is no item to reply to),
	// previewed before it is confirmed.
	model = typeIntoChat(t, model, "hola")
	updated, previewCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(previewCmd())
	model = updated.(Model)
	if len(client.outgoingCalls) != 1 || !client.outgoingCalls[0].dryRun || len(client.calls) != 0 {
		t.Fatalf("preview: outgoing %+v, replies %+v; want one dry-run Send", client.outgoingCalls, client.calls)
	}
	out := client.outgoingCalls[0].out
	if out.To[0] != "51922@s.whatsapp.net" || out.Thread != "51922@s.whatsapp.net" || out.Body != "hola" || out.Channel != core.ChannelWhatsApp {
		t.Fatalf("outgoing = %+v", out)
	}
	if !model.chatConfirm {
		t.Fatal("the send should wait for a confirm")
	}
	_, sendCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	for _, msg := range runCmds(sendCmd) {
		_ = msg
	}
	if len(client.outgoingCalls) != 2 || client.outgoingCalls[1].dryRun {
		t.Fatalf("confirm should send for real: %+v", client.outgoingCalls)
	}
}

func TestPickerMailOpensEditor(t *testing.T) {
	model, _ := pickerModel(t)
	model = typePicker(t, model, "carla")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if !model.mailComposing || model.mailTo.Value() != "carla@example.cl" || model.mailTargetID != "" || model.mailFocus != 2 {
		t.Fatalf("mail editor: composing=%v to=%q target=%q focus=%d", model.mailComposing, model.mailTo.Value(), model.mailTargetID, model.mailFocus)
	}
}

func TestPickerKeysAndMouse(t *testing.T) {
	model, _ := pickerModel(t)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	if model.picker.selected != 1 {
		t.Fatalf("down moved to %d", model.picker.selected)
	}
	// Clicking the third result opens it.
	updated, _ = model.Update(tea.MouseMsg{X: 3, Y: pickerHeaderLines + 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m := updated.(Model); !m.mailComposing || m.mailTo.Value() != "carla@example.cl" {
		t.Fatal("a click on a result should open it")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(Model).picker != nil {
		t.Fatal("Esc should close the picker")
	}
}

func TestPickerWithoutContactsSupport(t *testing.T) {
	model := chatReadyModel(&replyClient{}, "whatsapp:personal:1")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	updated, _ = updated.(Model).Update(cmd())
	if !strings.Contains(updated.(Model).View(), "no lista contactos") {
		t.Fatalf("a client without contacts should say so:\n%s", updated.(Model).View())
	}
}
