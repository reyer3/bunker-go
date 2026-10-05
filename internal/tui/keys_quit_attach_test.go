package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

func ctrlC(t *testing.T, m Model) tea.Cmd {
	t.Helper()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	return cmd
}

func TestCtrlCQuitsFromEveryView(t *testing.T) {
	client := &replyClient{}
	inbox := readyModel(client, "mail:a:1")
	detail := inbox
	detail.detail = true
	thread := inbox
	thread.detail, thread.threadMode = true, true
	updated, _ := inbox.Update(runeKey("r"))
	composing := updated.(Model)
	help := inbox.openHelp()
	palette := inbox.openPalette()
	filtering := inbox.startFilter()
	chat, _ := openedChat(t)
	editor := thread
	editor.mailComposing = true
	editor.composer = newComposer(80, nil)
	for name, m := range map[string]Model{
		"inbox": inbox, "detail": detail, "thread": thread, "reply composer": composing,
		"help": help, "palette": palette, "filter": filtering, "chat": chat, "mail editor": editor,
	} {
		if !sequenceQuits(t, ctrlC(t, m)) {
			t.Errorf("Ctrl+C in the %s does not quit", name)
		}
	}
}

func TestCtrlCDuringACallQuitsLikeQ(t *testing.T) {
	m := NewModel(nil)
	live := ringingCall()
	live.State = core.CallStateActive
	m.calls = []core.Call{live}
	cmd := ctrlC(t, m)
	if cmd == nil {
		t.Fatal("Ctrl+C during a call does nothing")
	}
	// Quitting the interface never hangs up: the call lives in the
	// daemon, exactly as with q.
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("Ctrl+C during a call should only quit, like q")
	}
}

func TestQQuitsMailThreadButIsTextInItsEditor(t *testing.T) {
	m := NewModel(nil)
	m.detail, m.threadMode = true, true
	_, cmd := m.Update(runeKey("q"))
	if cmd == nil {
		t.Fatal("q in a mail thread does nothing")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q in a mail thread should quit")
	}
	salir := false
	for _, e := range m.paletteCommands() {
		salir = salir || e.label == "Salir"
	}
	if !salir {
		t.Error("the thread palette should list Salir (q)")
	}
	m.mailComposing = true
	m.mailFocus = 3
	m.composer = newComposer(80, nil)
	updated, cmd := m.Update(runeKey("q"))
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("q in the mail editor quit instead of typing")
		}
	}
	if got := updated.(Model).composer.Value(); got != "q" {
		t.Fatalf("mail editor body = %q, want q typed", got)
	}
}

func replyComposer(t *testing.T) Model {
	t.Helper()
	model := readyModel(&replyClient{}, "mail:a:1")
	updated, _ := model.Update(runeKey("r"))
	return updated.(Model)
}

func TestReplyComposerCtrlAIsLineStartNotAttach(t *testing.T) {
	model := typeRunes(replyComposer(t), "hola")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	model = updated.(Model)
	if model.attaching {
		t.Fatal("Ctrl+A still opens the attach prompt")
	}
	model = typeRunes(model, "X")
	if got := model.composer.Value(); got != "Xhola" {
		t.Fatalf("draft = %q, want Ctrl+A to move to the line start", got)
	}
}

func TestReplyComposerAttachKeyOpensThePathPrompt(t *testing.T) {
	updated, _ := replyComposer(t).Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if !updated.(Model).attaching {
		t.Fatal("Ctrl+R should open the attachment path prompt")
	}
	for _, h := range composeHints {
		if h.key == "Ctrl+A" {
			t.Errorf("compose hints still list Ctrl+A: %+v", h)
		}
	}
	if line := hintLine(200, composeHints...); !strings.Contains(line, "Ctrl+R adjuntar") {
		t.Errorf("compose hints = %q, want Ctrl+R adjuntar", line)
	}
}

func TestReplyComposerAttachesDroppedFiles(t *testing.T) {
	path := writeTempFile(t, "informe.pdf", "hola")
	updated, _ := replyComposer(t).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(path), Paste: true})
	model := updated.(Model)
	if len(model.attachments) != 1 || model.attachments[0] != path {
		t.Fatalf("attachments = %v, want the dropped file", model.attachments)
	}
	if model.composer.Value() != "" {
		t.Fatalf("the dropped path was typed into the draft: %q", model.composer.Value())
	}
}

func TestReplyComposerPastesClipboardImage(t *testing.T) {
	model := replyComposer(t)
	model.clipboard = fakeClipboard{data: []byte("PNGDATA")}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if cmd == nil {
		t.Fatal("Ctrl+V in the reply composer does not read the clipboard")
	}
	msg := cmd().(clipboardImageMsg)
	updated, _ = updated.(Model).Update(msg)
	model = updated.(Model)
	if len(model.attachments) != 1 || model.attachments[0] != msg.path {
		t.Fatalf("attachments = %v, want the pasted image", model.attachments)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if len(updated.(Model).attachments) != 0 {
		t.Fatal("Ctrl+X did not remove the pasted image")
	}
	if _, err := os.Stat(msg.path); !os.IsNotExist(err) {
		t.Error("removing a pasted image should delete its temp file")
	}

	model.clipboard = fakeClipboard{data: []byte("PNGDATA")}
	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	msg = cmd().(clipboardImageMsg)
	updated, _ = model.Update(msg)
	_, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, err := os.Stat(msg.path); !os.IsNotExist(err) {
		t.Error("closing the composer should delete pasted temp images")
	}

	model.clipboard = fakeClipboard{err: errNoClipboardImage}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	updated, _ = updated.(Model).Update(cmd())
	if !strings.Contains(updated.(Model).View(), "no tiene una imagen") {
		t.Error("a clipboard without an image should say so in the composer")
	}
}
