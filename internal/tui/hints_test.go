package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

func TestHintLineDropsLowPriorityNeverPinned(t *testing.T) {
	for _, width := range []int{20, 40, 80, 200} {
		line := hintLine(width, inboxHints...)
		if runewidth.StringWidth(line) > width {
			t.Errorf("width %d: %q overflows", width, line)
		}
		if width >= 20 && (!strings.Contains(line, "q salir") || !strings.Contains(line, "? ayuda")) {
			t.Errorf("width %d: pinned hints missing: %q", width, line)
		}
	}
	if line := hintLine(200, inboxHints...); !strings.Contains(line, "n nuevo") || !strings.Contains(line, "1/2/3 secciones") {
		t.Errorf("a wide line should list every key: %q", line)
	}
	narrow := hintLine(40, chatHints...)
	if !strings.Contains(narrow, "Esc volver") || !strings.Contains(narrow, "F1 ayuda") || strings.Contains(narrow, "\n") {
		t.Errorf("chat hint at 40 columns = %q", narrow)
	}
}

func TestHelpOpensOnCurrentViewAndScrolls(t *testing.T) {
	model, _ := openedChat(t)
	model.height = 10
	// ? is text in a chat: F1 opens the help there.
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyF1})
	model = updated.(Model)
	view := model.View()
	lines := strings.Split(view, "\n")
	if !model.helpOpen || !strings.HasPrefix(lines[0], "Ayuda") || !strings.Contains(lines[1], "En un chat") {
		t.Fatalf("help from a chat should open on the chat section:\n%s", view)
	}
	if len(lines) > model.height {
		t.Fatalf("help has %d lines at height %d", len(lines), model.height)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if after := updated.(Model).View(); after == view {
		t.Fatal("j should scroll the help")
	}
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m := updated.(Model); m.helpOpen || !m.chatMode {
		t.Fatal("Esc should close the help and return to the chat")
	}
}

func TestHelpListsChatKeys(t *testing.T) {
	model := NewModel(nil)
	model.width = 80
	help := model.helpView()
	for _, key := range []string{"Ctrl+O", "Ctrl+V", "arrastrar", ":risa", "Ctrl+D", "F1", "Tab/⇧Tab"} {
		if !strings.Contains(help, key) {
			t.Errorf("help overlay does not mention %q", key)
		}
	}
}

func TestCtrlSSendsLikeEnterEverywhere(t *testing.T) {
	// Chat: Ctrl+S previews like Enter, and confirms like Enter.
	model, client := openedChat(t)
	model = typeIntoChat(t, model, "hola")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("Ctrl+S in a chat should preview the send")
	}
	updated, _ = updated.(Model).Update(cmd())
	if !updated.(Model).chatConfirm {
		t.Fatal("the preview should wait for a confirm")
	}
	_, cmd = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if cmd == nil {
		t.Fatal("Ctrl+S should also confirm")
	}
	runCmds(cmd)
	real := 0
	for _, c := range client.calls {
		if !c.dryRun {
			real++
		}
	}
	if real != 1 {
		t.Fatalf("real sends = %d, want 1", real)
	}
}

func TestComposerHintsUseOneNotation(t *testing.T) {
	for _, hints := range [][]keyHint{inboxHints, chatHints, threadHints, composeHints, confirmSendHints, mailEditorHints} {
		for _, h := range hints {
			if strings.Contains(h.key, "Enter") || strings.Contains(h.label, " to ") {
				t.Errorf("hint %+v breaks the notation (↵, Spanish labels)", h)
			}
		}
	}
}
