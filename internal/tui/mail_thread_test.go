package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// mailThreadReadyModel builds a Model with one loaded mail group ready to
// open, so Enter routes into the K6 mail thread view.
func mailThreadReadyModel(client Client, id string) Model {
	model := NewModel(client).withGlyphs(nil)
	model.width = 60
	model.height = 20
	model.loaded = true
	item := core.Item{
		ID: id, Channel: core.ChannelMail, Account: "cl", Thread: "t1",
		From:    core.Address{ID: "bob@example.com", Name: "Bob"},
		To:      []core.Address{{ID: "alice@example.com", Name: "Alice"}},
		Subject: "hola", Unread: true,
	}
	model.groups = []inboxGroup{{items: []core.Item{item}}}
	// Mail wraps this conversation under a collapsible sender row
	// (mail-sender-groups.md); expand it and select the nested thread row
	// so openThread's Enter opens the thread view instead of toggling the
	// sender, matching this fixture's name (already "ready to open").
	model = model.setSenderExpanded(senderKey(item), true)
	model.selected = 1
	return model
}

func openThread(model Model) (Model, tea.Cmd) {
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return updated.(Model), cmd
}

// TestOpeningMailItemEntersThreadModeAndMarksSeenImmediately pins K9's fix
// (conversation-view.md): opening a mail thread must call ReadThread with
// the thread's own (channel, account, thread) and receipt=true (mail's
// \Seen semantics) — marking the WHOLE conversation \Seen, not just the
// opened item's single id.
func TestOpeningMailItemEntersThreadModeAndMarksSeenImmediately(t *testing.T) {
	client := &replyClient{}
	model := mailThreadReadyModel(client, "mail:cl:1")
	model, cmd := openThread(model)
	if cmd == nil {
		t.Fatal("Enter on a mail item returned no command")
	}
	if !model.detail || !model.threadMode {
		t.Fatal("Enter on a mail item did not enter thread mode")
	}
	updated, _ := model.Update(cmd())
	model = updated.(Model)

	for _, call := range client.organizeCalls {
		if call.id == "mail:cl:1" && call.op.Seen != nil && *call.op.Seen {
			t.Fatalf("Organize(Seen=true) was called on the single item, want ReadThread instead: %+v", client.organizeCalls)
		}
	}
	if len(client.readThreadCalls) != 1 {
		t.Fatalf("readThreadCalls = %+v, want exactly 1", client.readThreadCalls)
	}
	rt := client.readThreadCalls[0]
	if rt.channel != core.ChannelMail || rt.account != "cl" || rt.thread != "t1" || !rt.receipt {
		t.Fatalf("readThreadCalls[0] = %+v, want the thread's own (mail, cl, t1) with receipt=true", rt)
	}
	if len(client.threadCalls) != 1 {
		t.Fatalf("thread calls = %d, want 1", len(client.threadCalls))
	}
}

func TestThreadViewShowsNewestExpandedAndOlderCollapsed(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	client := &replyClient{}
	model := mailThreadReadyModel(client, "mail:cl:2")
	model.now = func() time.Time { return now }
	model, cmd := openThread(model)
	msg := cmd().(threadLoadedMsg)
	msg.items = []core.Item{
		{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{Name: "Bob"}, Subject: "hola", Body: "primer mensaje", Timestamp: now.Add(-time.Hour)},
		{ID: "mail:cl:2", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{Name: "Alice"}, To: []core.Address{{Name: "Bob", ID: "bob@example.com"}}, Subject: "Re: hola", Body: "segundo mensaje", Timestamp: now},
	}
	updated, _ := model.Update(msg)
	model = updated.(Model)

	view := model.View()
	if !strings.Contains(view, "To:") {
		t.Fatalf("thread view = %q, want the newest item's full headers (To:)", view)
	}
	if !strings.Contains(view, "segundo mensaje") {
		t.Fatalf("thread view = %q, want the newest item's body shown expanded", view)
	}
	// The collapsed row is "sender · date · snippet" on one line (the
	// snippet is allowed to repeat body text, truncated); what must NOT
	// appear for it is the expanded view's full headers.
	if strings.Contains(view, "Date: 26-09-2026 11:00") {
		t.Fatalf("thread view = %q, the older item must start collapsed, not with full headers", view)
	}
	if !strings.Contains(view, "Bob · ") {
		t.Fatalf("thread view = %q, want the older item's collapsed \"sender · date · snippet\" line", view)
	}

	// Enter toggles the selected (older, index 0) item open.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if !strings.Contains(model.View(), "primer mensaje") {
		t.Fatalf("thread view after Enter = %q, want the older item expanded", model.View())
	}
}

func threadReadyWithItems(client Client, id string, items []core.Item) Model {
	model := mailThreadReadyModel(client, id)
	model, _ = openThread(model)
	model.threadItems = items
	model.threadExpanded = map[int]bool{len(items) - 1: true}
	model.threadSelected = len(items) - 1
	return model
}

