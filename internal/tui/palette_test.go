package tui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"

	"github.com/reyer3/bunker-go/internal/core"
)

var (
	ctrlKKey = tea.KeyMsg{Type: tea.KeyCtrlK}
	f2Key    = tea.KeyMsg{Type: tea.KeyF2}
)

// typePalette types text into the open palette, feeding back any
// contacts answer the "@" list asks for.
func typePalette(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			key = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		}
		var cmd tea.Cmd
		m, cmd = press(t, m, key)
		if cmd != nil {
			if msg, ok := cmd().(paletteContactsMsg); ok {
				updated, _ := m.Update(msg)
				m = updated.(Model)
			}
		}
	}
	return m
}

func paletteLabels(m Model) []string {
	var out []string
	for _, e := range m.paletteEntries() {
		out = append(out, e.label)
	}
	return out
}

func hasLabel(m Model, label string) bool {
	for _, l := range paletteLabels(m) {
		if l == label {
			return true
		}
	}
	return false
}

func TestPaletteOpensAndClosesInEveryView(t *testing.T) {
	inbox := chatReadyModel(&replyClient{}, "whatsapp:personal:1")
	sidebar := sidebarModel()
	thread := threadReadyWithItems(&replyClient{}, "mail:cl:1", []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
		From: core.Address{ID: "bob@example.com", Name: "Bob"}, Subject: "hola",
	}})
	chat, _ := openedChat(t)

	for _, tc := range []struct {
		name  string
		m     Model
		open  tea.KeyMsg
		close tea.KeyMsg
	}{
		{"inbox", inbox, ctrlKKey, tea.KeyMsg{Type: tea.KeyEsc}},
		{"sidebar", sidebar, ctrlKKey, ctrlKKey},
		{"thread", thread, ctrlKKey, tea.KeyMsg{Type: tea.KeyEsc}},
		{"thread F2", thread, f2Key, f2Key},
		{"chat", chat, f2Key, tea.KeyMsg{Type: tea.KeyEsc}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.m.View()
			m, _ := press(t, tc.m, tc.open)
			if m.palette == nil || !strings.Contains(m.View(), "Comandos") {
				t.Fatalf("palette did not open:\n%s", m.View())
			}
			m, _ = press(t, m, tc.close)
			if m.palette != nil {
				t.Fatal("palette did not close")
			}
			// Closing hands no key to the view underneath: it is exactly
			// as it was.
			if m.detail != tc.m.detail || m.chatMode != tc.m.chatMode || m.threadMode != tc.m.threadMode || m.View() != before {
				t.Fatalf("closing changed the view:\n%s\nwant:\n%s", m.View(), before)
			}
		})
	}
}

// TestPaletteCtrlKStaysKillLineInChat pins why a chat opens the palette
// with F2 only: its composer always has focus, and Ctrl+K there deletes
// to the end of the line.
func TestPaletteCtrlKStaysKillLineInChat(t *testing.T) {
	m, _ := openedChat(t)
	m = typeIntoChat(t, m, "hola mundo")
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyHome})
	m, _ = press(t, m, ctrlKKey)
	if m.palette != nil {
		t.Fatal("Ctrl+K opened the palette over the chat composer")
	}
	if got := m.composer.Value(); got != "" {
		t.Fatalf("Ctrl+K should kill the line in the composer, draft = %q", got)
	}

	// Nor does it open over the reply composer, the mail editor or the
	// filter, where it is text editing too.
	inbox := chatReadyModel(&replyClient{}, "whatsapp:personal:1")
	filtering, _ := press(t, inbox, runeKey("/"))
	for _, key := range []tea.KeyMsg{ctrlKKey, f2Key} {
		if next, _ := press(t, filtering, key); next.palette != nil {
			t.Fatalf("%s opened the palette while filtering", key)
		}
	}
	replying, _ := press(t, inbox, runeKey("r"))
	if !replying.composing {
		t.Fatal("r did not open the reply composer")
	}
	if next, _ := press(t, replying, f2Key); next.palette != nil {
		t.Fatal("F2 opened the palette over the reply composer")
	}
}

