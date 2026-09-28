package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEmojiQuery(t *testing.T) {
	cases := []struct {
		text, want string
		ok         bool
	}{
		{":ri", "ri", true},
		{"hola :risa", "risa", true},
		{"hola\n:cora", "cora", true},
		{":r", "", false},           // too short
		{"a las 12:30", "", false},  // mid-word colon
		{"ver http://x", "", false}, // URL
		{"hola:risa", "", false},    // no space before the colon
		{"hola :risa ", "", false},  // already finished
		{"hola :corazón", "corazón", true},
	}
	for _, c := range cases {
		got, ok := emojiQuery(c.text)
		if got != c.want || ok != c.ok {
			t.Errorf("emojiQuery(%q) = %q, %v; want %q, %v", c.text, got, ok, c.want, c.ok)
		}
	}
}

func TestMatchEmoji(t *testing.T) {
	if m := matchEmoji("risa"); len(m) == 0 || m[0].emoji != "😂" {
		t.Errorf("matchEmoji(risa) = %v, want 😂 first", m)
	}
	if m := matchEmoji("thumbs"); len(m) == 0 || m[0].emoji != "👍" {
		t.Errorf("matchEmoji(thumbs) = %v, want 👍 first", m)
	}
	if m := matchEmoji("Corazón"); len(m) == 0 || m[0].emoji != "😍" && m[0].emoji != "❤️" {
		t.Errorf("accented query found %v", m)
	}
	if m := matchEmoji("co"); len(m) > emojiMaxMatches {
		t.Errorf("got %d matches, cap is %d", len(m), emojiMaxMatches)
	}
	if m := matchEmoji("zzzqqq"); len(m) != 0 {
		t.Errorf("nonsense query matched %v", m)
	}
}

func typeIntoChat(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		var key tea.KeyMsg
		if r == ' ' {
			key = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		} else {
			key = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		}
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	return m
}

func openedChat(t *testing.T) (Model, *replyClient) {
	t.Helper()
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.width = 80
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd().(chatThreadLoadedMsg))
	return updated.(Model), client
}

func TestEmojiCompletionInsertsWithoutSending(t *testing.T) {
	model, client := openedChat(t)
	model = typeIntoChat(t, model, "jaja :risa")
	if !strings.Contains(model.View(), "😂 :joy") {
		t.Fatalf("completion list missing:\n%s", model.View())
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil || len(client.calls) != 0 || model.chatPreviewPending {
		t.Fatal("Enter on a completion must insert, not send")
	}
	if got := model.composer.Value(); got != "jaja 😂 " {
		t.Fatalf("draft = %q, want %q", got, "jaja 😂 ")
	}
	if strings.Contains(model.View(), ":joy") {
		t.Error("completion list still shown after inserting")
	}
}

func TestEmojiCompletionTabAndEsc(t *testing.T) {
	model, _ := openedChat(t)
	model = typeIntoChat(t, model, ":cora")
	first := matchEmoji("cora")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if got := model.composer.Value(); got != first[1].emoji+" " {
		t.Fatalf("Tab then Enter inserted %q, want the second match %q", got, first[1].emoji)
	}

	model = typeIntoChat(t, model, ":risa")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if !model.chatMode {
		t.Fatal("Esc on an open completion left the chat")
	}
	if strings.Contains(model.View(), ":joy") {
		t.Fatal("Esc did not dismiss the completion")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(Model).chatMode {
		t.Error("a second Esc should leave the chat as usual")
	}
}

func TestEmojiCompletionIgnoresTimes(t *testing.T) {
	model, _ := openedChat(t)
	model = typeIntoChat(t, model, "a las 12:30")
	if len(model.emojiMatches()) != 0 {
		t.Fatal("a time opened the emoji completion")
	}
}

func TestHelpListsChatKeys(t *testing.T) {
	model := NewModel(nil)
	model.width = 80
	help := model.helpView()
	for _, key := range []string{"Ctrl+O", "Ctrl+V", "arrastrar", ":risa", "Ctrl+D"} {
		if !strings.Contains(help, key) {
			t.Errorf("help overlay does not mention %q", key)
		}
	}
}
