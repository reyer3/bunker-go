package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// markClient is a fake Client that records every Organize call so tests can
// assert the TUI always previews (dry-run) before an explicit confirm, and
// never organizes twice for one confirm.
type markClient struct {
	inboxClient
	calls       []organizeCall
	previewErr  error
	previewOut  core.Plan
	organizeErr error
	organizeOut core.Plan
	block       chan struct{}
}

type organizeCall struct {
	id          string
	seen        *bool
	dryRun      bool
	hasDeadline bool
}

func (c *markClient) Organize(ctx context.Context, id string, op core.OrganizeOp, dryRun bool) (core.Plan, error) {
	_, hasDeadline := ctx.Deadline()
	c.calls = append(c.calls, organizeCall{id: id, seen: op.Seen, dryRun: dryRun, hasDeadline: hasDeadline})
	if c.block != nil {
		<-c.block
	}
	if dryRun {
		return c.previewOut, c.previewErr
	}
	return c.organizeOut, c.organizeErr
}

func (c *markClient) realCalls() int {
	n := 0
	for _, call := range c.calls {
		if !call.dryRun {
			n++
		}
	}
	return n
}

func TestMarkReadRequiresDryRunPreviewBeforeConfirm(t *testing.T) {
	client := &markClient{previewOut: core.Plan{Channel: core.ChannelMail, Account: "work"}}
	model := readyModel(client, "mail:a:1")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	model = updated.(Model)
	if cmd == nil || !model.marking || !model.markLoading {
		t.Fatal("m did not start a dry-run preview")
	}
	if len(client.calls) != 0 {
		t.Fatal("preview command ran synchronously inside Update")
	}

	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if len(client.calls) != 1 || !client.calls[0].dryRun || client.calls[0].seen == nil || !*client.calls[0].seen {
		t.Fatalf("preview call = %+v, want exactly one dry-run Organize(Seen=true)", client.calls)
	}
	if !client.calls[0].hasDeadline {
		t.Fatal("preview call had no deadline")
	}
	if !model.markConfirm || model.markLoading {
		t.Fatal("model did not reach the confirm stage after a successful dry-run")
	}
	if client.realCalls() != 0 {
		t.Fatal("dry-run preview must never organize for real")
	}
}

func TestMarkReadConfirmOrganizesAndOpeningNeverDoes(t *testing.T) {
	client := &markClient{previewOut: core.Plan{Channel: core.ChannelMail, Account: "work"}}
	model := readyModel(client, "mail:a:1")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)

	updated, confirmCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if confirmCmd == nil || !model.markSending {
		t.Fatal("Enter at confirm did not start the real Organize")
	}
	updated, _ = model.Update(confirmCmd())
	model = updated.(Model)
	if client.realCalls() != 1 {
		t.Fatalf("real Organize calls = %d, want 1", client.realCalls())
	}
	last := client.calls[len(client.calls)-1]
	if last.seen == nil || !*last.seen || !last.hasDeadline {
		t.Fatalf("real Organize call = %+v, want Seen=true with a deadline", last)
	}
	if model.marking {
		t.Fatal("model did not exit the mark-read flow after success")
	}

	// Opening a WhatsApp item (K5's chat view) marks it read via Read's
	// receipt, never Organize — Organize is K6's mail-\Seen-on-open
	// mechanism specifically, exercised above via the explicit "m" flow.
	// (A mail item's Enter now also marks \Seen via Organize on open by
	// design — see conversation-view.md's Decisions — so this invariant
	// is checked on a channel where Organize is never involved at all.)
	model.groups = []inboxGroup{{items: []core.Item{{ID: "whatsapp:a:1", Channel: core.ChannelWhatsApp, Account: "a", Thread: "t"}}}}
	// WhatsApp is never sender-wrapped (Mail only): its one thread is
	// row 0, not the row 1 readyModel's Mail fixture selected.
	model.selected = 0
	client.calls = nil
	updated, readCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if readCmd == nil {
		t.Fatal("Enter did not open the item")
	}
	updated, _ = model.Update(readCmd())
	_ = updated.(Model)
	if len(client.calls) != 0 {
		t.Fatalf("opening called Organize: %+v", client.calls)
	}
}