func TestPaletteFiltersIgnoringAccents(t *testing.T) {
	m := chatReadyModel(&replyClient{}, "whatsapp:personal:1")
	m, _ = press(t, m, ctrlKKey)

	accented := typePalette(t, m, "LEÍDO")
	plain := typePalette(t, m, "leido")
	if got, want := strings.Join(paletteLabels(accented), "|"), strings.Join(paletteLabels(plain), "|"); got != want {
		t.Fatalf("accents changed the matches: %q vs %q", got, want)
	}
	labels := paletteLabels(plain)
	if len(labels) != 2 || labels[0] != "Marcar leído" || labels[1] != "Deshacer leído" {
		t.Fatalf("leido matches = %q, want the runnable Marcar leído first", labels)
	}

	if fuzzy := paletteLabels(typePalette(t, m, "rfsc")); len(fuzzy) != 1 || fuzzy[0] != "Refrescar" {
		t.Fatalf("letters in order should match Refrescar, got %q", fuzzy)
	}
	if recent := paletteLabels(typePalette(t, m, "alice")); len(recent) != 1 || recent[0] != "Alice" {
		t.Fatalf("typing a name should list its recent conversation, got %q", recent)
	}
	if view := typePalette(t, m, "zzz").View(); !strings.Contains(view, "sin coincidencias") {
		t.Fatalf("no match view:\n%s", view)
	}
}

// runViaPalette opens the palette, types query and runs the first entry.
func runViaPalette(t *testing.T, m Model, open tea.KeyMsg, query string) (Model, tea.Cmd) {
	t.Helper()
	m, _ = press(t, m, open)
	m = typePalette(t, m, query)
	return press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
}

func TestPaletteCommandRunsTheSamePathAsItsKey(t *testing.T) {
	base := chatReadyModel(&replyClient{}, "whatsapp:personal:1")
	base.polling = false

	byKey, keyCmd := press(t, base, runeKey("g"))
	byPalette, paletteCmd := runViaPalette(t, base, ctrlKKey, "refrescar")
	if byPalette.palette != nil || !byPalette.polling || byPalette.pollToken != byKey.pollToken || (keyCmd == nil) != (paletteCmd == nil) {
		t.Fatalf("Refrescar: polling=%v token=%d, key token=%d", byPalette.polling, byPalette.pollToken, byKey.pollToken)
	}

	byKey, _ = press(t, base, runeKey("m"))
	byPalette, paletteCmd = runViaPalette(t, base, ctrlKKey, "marcar")
	if !byPalette.marking || byPalette.markID != byKey.markID || byPalette.markToken != byKey.markToken || paletteCmd == nil {
		t.Fatalf("Marcar leído: marking=%v id=%q, key id=%q", byPalette.marking, byPalette.markID, byKey.markID)
	}
	if _, ok := paletteCmd().(markPreviewMsg); !ok {
		t.Fatal("Marcar leído should request the dry-run preview first, like m")
	}

	byPalette, _ = runViaPalette(t, base, ctrlKKey, "ir a whatsapp")
	if byKey, _ = press(t, base, runeKey("2")); byPalette.activeTab != byKey.activeTab || byPalette.activeTab != 2 {
		t.Fatalf("Ir a WhatsApp: tab %d, key tab %d", byPalette.activeTab, byKey.activeTab)
	}

	byPalette, _ = runViaPalette(t, base, ctrlKKey, "ayuda")
	if !byPalette.helpOpen || byPalette.helpCtx != "inbox" {
		t.Fatalf("Ayuda should open the inbox help, got open=%v ctx=%q", byPalette.helpOpen, byPalette.helpCtx)
	}

	chat, _ := actionChat(t)
	byKey, _ = press(t, chat, altKey('e'))
	byPalette, _ = runViaPalette(t, chat, f2Key, "editar")
	if byPalette.chatEditID == "" || byPalette.chatEditID != byKey.chatEditID || byPalette.composer.Value() != byKey.composer.Value() {
		t.Fatalf("Editar: edit id %q draft %q, key %q %q", byPalette.chatEditID, byPalette.composer.Value(), byKey.chatEditID, byKey.composer.Value())
	}
	byKey, _ = press(t, chat, altKey('+'))
	byPalette, _ = runViaPalette(t, chat, f2Key, "reaccionar")
	if byPalette.chatAction == nil || byKey.chatAction == nil || *byPalette.chatAction != *byKey.chatAction {
		t.Fatalf("Reaccionar: %+v, key %+v", byPalette.chatAction, byKey.chatAction)
	}
}

