package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// replyClient is a fake Client that records every Reply call (dry-run
// preview and real send alike) so tests can assert the TUI never sends
// without an explicit confirm, never double-sends, and always carries a
// deadline.
type replyClient struct {
	inboxClient
	calls      []replyCall
	previewErr error
	previewOut core.Plan
	sendErr    error
	sendOut    core.Plan
	sendRcpt   core.Receipt
	block      chan struct{}
	// delay, when set, is a fixed pause before Read/Reply return. Unit
	// tests leave it zero (instant, deterministic single-goroutine
	// stepping). The teatest walkthroughs set it so a real Bubble Tea
	// program's transient "Loading.../Sending..." frame is reliably long
	// enough to be painted and captured every run, instead of racing the
	// scheduler on whether that frame gets flushed before the result
	// arrives (see teatest_walkthrough_test.go).
	delay time.Duration
}

type replyCall struct {
	id, body    string
	cc, attach  []string
	dryRun      bool
	hasDeadline bool
}

func (c *replyClient) Reply(ctx context.Context, id, body string, cc, attachments []string, dryRun bool) (core.Plan, core.Receipt, error) {
	_, hasDeadline := ctx.Deadline()
	c.calls = append(c.calls, replyCall{id: id, body: body, cc: cc, attach: attachments, dryRun: dryRun, hasDeadline: hasDeadline})
	if c.block != nil {
		<-c.block
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	if dryRun {
		return c.previewOut, core.Receipt{}, c.previewErr
	}
	return c.sendOut, c.sendRcpt, c.sendErr
}

// Read, List and Counts override the embedded inboxClient's versions only
// to honor delay; the embedded fake still supplies the actual result/error/
// call counting.
func (c *replyClient) Read(ctx context.Context, id string, receipt bool) (core.Item, error) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return c.inboxClient.Read(ctx, id, receipt)
}

func (c *replyClient) List(ctx context.Context, filter core.Filter) ([]core.Item, error) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return c.inboxClient.List(ctx, filter)
}

func (c *replyClient) Counts(ctx context.Context) (map[core.Channel]map[string]int, error) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return c.inboxClient.Counts(ctx)
}

func (c *replyClient) sendCalls() int {
	n := 0
	for _, call := range c.calls {
		if !call.dryRun {
			n++
		}
	}
	return n
}

func readyModel(client Client, id string) Model {
	model := NewModel(client)
	model.loaded = true
	// Real item ids are shaped "<channel>:<account>:<native>" (see
	// core.Item.ID); the fixed test id "mail:a:1" carries a real channel
	// so it survives the sectioned inbox's per-channel filtering.
	model.groups = []inboxGroup{{items: []core.Item{{ID: id, Channel: core.ChannelMail, Account: "a"}}}}
	return model
}

func typeRunes(model Model, text string) Model {
	for _, r := range text {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		model = updated.(Model)
	}
	return model
}

func TestReplyRequiresExplicitPreviewBeforeSend(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"alice@example.com"}}}
	model := readyModel(client, "mail:a:1")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	if cmd != nil || !model.composing {
		t.Fatal("r did not enter compose mode")
	}

	model = typeRunes(model, "hi there")
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if len(client.calls) != 0 || !strings.Contains(model.draftBody, "\n") {
		t.Fatalf("Enter while composing must insert a newline, not send: calls=%d body=%q", len(client.calls), model.draftBody)
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("ctrl+s did not request a preview")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if len(client.calls) != 1 || !client.calls[0].dryRun || !client.calls[0].hasDeadline {
		t.Fatalf("preview call = %+v, want exactly one dry-run call with a deadline", client.calls)
	}
	if model.composing || !model.previewing {
		t.Fatal("model did not transition to previewing after a successful dry-run")
	}
}

func TestReplyPreviewShowsRecipientAndConfirmSends(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{
		Channel: core.ChannelMail, Account: "work", Recipients: []string{"bob@example.com"},
	}}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = typeRunes(updated.(Model), "hello")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)

	view := model.View()
	if !strings.Contains(view, "bob@example.com") {
		t.Fatalf("preview view = %q, want the recipient visible", view)
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil || !model.sending {
		t.Fatal("Enter on preview did not start the real send")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if client.sendCalls() != 1 || client.calls[len(client.calls)-1].dryRun {
		t.Fatalf("send call missing or still dry-run: calls=%+v", client.calls)
	}
	if !client.calls[len(client.calls)-1].hasDeadline {
		t.Fatal("real send call had no deadline")
	}
	if model.composing || model.previewing || model.sending || model.draftBody != "" {
		t.Fatalf("model did not reset after a successful send: %+v", model)
	}
}

func TestReplyEscCancelsWithoutSendingAtAnyStage(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"a@b.c"}}}
	model := readyModel(client, "mail:a:1")

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = typeRunes(updated.(Model), "draft text")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.composing || model.draftBody != "" {
		t.Fatalf("esc from compose did not discard the draft: %+v", model)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = typeRunes(updated.(Model), "second draft")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)
	if !model.previewing {
		t.Fatal("preview did not arrive")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.previewing || !model.composing || model.draftBody != "second draft" {
		t.Fatalf("esc from preview must return to compose with the draft kept: %+v", model)
	}

	if client.sendCalls() != 0 {
		t.Fatalf("send calls = %d, want 0: esc must never send", client.sendCalls())
	}
}

