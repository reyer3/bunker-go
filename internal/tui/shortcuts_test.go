package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// drain runs cmd and feeds every message it produces (a batch's too) back
// into the model, once, the way the Bubble Tea runtime would.
func drain(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = drain(t, m, c)
		}
		return m
	}
	if msg == nil {
		return m
	}
	updated, _ := m.Update(msg)
	return updated.(Model)
}

func pressDrain(t *testing.T, m Model, key tea.KeyMsg) Model {
	t.Helper()
	updated, cmd := m.Update(key)
	return drain(t, updated.(Model), cmd)
}

func atKey() tea.KeyMsg { return digitKey('@') }

// searchClient is a call-capable client with an address book.
type searchClient struct {
	callClient
	book     []core.Contact
	channels []core.Channel
}

func (c *searchClient) Contacts(_ context.Context, filter core.ContactFilter) ([]core.Contact, error) {
	c.channels = append(c.channels, filter.Channel)
	var out []core.Contact
	for _, ct := range c.book {
		if filter.Channel == "" || ct.Channel == filter.Channel {
			out = append(out, ct)
		}
	}
	return out, nil
}

func TestAtOpensPickerScopedToTheFocusedChatTab(t *testing.T) {
	cases := []struct {
		tab     int
		scope   core.Channel
		heading string
	}{
		{0, "", "Buscar contacto"},
		{1, "", "Buscar contacto"},
		{2, core.ChannelWhatsApp, "Buscar contacto en WhatsApp"},
		{3, core.ChannelMatrix, "Buscar contacto en Matrix"},
	}
	for _, tc := range cases {
		client := &contactsClient{}
		m := chatReadyModel(client, "whatsapp:personal:1").switchTab(tc.tab)
		updated, cmd := m.Update(atKey())
		m = updated.(Model)
		if m.picker == nil || cmd == nil {
			t.Fatalf("tab %d: @ should open the picker and load", tc.tab)
		}
		m = drain(t, m, cmd)
		if got := client.channels[len(client.channels)-1]; got != tc.scope {
			t.Errorf("tab %d: contacts scope %q, want %q", tc.tab, got, tc.scope)
		}
		if first := strings.Split(m.View(), "\n")[0]; first != tc.heading {
			t.Errorf("tab %d: title %q, want %q", tc.tab, first, tc.heading)
		}
		// Typing keeps the scope.
		m = typePicker(t, m, "a")
		if got := client.channels[len(client.channels)-1]; got != tc.scope {
			t.Errorf("tab %d: scope lost while typing: %q", tc.tab, got)
		}
	}
}

func TestNKeepsAllChannelsEvenOnTheWhatsAppTab(t *testing.T) {
	client := &contactsClient{}
	m := chatReadyModel(client, "whatsapp:personal:1").switchTab(2)
	updated, cmd := m.Update(digitKey('n'))
	drain(t, updated.(Model), cmd)
	if client.channels[0] != "" || !strings.HasPrefix(updated.(Model).View(), "Nuevo mensaje") {
		t.Errorf("n scope %q, view %q", client.channels[0], updated.(Model).View())
	}
}

func TestAtWorksInTheSidebarAndIsIgnoredElsewhere(t *testing.T) {
	m := sidebarModel().switchTab(2)
	m, _ = press(t, m, atKey())
	if m.picker == nil || m.picker.channel != core.ChannelWhatsApp {
		t.Fatalf("sidebar @: picker %+v", m.picker)
	}

	// In a chat the composer takes it as text.
	chat := callChat(t, &contactsClient{})
	chat = typeRunes(chat, "@")
	if chat.picker != nil || chat.composer.Value() != "@" {
		t.Errorf("chat: picker %v, composer %q", chat.picker, chat.composer.Value())
	}
	// While filtering and replying it is text too.
	f := chatReadyModel(&contactsClient{}, "whatsapp:personal:1")
	f, _ = press(t, f, digitKey('/'))
	f, _ = press(t, f, atKey())
	if f.picker != nil || f.filterQuery != "@" {
		t.Errorf("filter: picker %v, query %q", f.picker, f.filterQuery)
	}
	r := chatReadyModel(&contactsClient{}, "whatsapp:personal:1")
	r, _ = press(t, r, digitKey('r'))
	r, _ = press(t, r, atKey())
	if r.picker != nil || r.composer.Value() != "@" {
		t.Errorf("reply: picker %v, composer %q", r.picker, r.composer.Value())
	}
	// The palette takes it as its contacts prefix, not the picker.
	p := chatReadyModel(&contactsClient{}, "whatsapp:personal:1")
	p, _ = press(t, p, ctrlKKey)
	p, _ = press(t, p, atKey())
	if p.picker != nil || p.palette == nil || p.palette.query != "@" {
		t.Errorf("palette: picker %v", p.picker)
	}
}