func TestPaletteHidesAndDimsCommandsByContext(t *testing.T) {
	inbox, _ := press(t, chatReadyModel(&replyClient{}, "whatsapp:personal:1"), ctrlKKey)
	for _, want := range []string{"Nuevo mensaje", "Buscar / filtrar", "Ir a Mail", "Responder", "Salir"} {
		if !hasLabel(inbox, want) {
			t.Errorf("inbox palette lacks %q: %q", want, paletteLabels(inbox))
		}
	}
	for _, hidden := range []string{"Editar último mensaje", "Responder a todos", "Preguntar a Claude"} {
		if hasLabel(inbox, hidden) {
			t.Errorf("inbox palette lists %q", hidden)
		}
	}

	chat, _ := actionChat(t)
	chat, _ = press(t, chat, f2Key)
	for _, want := range []string{"Editar último mensaje", "Borrar último mensaje", "Reaccionar al último mensaje", "Volver a la bandeja"} {
		if !hasLabel(chat, want) {
			t.Errorf("chat palette lacks %q: %q", want, paletteLabels(chat))
		}
	}
	for _, hidden := range []string{"Ir a Mail", "Nuevo mensaje", "Marcar leído"} {
		if hasLabel(chat, hidden) {
			t.Errorf("chat palette lists %q", hidden)
		}
	}

	// The ask command exists only with an asker (inside herdr).
	asking := chatReadyModel(&replyClient{}, "whatsapp:personal:1")
	asking.agentAsk = func(context.Context, string) error { return nil }
	if asking, _ = press(t, asking, ctrlKKey); !hasLabel(asking, "Preguntar a Claude") {
		t.Errorf("with an asker the palette lacks Preguntar a Claude: %q", paletteLabels(asking))
	}

	// A command of this view that cannot run now is dimmed with why, and
	// ↵ on it does nothing.
	undo := typePalette(t, inbox, "deshacer")
	if !strings.Contains(undo.View(), "Deshacer leído · nada que deshacer") {
		t.Fatalf("dimmed command should say why:\n%s", undo.View())
	}
	after, cmd := press(t, undo, tea.KeyMsg{Type: tea.KeyEnter})
	if after.palette == nil || cmd != nil {
		t.Fatal("↵ on a dimmed command should do nothing")
	}
}