func TestReplyNoDoubleSendWhileOneIsInFlight(t *testing.T) {
	client := &replyClient{
		previewOut: core.Plan{Recipients: []string{"a@b.c"}},
		block:      make(chan struct{}),
	}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = typeRunes(updated.(Model), "hi")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	client.block = nil // preview should not block
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)

	client.block = make(chan struct{})
	updated, sendCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if sendCmd == nil {
		t.Fatal("first Enter did not start the send")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- sendCmd() }()

	// The send command is now blocked inside Reply. Further Enter/any key
	// presses must not launch a second send while it is in flight.
	updated, again := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if again != nil {
		t.Fatal("second Enter launched another send while one was in flight")
	}
	close(client.block)
	<-done
	if client.sendCalls() != 1 {
		t.Fatalf("send calls = %d, want exactly 1", client.sendCalls())
	}
}

func TestReplyDraftKeptOnPreviewError(t *testing.T) {
	client := &replyClient{previewErr: errors.New("offline")}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = typeRunes(updated.(Model), "keep me")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)

	if !model.composing || model.previewing || model.draftBody != "keep me" {
		t.Fatalf("preview error must keep the draft and stay in compose: %+v", model)
	}
	if !strings.Contains(model.View(), "offline") {
		t.Fatalf("preview error not shown: %q", model.View())
	}
	if client.sendCalls() != 0 {
		t.Fatal("a preview error must never trigger a real send")
	}
}

func TestReplyDraftKeptOnSendErrorAndNoAutoRetry(t *testing.T) {
	client := &replyClient{
		previewOut: core.Plan{Recipients: []string{"a@b.c"}},
		sendErr:    errors.New("uncertain result"),
	}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = typeRunes(updated.(Model), "important text")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)

	updated, sendCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(sendCmd())
	model = updated.(Model)

	if model.draftBody != "important text" {
		t.Fatalf("send error wiped the draft: %+v", model)
	}
	if model.sending || model.previewing {
		t.Fatalf("send error left model mid-send/preview instead of back at compose: %+v", model)
	}
	if !model.composing {
		t.Fatal("send error must return to compose so a fresh preview is required before retrying")
	}
	if !strings.Contains(model.View(), "uncertain result") {
		t.Fatalf("send error not shown: %q", model.View())
	}
	if client.sendCalls() != 1 {
		t.Fatalf("send calls = %d, want exactly 1 (no auto-retry)", client.sendCalls())
	}
}

func TestReplyEditAfterPreviewRequiresFreshPreviewBeforeSend(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"a@b.c"}}}
	model := readyModel(client, "mail:a:1")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = typeRunes(updated.(Model), "draft one")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = typeRunes(updated.(Model), " edited")
	if model.previewing {
		t.Fatal("editing after esc must not leave the stale preview active")
	}
	updated, enterCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if enterCmd != nil || client.sendCalls() != 0 {
		t.Fatal("Enter while composing must never send directly, even after a prior preview")
	}
	if model.draftBody != "draft one edited\n" {
		t.Fatalf("draft body = %q", model.draftBody)
	}
}

func TestReplyTargetsSelectedItemOrOpenDetail(t *testing.T) {
	client := &replyClient{}
	model := NewModel(client)
	model.loaded = true
	model.groups = []inboxGroup{{items: []core.Item{{ID: "first", Channel: core.ChannelMail}}}, {items: []core.Item{{ID: "second", Channel: core.ChannelMail}}}}
	model.selected = 1

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	if model.draftID != "second" {
		t.Fatalf("reply target from list = %q, want the selected item", model.draftID)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)

	model.detail = true
	model.readItem = core.Item{ID: "detail-item"}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	if model.draftID != "detail-item" {
		t.Fatalf("reply target from detail = %q, want the open item", model.draftID)
	}
}