func TestCStartsACallPreviewFromAWhatsAppRow(t *testing.T) {
	client := &callClient{}
	m := chatReadyModel(client, "whatsapp:personal:1")
	m.width = 100
	m = pressDrain(t, m, digitKey('c'))
	if !m.chatMode || m.chatAction == nil || m.chatAction.kind != "call" || !m.chatAction.confirm {
		t.Fatalf("chatMode %v, action %+v; want the call preview pending a confirm", m.chatMode, m.chatAction)
	}
	want := placeRecord{core.ChannelWhatsApp, "personal", "5511999999999@s.whatsapp.net", true}
	if len(client.placed) != 1 || client.placed[0] != want {
		t.Fatalf("placed %+v, want one dry-run %+v", client.placed, want)
	}
	if !strings.Contains(m.View(), "¿Llamar a «Alice»? ↵ llamar · Esc cancelar") {
		t.Errorf("no confirm on screen:\n%s", m.View())
	}
	m = pressDrain(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(client.placed) != 2 || client.placed[1].dryRun {
		t.Fatalf("placed %+v, want the real call after ↵", client.placed)
	}
	if line := lastLine(m.View()); !strings.Contains(line, "Llamando a Alice") {
		t.Errorf("banner = %q", line)
	}
}

func TestCEscCancelsWithoutPlacing(t *testing.T) {
	client := &callClient{}
	m := pressDrain(t, chatReadyModel(client, "whatsapp:personal:1"), digitKey('c'))
	m = pressDrain(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.chatAction != nil || len(client.placed) != 1 || !client.placed[0].dryRun {
		t.Errorf("action %+v, placed %+v", m.chatAction, client.placed)
	}
}

func TestCOnRowsThatCannotBeCalledFlashesWhy(t *testing.T) {
	flashOf := func(m Model) string {
		f, _ := m.currentFlash()
		return f
	}
	t.Run("group", func(t *testing.T) {
		client := &callClient{}
		m := chatReadyModel(client, "whatsapp:personal:1")
		m.groups[0].items[0].Thread = "120363000000000000@g.us"
		m = pressDrain(t, m, digitKey('c'))
		if m.chatMode || !strings.Contains(flashOf(m), "no se puede llamar a un grupo") || len(client.placed) != 0 {
			t.Errorf("chatMode %v, flash %q, placed %+v", m.chatMode, flashOf(m), client.placed)
		}
	})
	t.Run("mail", func(t *testing.T) {
		m := callInbox(&callClient{})
		m = pressDrain(t, m, digitKey('c'))
		if m.chatMode || !strings.Contains(flashOf(m), "solo están disponibles en WhatsApp") {
			t.Errorf("flash %q", flashOf(m))
		}
	})
	t.Run("matrix", func(t *testing.T) {
		m := chatReadyModel(&callClient{}, "matrix:hs:1")
		m.groups[0].items[0].Channel = core.ChannelMatrix
		m = pressDrain(t, m, digitKey('c'))
		if m.chatMode || !strings.Contains(flashOf(m), "solo están disponibles en WhatsApp") {
			t.Errorf("flash %q", flashOf(m))
		}
	})
	t.Run("connection without calls", func(t *testing.T) {
		m := pressDrain(t, chatReadyModel(&replyClient{}, "whatsapp:personal:1"), digitKey('c'))
		if m.chatMode || !strings.Contains(flashOf(m), "no permite llamadas") {
			t.Errorf("flash %q", flashOf(m))
		}
	})
	t.Run("call already live", func(t *testing.T) {
		client := &callClient{}
		m, _ := deliverCalls(chatReadyModel(client, "whatsapp:personal:1"), core.Call{ID: "x", State: core.CallStateActive, Peer: "1"})
		m = pressDrain(t, m, digitKey('c'))
		if m.chatMode || !strings.Contains(flashOf(m), "ya hay una llamada en curso") || len(client.placed) != 0 {
			t.Errorf("flash %q, placed %+v", flashOf(m), client.placed)
		}
	})
	t.Run("account without calls enabled", func(t *testing.T) {
		client := &callClient{placeErr: fmt.Errorf("whatsapp: calls are off: %w", core.ErrUnsupported)}
		m := pressDrain(t, chatReadyModel(client, "whatsapp:personal:1"), digitKey('c'))
		if !m.chatMode || m.chatSendErr == nil || !strings.Contains(m.View(), "calls = true") {
			t.Errorf("chatMode %v, err %v:\n%s", m.chatMode, m.chatSendErr, m.View())
		}
	})
	t.Run("sidebar hands conversations to another pane", func(t *testing.T) {
		opened := false
		m := sidebarModel(WithExternalOpener(func(string) error { opened = true; return nil }))
		m.client = &callClient{}
		m = pressDrain(t, m, digitKey('c'))
		if opened || m.chatMode || !strings.Contains(flashOf(m), "Alt+C") {
			t.Errorf("opened %v, flash %q", opened, flashOf(m))
		}
	})
}

func TestCInChatIsDraftText(t *testing.T) {
	client := &callClient{}
	m := callChat(t, client)
	m = typeRunes(m, "c")
	if m.composer.Value() != "c" || len(client.placed) != 0 {
		t.Errorf("composer %q, placed %+v", m.composer.Value(), client.placed)
	}
}

func TestPickerAltCCallsTheHighlightedWhatsAppContact(t *testing.T) {
	client := &searchClient{book: []core.Contact{
		{Channel: core.ChannelWhatsApp, Account: "personal", Name: "Ana Díaz", Address: "51922@s.whatsapp.net"},
		{Channel: core.ChannelMail, Account: "cl", Name: "Carla", Address: "carla@example.cl"},
	}}
	m := chatReadyModel(client, "whatsapp:personal:1")
	m.width = 100
	m = pressDrain(t, m, digitKey('n'))

	m = pressDrain(t, m, altKey('c'))
	if m.picker != nil || !m.chatMode || m.chatName != "Ana Díaz" || m.chatAction == nil || !m.chatAction.confirm {
		t.Fatalf("picker %v chatMode %v action %+v", m.picker, m.chatMode, m.chatAction)
	}
	want := placeRecord{core.ChannelWhatsApp, "personal", "51922@s.whatsapp.net", true}
	if len(client.placed) != 1 || client.placed[0] != want {
		t.Fatalf("placed %+v, want one dry-run %+v", client.placed, want)
	}
	m = pressDrain(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(client.placed) != 2 || client.placed[1].dryRun {
		t.Fatalf("placed %+v", client.placed)
	}

	// A mail contact says why in the picker and stays open.
	m = chatReadyModel(client, "whatsapp:personal:1")
	m.width = 100
	m = pressDrain(t, m, digitKey('n'))
	m = pressDrain(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = pressDrain(t, m, altKey('c'))
	if m.picker == nil || m.chatMode || !strings.Contains(m.View(), "No se puede llamar: las llamadas de voz solo están disponibles en WhatsApp") {
		t.Errorf("picker %v:\n%s", m.picker, m.View())
	}
	if len(client.placed) != 2 {
		t.Errorf("placed %+v", client.placed)
	}
}

func TestPaletteListsSearchAndCallEntries(t *testing.T) {
	m := chatReadyModel(&callClient{}, "whatsapp:personal:1")
	m, _ = press(t, m, ctrlKKey)
	byLabel := map[string]paletteEntry{}
	for _, e := range m.paletteCommands() {
		byLabel[e.label] = e
	}
	if e, ok := byLabel["Buscar contacto"]; !ok || e.key != "@" || e.reason != "" {
		t.Errorf("Buscar contacto = %+v", e)
	}
	if e, ok := byLabel["Llamar"]; !ok || e.key != "c" || e.reason != "" {
		t.Errorf("Llamar = %+v", e)
	}
	// Running them replays their keys.
	for _, label := range []string{"Llamar", "Buscar contacto"} {
		mm := chatReadyModel(&searchClient{}, "whatsapp:personal:1")
		mm, _ = press(t, mm, ctrlKKey)
		mm = typeRunes(mm, label)
		mm = pressDrain(t, mm, tea.KeyMsg{Type: tea.KeyEnter})
		if label == "Llamar" && (!mm.chatMode || mm.chatAction == nil || mm.chatAction.kind != "call") {
			t.Errorf("palette Llamar: chatMode %v action %+v", mm.chatMode, mm.chatAction)
		}
		if label == "Buscar contacto" && mm.picker == nil {
			t.Error("palette Buscar contacto did not open the picker")
		}
	}

	// Unavailable reasons, like the other entries.
	mail := callInbox(&callClient{})
	for _, e := range mail.paletteCommands() {
		if e.label == "Llamar" && !strings.Contains(e.reason, "WhatsApp") {
			t.Errorf("mail row Llamar reason = %q", e.reason)
		}
	}
	none := chatReadyModel(&replyClient{}, "whatsapp:personal:1")
	for _, e := range none.paletteCommands() {
		if e.label == "Llamar" && e.reason == "" {
			t.Error("a connection without calls should dim Llamar")
		}
	}
	// Not in chats.
	chat := callChat(t, &callClient{})
	for _, e := range chat.paletteCommands() {
		if e.label == "Buscar contacto" {
			t.Error("the chat palette should not list Buscar contacto")
		}
	}
}

func TestShortcutHintsAreDroppedFirstAndHelpDescribesThem(t *testing.T) {
	wide := hintLine(200, inboxHints...)
	for _, want := range []string{"@ contacto", "c llamar"} {
		if !strings.Contains(wide, want) {
			t.Errorf("wide hints %q lack %q", wide, want)
		}
	}
	narrow := hintLine(70, inboxHints...)
	if strings.Contains(narrow, "@ contacto") || strings.Contains(narrow, "c llamar") || !strings.Contains(narrow, "q salir") || !strings.Contains(narrow, "? ayuda") {
		t.Errorf("narrow hints %q should drop the new hints first", narrow)
	}
	for _, ctx := range []string{"inbox", "sidebar", "picker", "calls"} {
		body, _ := helpBody(ctx, false)
		text := strings.Join(body, "\n")
		switch ctx {
		case "inbox", "sidebar":
			if !strings.Contains(text, "buscar contacto") || !strings.Contains(text, "llamar") {
				t.Errorf("%s help lacks @ and c", ctx)
			}
		case "picker":
			if !strings.Contains(text, "Alt+C") {
				t.Error("picker help lacks Alt+C")
			}
		}
	}
}