func TestPaletteRecentOpensTheConversation(t *testing.T) {
	m := chatReadyModel(&replyClient{}, "whatsapp:personal:1")
	m, cmd := runViaPalette(t, m, ctrlKKey, "alice")
	if m.palette != nil || !m.chatMode || m.chatThread != "5511999999999@s.whatsapp.net" || cmd == nil {
		t.Fatalf("recent did not open the chat: palette=%v chat=%v thread=%q", m.palette != nil, m.chatMode, m.chatThread)
	}

	// From a mail thread it leaves the thread first.
	thread := threadReadyWithItems(&replyClient{}, "mail:cl:1", []core.Item{{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1"}})
	thread.groups = append(thread.groups, inboxGroup{items: []core.Item{{
		ID: "whatsapp:personal:9", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t9",
		From: core.Address{Name: "Dana"}, Unread: true,
	}}})
	thread, _ = runViaPalette(t, thread, ctrlKKey, "dana")
	if thread.threadMode || !thread.chatMode || thread.chatThread != "t9" {
		t.Fatalf("jump from a thread: thread=%v chat=%v %q", thread.threadMode, thread.chatMode, thread.chatThread)
	}
}

func TestPaletteAtListsContactsAndStartsAMessage(t *testing.T) {
	client := &contactsClient{book: []core.Contact{
		{Channel: core.ChannelWhatsApp, Account: "personal", Name: "Ana Díaz", Address: "51922@s.whatsapp.net"},
		{Channel: core.ChannelMail, Account: "cl", Name: "Carla", Address: "carla@example.cl"},
	}}
	m := chatReadyModel(client, "whatsapp:personal:1")
	m.width = 60
	m, _ = press(t, m, ctrlKKey)
	m = typePalette(t, m, "@")
	if view := m.View(); !strings.Contains(view, "contactos") || !strings.Contains(view, "Ana Díaz") || !strings.Contains(view, "Carla") {
		t.Fatalf("@ should list contacts:\n%s", view)
	}
	m = typePalette(t, m, "ana")
	if got := client.queries[len(client.queries)-1]; got != "ana" {
		t.Fatalf("contacts query = %q", got)
	}
	if labels := paletteLabels(m); len(labels) != 1 || labels[0] != "Ana Díaz" {
		t.Fatalf("@ana lists %q", labels)
	}
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.palette != nil || !m.chatMode || m.chatThread != "51922@s.whatsapp.net" || m.chatNewTo != "51922@s.whatsapp.net" {
		t.Fatalf("contact did not start a chat: chat=%v thread=%q to=%q", m.chatMode, m.chatThread, m.chatNewTo)
	}
}

func paletteGoldenModel(width, height int, opts ...Option) Model {
	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	m := NewModel(&replyClient{}, opts...).withGlyphs(nil)
	m.polling = false
	m.render = r
	m.now = func() time.Time { return at }
	m.loaded = true
	m.width, m.height = width, height
	m.groups = []inboxGroup{
		{items: []core.Item{{
			ID: "whatsapp:personal:2", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "g1",
			ThreadName: "Equipo de producto y diseño", From: core.Address{Name: "Bob"}, Unread: true, Timestamp: at,
		}}},
		{items: []core.Item{{
			ID: "matrix:home:4", Channel: core.ChannelMatrix, Account: "home", Thread: "!room:example.org",
			ThreadName: "Soporte", From: core.Address{Name: "Erin"}, Unread: true, Timestamp: at.Add(-time.Hour),
		}}},
	}
	m.counts = map[core.Channel]map[string]int{
		core.ChannelWhatsApp: {"personal": 1},
		core.ChannelMatrix:   {"home": 1},
	}
	next, _ := m.update(ctrlKKey)
	return next.(Model)
}

// TestPaletteGoldenWidth80 goldens the palette over the full inbox: the
// runnable commands with their keys, the dimmed ones with why, then the
// recent conversations.
func TestPaletteGoldenWidth80(t *testing.T) {
	m := paletteGoldenModel(80, 30)
	teatest.RequireEqualOutput(t, []byte(m.View()))
}

// TestPaletteGoldenWidth32 goldens it in the herdr sidebar's width, where
// the list scrolls to fit the pane.
func TestPaletteGoldenWidth32(t *testing.T) {
	m := paletteGoldenModel(32, 16, WithSidebar())
	teatest.RequireEqualOutput(t, []byte(m.View()))
}

// TestPaletteF1OpensItsHelpSection pins that help over the palette opens
// on the palette's own section and closing it returns to the palette.
func TestPaletteF1OpensItsHelpSection(t *testing.T) {
	m, _ := press(t, chatReadyModel(&replyClient{}, "whatsapp:personal:1"), ctrlKKey)
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyF1})
	if !m.helpOpen || m.helpCtx != "palette" || !strings.Contains(m.View(), "Paleta de comandos") {
		t.Fatalf("F1 over the palette: open=%v ctx=%q", m.helpOpen, m.helpCtx)
	}
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.helpOpen || m.palette == nil {
		t.Fatal("closing the help should return to the palette")
	}
}
