package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

func filterModel(t *testing.T) Model {
	t.Helper()
	now := time.Now()
	client := &inboxClient{items: []core.Item{
		{ID: "whatsapp:wa:1", Channel: core.ChannelWhatsApp, Account: "wa", Thread: "a", ThreadName: "José Pérez", Body: "nos vemos", Unread: true, Timestamp: now},
		{ID: "whatsapp:wa:2", Channel: core.ChannelWhatsApp, Account: "wa", Thread: "b", ThreadName: "Equipo", Body: "reunión a las 5", Unread: true, Timestamp: now},
		{ID: "matrix:m:1", Channel: core.ChannelMatrix, Account: "m", Thread: "c", ThreadName: "General", Body: "hola", Unread: true, Timestamp: now},
	}}
	m := NewModel(client)
	m.width, m.height = 80, 24
	return pollOnce(t, m)
}

func typeFilter(m Model, text string) Model {
	for _, r := range text {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	return m
}

func TestInboxFilter(t *testing.T) {
	m := filterModel(t)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = typeFilter(updated.(Model), "jose")
	view := m.View()
	if !strings.Contains(view, "José Pérez") || strings.Contains(view, "Equipo") || strings.Contains(view, "General") {
		t.Fatalf("filter jose (accent-insensitive, by name):\n%s", view)
	}
	if !strings.Contains(view, "sin coincidencias") {
		t.Fatalf("a section the filter empties should say so:\n%s", view)
	}
	if !strings.Contains(view, "/jose") {
		t.Fatalf("the typed filter should show:\n%s", view)
	}

	// Enter keeps the filter and returns to the list, where keys work again.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.filtering || !strings.Contains(m.View(), "filtro: jose") {
		t.Fatalf("after enter:\n%s", m.View())
	}

	// Matching also looks at the text.
	m.filterQuery = "reunion"
	if view := m.View(); !strings.Contains(view, "Equipo") || strings.Contains(view, "José") {
		t.Fatalf("filter by body text:\n%s", view)
	}

	// Esc clears it.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.filterQuery != "" || !strings.Contains(m.View(), "General") {
		t.Fatalf("Esc should clear the filter:\n%s", m.View())
	}
}

func TestChatEmptyAndLoadingStates(t *testing.T) {
	model := chatReadyModel(&replyClient{}, "whatsapp:personal:1")
	model.width = 60
	model, cmd := openChat(model)
	if !strings.Contains(model.View(), "cargando mensajes") {
		t.Fatalf("an opening chat should say it is loading:\n%s", model.View())
	}
	msg := cmd().(chatThreadLoadedMsg)
	msg.items = nil
	updated, _ := model.Update(msg)
	if !strings.Contains(updated.(Model).View(), "sin mensajes todavía") {
		t.Fatalf("an empty chat should say so:\n%s", updated.(Model).View())
	}
}
