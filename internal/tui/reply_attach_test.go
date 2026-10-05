package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

func writeTempFile(t *testing.T, name string, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func attachPath(model Model, path string) Model {
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	model = updated.(Model)
	model = typeRunes(model, path)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return updated.(Model)
}

func TestAttachAddsValidatedPathAndShowsNameAndSize(t *testing.T) {
	path := writeTempFile(t, "report card.pdf", "hello world")
	client := &replyClient{}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = attachPath(updated.(Model), path)

	if len(model.attachments) != 1 || model.attachments[0] != path {
		t.Fatalf("attachments = %+v, want %q", model.attachments, path)
	}
	if model.attaching {
		t.Fatal("attach input did not close after Enter")
	}
	view := model.View()
	if !strings.Contains(view, "report card.pdf") || !strings.Contains(view, "11 bytes") {
		t.Fatalf("compose view = %q, want the attachment name and size", view)
	}
}

// TestAttachChipsRenderInlineOnOneLine pins the "attachments as chips"
// decision (conversation-view.md): several attachments must render as
// bracketed inline tags on one line, not one bulleted "- name (size)\n"
// row per attachment as before K4.
func TestAttachChipsRenderInlineOnOneLine(t *testing.T) {
	pathA := writeTempFile(t, "a.txt", "abc")
	pathB := writeTempFile(t, "b.txt", "wxyz")
	client := &replyClient{}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = attachPath(updated.(Model), pathA)
	model = attachPath(model, pathB)

	view := model.View()
	if !strings.Contains(view, "[a.txt (3 bytes)] [b.txt (4 bytes)]") {
		t.Fatalf("compose view = %q, want both attachments as one line of chips", view)
	}
	if strings.Contains(view, "- a.txt") || strings.Contains(view, "- b.txt") {
		t.Fatalf("compose view = %q, still shows the old bulleted-list format", view)
	}
}

func TestAttachRejectsMissingPathAndShowsVisibleError(t *testing.T) {
	client := &replyClient{}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	missing := filepath.Join(t.TempDir(), "does-not-exist.pdf")
	model = attachPath(updated.(Model), missing)

	if len(model.attachments) != 0 {
		t.Fatalf("attachments = %+v, want none for a missing path", model.attachments)
	}
	if model.replyErr == nil {
		t.Fatal("missing attachment path did not set a visible error")
	}
	if !strings.Contains(model.View(), "does-not-exist.pdf") {
		t.Fatalf("compose view = %q, want the failing path named", model.View())
	}
}

func TestAttachRemovedWithCtrlX(t *testing.T) {
	first := writeTempFile(t, "one.txt", "a")
	second := writeTempFile(t, "two.txt", "b")
	client := &replyClient{}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = attachPath(updated.(Model), first)
	model = attachPath(model, second)
	if len(model.attachments) != 2 {
		t.Fatalf("attachments = %+v, want 2 before removal", model.attachments)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	model = updated.(Model)
	if len(model.attachments) != 1 || model.attachments[0] != first {
		t.Fatalf("attachments after one removal = %+v, want only %q", model.attachments, first)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	model = updated.(Model)
	if len(model.attachments) != 0 {
		t.Fatalf("attachments after second removal = %+v, want none", model.attachments)
	}
}

func TestAttachPathsFlowThroughPreviewAndSend(t *testing.T) {
	path := writeTempFile(t, "invoice.pdf", "data")
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"a@b.c"}}}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = attachPath(updated.(Model), path)
	model = typeRunes(model, "see attached")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)
	if len(client.calls) != 1 || len(client.calls[0].attach) != 1 || client.calls[0].attach[0] != path {
		t.Fatalf("preview call attachments = %+v, want [%q]", client.calls, path)
	}

	updated, sendCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = updated.(Model).Update(sendCmd())
	model = updated.(Model)
	last := client.calls[len(client.calls)-1]
	if last.dryRun || len(last.attach) != 1 || last.attach[0] != path {
		t.Fatalf("send call = %+v, want a real send carrying [%q]", last, path)
	}
	if len(model.attachments) != 0 {
		t.Fatalf("attachments after a successful send = %+v, want none (cleared)", model.attachments)
	}
}

func TestAttachRevalidatedBeforePreviewBlocksMissingFile(t *testing.T) {
	path := writeTempFile(t, "gone.pdf", "x")
	client := &replyClient{}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = attachPath(updated.(Model), path)

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove temp file: %v", err)
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	model = updated.(Model)
	if cmd != nil || len(client.calls) != 0 {
		t.Fatalf("preview proceeded with a missing attachment: cmd=%v calls=%d", cmd, len(client.calls))
	}
	if model.replyErr == nil || !model.composing {
		t.Fatalf("missing attachment did not block preview visibly: %+v", model)
	}
}

func TestAttachRevalidatedBeforeSendBlocksFileRemovedAfterPreview(t *testing.T) {
	path := writeTempFile(t, "here-then-gone.pdf", "x")
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"a@b.c"}}}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = attachPath(updated.(Model), path)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)
	if !model.previewing {
		t.Fatal("preview did not arrive")
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove temp file: %v", err)
	}
	updated, sendCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if sendCmd != nil || client.sendCalls() != 0 {
		t.Fatalf("send proceeded with a missing attachment: cmd=%v sendCalls=%d", sendCmd, client.sendCalls())
	}
	if model.replyErr == nil || !model.previewing || model.sending {
		t.Fatalf("missing attachment did not block send visibly: %+v", model)
	}
}

func TestAttachDiscardedOnEscFromCompose(t *testing.T) {
	path := writeTempFile(t, "keep-out.pdf", "x")
	client := &replyClient{}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = attachPath(updated.(Model), path)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if len(model.attachments) != 0 || model.attaching {
		t.Fatalf("esc from compose kept attachment state: %+v", model)
	}
}

func TestAttachDoesNotCarryOverToNextDraft(t *testing.T) {
	path := writeTempFile(t, "old-draft.pdf", "x")
	client := &replyClient{}
	model := NewModel(client)
	model.loaded = true
	// Distinct senders (mail-sender-groups.md merges by From address), both
	// pre-expanded, so each item still gets its own selectable thread row.
	model.groups = []inboxGroup{
		{items: []core.Item{{ID: "first", Channel: core.ChannelMail, From: core.Address{ID: "first@example.com"}}}},
		{items: []core.Item{{ID: "second", Channel: core.ChannelMail, From: core.Address{ID: "second@example.com"}}}},
	}
	model = model.setSenderExpanded(senderKey(model.groups[0].items[0]), true)
	model = model.setSenderExpanded(senderKey(model.groups[1].items[0]), true)
	model.selected = 1 // rows: sender(first), thread(first), sender(second), thread(second)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = attachPath(updated.(Model), path)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	model.selected = 3
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	if len(model.attachments) != 0 {
		t.Fatalf("new draft inherited a stale attachment list: %+v", model.attachments)
	}
}