func TestThreadReplyPrefillsOriginalSenderAndQuotesBody(t *testing.T) {
	client := &replyClient{}
	items := []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
		From: core.Address{ID: "bob@example.com", Name: "Bob"}, Subject: "hola", Body: "hello there",
	}}
	model := threadReadyWithItems(client, "mail:cl:1", items)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	if !model.mailComposing || model.mailAction != "reply" {
		t.Fatalf("r did not open the reply editor: composing=%v action=%q", model.mailComposing, model.mailAction)
	}
	if model.mailTo.Value() != "bob@example.com" {
		t.Fatalf("To = %q, want the original sender", model.mailTo.Value())
	}
	if !strings.HasPrefix(model.mailSubject.Value(), "Re: ") {
		t.Fatalf("Subject = %q, want a Re: prefix", model.mailSubject.Value())
	}
	if !strings.Contains(model.composer.Value(), "hello there") {
		t.Fatalf("body = %q, want the original quoted", model.composer.Value())
	}
}

func TestThreadReplyAllExcludesOwnAddress(t *testing.T) {
	client := &replyClient{}
	items := []core.Item{
		{ID: "mail:cl:0", Channel: core.ChannelMail, Account: "cl", Thread: "t1", FromMe: true, From: core.Address{ID: "me@example.com"}},
		{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
			From:    core.Address{ID: "bob@example.com", Name: "Bob"},
			To:      []core.Address{{ID: "me@example.com"}, {ID: "carol@example.com"}},
			Subject: "hola", Body: "hi"},
	}
	model := threadReadyWithItems(client, "mail:cl:1", items)
	model.threadSelected = 1

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	model = updated.(Model)
	to := model.mailTo.Value()
	if strings.Contains(to, "me@example.com") {
		t.Fatalf("To = %q, must exclude our own address", to)
	}
	if !strings.Contains(to, "bob@example.com") || !strings.Contains(to, "carol@example.com") {
		t.Fatalf("To = %q, want every other participant", to)
	}
}

func TestThreadForwardStartsWithEmptyToAndListsAttachments(t *testing.T) {
	client := &replyClient{}
	items := []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
		From: core.Address{ID: "bob@example.com", Name: "Bob"}, Subject: "hola", Body: "hi",
		Attachments: []core.Attachment{{Name: "plan.pdf", Size: 100}},
	}}
	model := threadReadyWithItems(client, "mail:cl:1", items)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	model = updated.(Model)
	if model.mailTo.Value() != "" {
		t.Fatalf("forward To = %q, want empty (the user fills it in)", model.mailTo.Value())
	}
	if !strings.HasPrefix(model.mailSubject.Value(), "Fwd: ") {
		t.Fatalf("Subject = %q, want a Fwd: prefix", model.mailSubject.Value())
	}
	if !strings.Contains(model.View(), "plan.pdf") {
		t.Fatalf("view = %q, want the original attachment listed", model.View())
	}
}

func TestMailEditorPreviewThenConfirmSends(t *testing.T) {
	client := &replyClient{outgoingPreview: core.Plan{Recipients: []string{"bob@example.com"}}}
	items := []core.Item{{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{ID: "bob@example.com"}, Subject: "hola"}}
	model := threadReadyWithItems(client, "mail:cl:1", items)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	model = typeRunes(model, "thanks")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("ctrl+s did not request a preview")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if !model.mailPreviewing || len(client.outgoingCalls) != 1 || !client.outgoingCalls[0].dryRun {
		t.Fatalf("expected a dry-run preview: previewing=%v calls=%+v", model.mailPreviewing, client.outgoingCalls)
	}
	if client.outgoingCalls[0].out.To[0] != "bob@example.com" {
		t.Fatalf("outgoing To = %+v, want the editor's To field", client.outgoingCalls[0].out.To)
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("confirming Enter did not send")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if len(client.outgoingCalls) != 2 || client.outgoingCalls[1].dryRun {
		t.Fatalf("outgoing calls = %+v, want a second real send", client.outgoingCalls)
	}
	if model.mailComposing {
		t.Fatal("editor did not close after a successful send")
	}
}

func TestMailEditorEscFromPreviewReturnsToEditingWithoutSending(t *testing.T) {
	client := &replyClient{outgoingPreview: core.Plan{Recipients: []string{"bob@example.com"}}}
	items := []core.Item{{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{ID: "bob@example.com"}}}
	model := threadReadyWithItems(client, "mail:cl:1", items)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	model = typeRunes(model, "thanks")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.mailPreviewing || !model.mailComposing {
		t.Fatal("esc from preview must return to editing, not close the editor")
	}
	sends := 0
	for _, c := range client.outgoingCalls {
		if !c.dryRun {
			sends++
		}
	}
	if sends != 0 {
		t.Fatalf("send calls = %d, want 0: esc must never send", sends)
	}
}

func TestMailEditorNoDoubleSendWhileOneIsInFlight(t *testing.T) {
	client := &replyClient{outgoingPreview: core.Plan{Recipients: []string{"bob@example.com"}}}
	items := []core.Item{{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{ID: "bob@example.com"}}}
	model := threadReadyWithItems(client, "mail:cl:1", items)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	model = typeRunes(model, "thanks")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	client.block = make(chan struct{})
	updated, sendCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if sendCmd == nil {
		t.Fatal("first Enter did not start the send")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- sendCmd() }()

	updated, again := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if again != nil {
		t.Fatal("second Enter launched another send while one was in flight")
	}
	if !model.mailSending {
		t.Fatal("second Enter cleared the mailSending guard; the in-flight send should still hold it")
	}
	close(client.block)
	<-done
	sends := 0
	for _, c := range client.outgoingCalls {
		if !c.dryRun {
			sends++
		}
	}
	if sends != 1 {
		t.Fatalf("send calls = %d, want exactly 1", sends)
	}
}
