package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func tempFile(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseDroppedPaths(t *testing.T) {
	dir := t.TempDir()
	plain := tempFile(t, dir, "foto.png")
	spaced := tempFile(t, dir, "mi foto.jpg")

	cases := []struct {
		text string
		want []string
		ok   bool
	}{
		{plain, []string{plain}, true},
		{"file://" + strings.ReplaceAll(spaced, " ", "%20"), []string{spaced}, true},
		{"'" + spaced + "'", []string{spaced}, true},
		{strings.ReplaceAll(spaced, " ", `\ `), []string{spaced}, true},
		{plain + " " + strings.ReplaceAll(spaced, " ", `\ `), []string{plain, spaced}, true},
		{plain + "\n" + "'" + spaced + "'\n", []string{plain, spaced}, true},
		{"hola mundo", nil, false},
		{filepath.Join(dir, "no-existe.png"), nil, false},
		{dir, nil, false}, // a directory
		{"foto.png", nil, false},
		{"'" + plain, nil, false}, // unterminated quote
	}
	for _, c := range cases {
		got, ok := parseDroppedPaths(c.text)
		if ok != c.ok || strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("parseDroppedPaths(%q) = %v, %v; want %v, %v", c.text, got, ok, c.want, c.ok)
		}
	}
}

// fakeRun answers wl-paste/xclip calls from a table keyed by the joined
// command line.
func fakeRun(answers map[string][]byte, errs map[string]error) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		key := name + " " + strings.Join(args, " ")
		if err, ok := errs[key]; ok {
			return nil, err
		}
		return answers[key], nil
	}
}

func TestExecClipboard(t *testing.T) {
	wayland := func(string) string { return "" }
	wl := func(k string) string {
		if k == "WAYLAND_DISPLAY" {
			return "wayland-0"
		}
		return ""
	}
	x11 := func(k string) string {
		if k == "DISPLAY" {
			return ":0"
		}
		return ""
	}

	cb := execClipboard{getenv: wl, run: fakeRun(map[string][]byte{
		"wl-paste --list-types":                  []byte("text/plain\nimage/png\n"),
		"wl-paste --no-newline --type image/png": []byte("PNGDATA"),
	}, nil)}
	data, ext, err := cb.Image(context.Background())
	if err != nil || string(data) != "PNGDATA" || ext != ".png" {
		t.Fatalf("wayland image = %q, %q, %v", data, ext, err)
	}

	cb = execClipboard{getenv: x11, run: fakeRun(map[string][]byte{
		"xclip -selection clipboard -t TARGETS -o": []byte("UTF8_STRING\nTEXT\n"),
	}, nil)}
	if _, _, err := cb.Image(context.Background()); !errors.Is(err, errNoClipboardImage) {
		t.Fatalf("text-only clipboard = %v, want errNoClipboardImage", err)
	}

	cb = execClipboard{getenv: wl, run: fakeRun(nil, map[string]error{
		"wl-paste --list-types": &exec.Error{Name: "wl-paste", Err: exec.ErrNotFound},
	})}
	if _, _, err := cb.Image(context.Background()); err == nil || !strings.Contains(err.Error(), "falta wl-paste") {
		t.Fatalf("missing tool = %v, want a clear 'falta wl-paste' error", err)
	}

	cb = execClipboard{getenv: wayland, run: fakeRun(nil, nil)}
	if _, _, err := cb.Image(context.Background()); err == nil {
		t.Fatal("no display should be an error, not a silent no-op")
	}
}

type fakeClipboard struct {
	data []byte
	err  error
}

func (f fakeClipboard) Image(context.Context) ([]byte, string, error) { return f.data, ".png", f.err }

func TestChatDropPathsAttachAndSend(t *testing.T) {
	model, client := openedChat(t)
	file := tempFile(t, t.TempDir(), "foto.png")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(file), Paste: true})
	model = updated.(Model)
	if len(model.chatAttachments) != 1 || model.composer.Value() != "" {
		t.Fatalf("attachments = %v, draft = %q", model.chatAttachments, model.composer.Value())
	}
	if !strings.Contains(model.View(), "foto.png") {
		t.Fatal("attachment chip missing from the chat")
	}

	// A plain text paste still goes into the draft.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hola"), Paste: true})
	model = updated.(Model)
	if model.composer.Value() != "hola" {
		t.Fatalf("text paste = %q", model.composer.Value())
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	cmd()
	if len(client.calls) != 1 || !client.calls[0].dryRun || len(client.calls[0].attach) != 1 || client.calls[0].attach[0] != file {
		t.Fatalf("preview call = %+v, want a dry-run carrying the attachment", client.calls)
	}
}

func TestChatAttachmentOnlySendIsAllowed(t *testing.T) {
	model, client := openedChat(t)
	file := tempFile(t, t.TempDir(), "doc.pdf")
	model = model.addChatAttachments(file)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !updated.(Model).chatPreviewPending {
		t.Fatal("an attachment with an empty draft should still preview")
	}
	cmd()
	if len(client.calls) != 1 || client.calls[0].body != "" {
		t.Fatalf("calls = %+v", client.calls)
	}
}

func TestChatClipboardPasteAndBackspace(t *testing.T) {
	model, _ := openedChat(t)
	model.clipboard = fakeClipboard{data: []byte("PNGDATA")}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	model = updated.(Model)
	msg := cmd().(clipboardImageMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	info, err := os.Stat(msg.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("temp file %s: %v, mode %v; want private", msg.path, err, info.Mode())
	}
	updated, _ = model.Update(msg)
	model = updated.(Model)
	if len(model.chatAttachments) != 1 {
		t.Fatalf("pasted image not attached: %v", model.chatAttachments)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	model = updated.(Model)
	if len(model.chatAttachments) != 0 {
		t.Fatal("Backspace on an empty draft did not remove the attachment")
	}
	if _, err := os.Stat(msg.path); !os.IsNotExist(err) {
		t.Error("removing a pasted image should delete its temp file")
	}

	model.clipboard = fakeClipboard{err: errNoClipboardImage}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	updated, _ = updated.(Model).Update(cmd())
	if !strings.Contains(updated.(Model).View(), "no tiene una imagen") {
		t.Error("a clipboard without an image should say so")
	}
}

func TestLeavingChatDeletesPastedImages(t *testing.T) {
	model, _ := openedChat(t)
	model.clipboard = fakeClipboard{data: []byte("PNGDATA")}
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	msg := cmd().(clipboardImageMsg)
	updated, _ := model.Update(msg)
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(Model).chatMode {
		t.Fatal("Esc did not leave the chat")
	}
	if _, err := os.Stat(msg.path); !os.IsNotExist(err) {
		t.Error("leaving the chat should delete pasted temp images")
	}
}
