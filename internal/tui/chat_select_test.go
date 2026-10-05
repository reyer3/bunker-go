package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

var (
	altUp   = tea.KeyMsg{Type: tea.KeyUp, Alt: true}
	altDown = tea.KeyMsg{Type: tea.KeyDown, Alt: true}
	escKey  = tea.KeyMsg{Type: tea.KeyEsc}
)

func TestAltArrowKeyStrings(t *testing.T) {
	if altUp.String() != chatSelectPrevKey || altDown.String() != chatSelectNextKey {
		t.Fatalf("bubbletea reports %q/%q, the chat matches %q/%q",
			altUp.String(), altDown.String(), chatSelectPrevKey, chatSelectNextKey)
	}
}

func TestAltUpDownMovesTheChatSelection(t *testing.T) {
	model, _ := uxChat(t, &replyClient{})
	steps := []struct {
		key  tea.KeyMsg
		want string
	}{
		{altUp, "whatsapp:personal:c"}, // from none: the newest
		{altUp, "whatsapp:personal:b"},
		{altUp, "whatsapp:personal:a"},
		{altUp, "whatsapp:personal:a"}, // the oldest loaded stays
		{altDown, "whatsapp:personal:b"},
		{altDown, "whatsapp:personal:c"},
		{altDown, ""}, // past the newest: no selection
		{altDown, ""},
	}
	for i, s := range steps {
		var cmd tea.Cmd
		model, cmd = uxPress(t, model, s.key)
		if model.chatFocus != s.want {
			t.Fatalf("step %d (%s): focus = %q, want %q", i, s.key, model.chatFocus, s.want)
		}
		if cmd != nil {
			t.Fatalf("step %d: selecting a message sent a command", i)
		}
	}
	if model.composer.Value() != "" {
		t.Fatalf("Alt+arrows typed into the composer: %q", model.composer.Value())
	}
}

func TestAltYCopiesTheKeyboardSelection(t *testing.T) {
	model, _ := uxChat(t, &replyClient{})
	rc := &recordingCopier{tools: map[string]bool{"xclip": true}}
	model.copier = rc.copier(map[string]string{"DISPLAY": ":0"})
	model, _ = uxPress(t, model, altUp)
	model, _ = uxPress(t, model, altUp)
	model, _ = uxPress(t, model, altUp)
	_, cmd := uxPress(t, model, keyAlt('y'))
	cmd()
	if len(rc.stdin) != 1 || rc.stdin[0] != "primer mensaje" {
		t.Fatalf("copied %q, want the keyboard-selected message", rc.stdin)
	}
}

func TestEscClearsTheSelectionBeforeLeaving(t *testing.T) {
	model, _ := uxChat(t, &replyClient{})
	model, _ = uxPress(t, model, altUp)
	model, _ = uxPress(t, model, escKey)
	if !model.chatMode || model.chatFocus != "" {
		t.Fatalf("Esc with a selection: chatMode=%v focus=%q, want the chat open and no selection", model.chatMode, model.chatFocus)
	}
	model, _ = uxPress(t, model, escKey)
	if model.chatMode {
		t.Fatal("Esc without a selection should still leave the chat")
	}
}

func TestKeyboardSelectionScrollsIntoView(t *testing.T) {
	model, _ := chatModelWithManyMessages(40, 62, 20)
	for i := 0; i < 40; i++ {
		model, _ = uxPress(t, model, altUp)
	}
	if model.chatFocus != "whatsapp:personal:0" {
		t.Fatalf("focus = %q after 40 Alt+Up, want the oldest", model.chatFocus)
	}
	if view := model.View(); !strings.Contains(view, "mensaje numero 0") {
		t.Fatalf("the selected oldest message is not on screen:\n%s", view)
	}
	for i := 0; i < 39; i++ {
		model, _ = uxPress(t, model, altDown)
	}
	if model.chatFocus != "whatsapp:personal:39" {
		t.Fatalf("focus = %q, want the newest", model.chatFocus)
	}
	if view := model.View(); !strings.Contains(view, "mensaje numero 39") {
		t.Fatalf("the selected newest message is not on screen:\n%s", view)
	}
}