func TestMarkReadEscCancelsAtEveryStageWithoutOrganizing(t *testing.T) {
	client := &markClient{previewOut: core.Plan{}}
	model := readyModel(client, "mail:a:1")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.marking {
		t.Fatal("esc while loading did not cancel the mark-read flow")
	}
	// The in-flight preview result must be discarded, not applied late.
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if model.marking || model.markConfirm {
		t.Fatal("a stale preview result revived a cancelled mark-read flow")
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)
	if !model.markConfirm {
		t.Fatal("second attempt did not reach confirm")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.marking {
		t.Fatal("esc at confirm did not cancel")
	}
	if client.realCalls() != 0 {
		t.Fatalf("esc must never organize for real: calls=%+v", client.calls)
	}
}

func TestMarkReadNoDoubleOrganizeWhileOneIsInFlight(t *testing.T) {
	client := &markClient{previewOut: core.Plan{}}
	model := readyModel(client, "mail:a:1")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)

	client.block = make(chan struct{})
	updated, confirmCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	done := make(chan tea.Msg, 1)
	go func() { done <- confirmCmd() }()

	updated, again := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if again != nil {
		t.Fatal("second Enter launched another Organize while one was in flight")
	}
	if !model.markSending {
		t.Fatal("second Enter cleared the markSending guard; the in-flight Organize should still hold it")
	}
	close(client.block)
	<-done
	if client.realCalls() != 1 {
		t.Fatalf("real Organize calls = %d, want exactly 1", client.realCalls())
	}
}

func TestMarkReadIgnoresEscWhileOrganizeInFlight(t *testing.T) {
	client := &markClient{previewOut: core.Plan{}}
	model := readyModel(client, "mail:a:1")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)

	client.block = make(chan struct{})
	updated, confirmCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	done := make(chan tea.Msg, 1)
	go func() { done <- confirmCmd() }()

	updated, escCmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if escCmd != nil || !model.marking || !model.markSending {
		t.Fatal("esc must be ignored while an Organize confirm is in flight")
	}
	close(client.block)
	msg := <-done
	updated, _ = model.Update(msg)
	model = updated.(Model)
	if model.marking {
		t.Fatal("model did not settle back to idle once the in-flight Organize completed")
	}
	if client.realCalls() != 1 {
		t.Fatalf("real Organize calls = %d, want exactly 1", client.realCalls())
	}
}

func TestMarkReadShowsErrorAndNeverAutoRetries(t *testing.T) {
	client := &markClient{previewOut: core.Plan{}, organizeErr: errors.New("uncertain result")}
	model := readyModel(client, "mail:a:1")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)

	updated, confirmCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = updated.(Model).Update(confirmCmd())
	model = updated.(Model)

	if !model.marking || model.markSending || model.markConfirm {
		t.Fatalf("send error left an unexpected state: %+v", model)
	}
	if !strings.Contains(model.View(), "uncertain result") {
		t.Fatalf("send error not visible: %q", model.View())
	}
	// No key besides Esc should be able to organize again without a fresh
	// dry-run preview.
	updated, retry := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if retry != nil || client.realCalls() != 1 {
		t.Fatalf("auto-retry occurred: cmd=%v realCalls=%d", retry, client.realCalls())
	}
}

func TestMarkReadDryRunErrorNeverOrganizesForReal(t *testing.T) {
	client := &markClient{previewErr: errors.New("offline")}
	model := readyModel(client, "mail:a:1")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	updated, _ = updated.(Model).Update(cmd())
	model = updated.(Model)

	if !model.marking || model.markConfirm {
		t.Fatal("dry-run error must not reach the confirm stage")
	}
	if !strings.Contains(model.View(), "offline") {
		t.Fatalf("dry-run error not visible: %q", model.View())
	}
	updated, blocked := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if blocked != nil || client.realCalls() != 0 {
		t.Fatal("Enter after a dry-run error must not organize for real")
	}
}
