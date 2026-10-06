package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// queueClient is a replyClient whose real sends get one receipt ID per
// call, in order, and whose failAt-th real send (1-based) fails, so the
// send-queue tests can tell several in-flight messages apart.
type queueClient struct {
	replyClient
	rcpts  []string
	failAt int
	real   int
}

func (c *queueClient) Reply(ctx context.Context, id, body string, cc, attachments []string, dryRun bool) (core.Plan, core.Receipt, error) {
	plan, rcpt, err := c.replyClient.Reply(ctx, id, body, cc, attachments, dryRun)
	if dryRun {
		return plan, rcpt, err
	}
	c.real++
	if c.real == c.failAt {
		return core.Plan{}, core.Receipt{}, errors.New("sin conexión")
	}
	if c.real <= len(c.rcpts) {
		rcpt = core.Receipt{ID: c.rcpts[c.real-1]}
	}
	return plan, rcpt, err
}

// realBodies lists the bodies of the real (non-dry-run) sends, in order.
func (c *queueClient) realBodies() []string {
	var out []string
	for _, call := range c.calls {
		if !call.dryRun {
			out = append(out, call.body)
		}
	}
	return out
}

func queueChat(t *testing.T, client *queueClient) Model {
	t.Helper()
	client.previewOut = core.Plan{Recipients: []string{"alice"}}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.width, model.height = 60, 40
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	return updated.(Model)
}

// runMsgs runs cmd and returns every message it produced, unpacking a
// tea.Batch one level deep.
func runMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			if c != nil {
				out = append(out, c())
			}
		}
		return out
	}
	return []tea.Msg{msg}
}

// sendDraft types text and presses Enter on the one-Enter flow: the
// dry-run's reply sends it. It returns the command the preview's reply
// produced (the real send, or nil while an earlier send is in flight).
func sendDraft(t *testing.T, model Model, text string) (Model, tea.Cmd) {
	t.Helper()
	model = typeRunes(model, text)
	updated, preview := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if preview == nil {
		t.Fatalf("Enter on %q did not request the dry-run", text)
	}
	updated, send := model.Update(preview())
	return updated.(Model), send
}

func TestChatComposerAcceptsTypingWhileASendIsInFlight(t *testing.T) {
	client := &queueClient{}
	model := queueChat(t, client)
	model, send := sendDraft(t, model, "uno")
	if send == nil {
		t.Fatal("the first message did not start its send")
	}
	model = typeRunes(model, "dos")
	if got := model.composer.Value(); got != "dos" {
		t.Fatalf("composer while sending = %q, want the typed text accepted", got)
	}
}

func TestChatQueuedSendsShowBubblesAndDeliverInOrder(t *testing.T) {
	client := &queueClient{rcpts: []string{"whatsapp:personal:91", "whatsapp:personal:92"}}
	model := queueChat(t, client)
	model, send1 := sendDraft(t, model, "uno")
	if send1 == nil {
		t.Fatal("the first message did not start its send")
	}
	model, send2 := sendDraft(t, model, "dos")
	if send2 != nil {
		t.Fatal("the second send started while the first was still in flight")
	}
	if model.composer.Value() != "" {
		t.Fatalf("composer after the second confirm = %q, want cleared", model.composer.Value())
	}
	view := model.View()
	if !strings.Contains(view, "uno") || !strings.Contains(view, "dos") {
		t.Fatalf("view = %q, want both optimistic bubbles", view)
	}
	if got := strings.Count(view, "enviando…"); got != 2 {
		t.Fatalf("view has %d \"enviando…\" bubbles, want 2:\n%s", got, view)
	}

	msgs := runMsgs(send1)
	if got := client.realBodies(); len(got) != 1 || got[0] != "uno" {
		t.Fatalf("real sends = %q, want only the first", got)
	}
	updated, next := model.Update(msgs[0])
	model = updated.(Model)
	// The reload finds the first message stored but not yet the second:
	// only the first bubble is reconciled.
	client.threadItems = []core.Item{
		{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", Body: "hi", From: core.Address{Name: "Alice"}},
		{ID: "whatsapp:personal:91", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", FromMe: true, Body: "uno"},
	}
	var reload, sent2 tea.Msg
	for _, msg := range runMsgs(next) {
		switch msg.(type) {
		case chatSendReloadMsg:
			reload = msg
		case chatReplySentMsg:
			sent2 = msg
		}
	}
	if reload == nil || sent2 == nil {
		t.Fatal("the first receipt should reload the thread and start the second send")
	}
	if got := client.realBodies(); len(got) != 2 || got[1] != "dos" {
		t.Fatalf("real sends = %q, want uno then dos", got)
	}

	updated, _ = model.Update(reload)
	model = updated.(Model)
	view = model.View()
	if got := strings.Count(view, "uno"); got != 1 {
		t.Fatalf("\"uno\" shown %d times, want once (stored, not duplicated):\n%s", got, view)
	}
	if got := strings.Count(view, "enviando…"); got != 1 || !strings.Contains(view, "dos") {
		t.Fatalf("want only the second bubble still pending:\n%s", view)
	}

	updated, reload2 := model.Update(sent2)
	model = updated.(Model)
	client.threadItems = append(client.threadItems, core.Item{ID: "whatsapp:personal:92", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", FromMe: true, Body: "dos"})
	updated, _ = model.Update(reload2())
	model = updated.(Model)
	view = model.View()
	if strings.Contains(view, "enviando…") || strings.Count(view, "dos") != 1 {
		t.Fatalf("want both messages stored and no pending bubble:\n%s", view)
	}
	if model.chatSendBusy() {
		t.Fatal("still busy after every send returned")
	}
}

// A queued send carries its own attachments: the next message typed
// while it is in flight must not inherit them.
func TestChatQueuedSendKeepsItsOwnAttachments(t *testing.T) {
	client := &queueClient{}
	model := queueChat(t, client)
	path := writeTempFile(t, "foto.jpg", "img")
	model = model.addChatAttachments(path)
	model = typeRunes(model, "mira")
	updated, preview := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(preview())
	model = updated.(Model)
	if !model.chatConfirm {
		t.Fatal("an attachment should wait for the explicit confirm")
	}
	updated, send1 := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if send1 == nil || len(model.chatAttachments) != 0 {
		t.Fatalf("confirm should start the send and free the composer (attachments=%v)", model.chatAttachments)
	}
	model, send2 := sendDraft(t, model, "y esto")
	if send2 != nil {
		t.Fatal("the second send started while the first was still in flight")
	}
	updated, next := model.Update(send1())
	model = updated.(Model)
	for _, msg := range runMsgs(next) {
		updated, _ = model.Update(msg)
		model = updated.(Model)
	}
	var real []replyCall
	for _, c := range client.calls {
		if !c.dryRun {
			real = append(real, c)
		}
	}
	if len(real) != 2 || len(real[0].attach) != 1 || len(real[1].attach) != 0 {
		t.Fatalf("real sends = %+v, want the attachment only on the first", real)
	}
}

// lastBubble is the open chat's newest optimistic bubble, or nil.
func lastBubble(m Model) *chatOptimisticMsg {
	b := m.chatBubbles()
	if len(b) == 0 {
		return nil
	}
	return &b[len(b)-1]
}

// A failed send stops its conversation's queue: nothing behind it is
// sent out of order, every unsent bubble says "no enviado", and their
// text comes back into the composer ahead of what was typed since.
func TestChatFailedSendStopsTheQueueAndKeepsTheText(t *testing.T) {
	client := &queueClient{rcpts: []string{"whatsapp:personal:91"}, failAt: 2}
	model := queueChat(t, client)
	model, send1 := sendDraft(t, model, "uno")
	model, _ = sendDraft(t, model, "dos")
	model, _ = sendDraft(t, model, "tres")
	model = typeRunes(model, "cuatro")

	updated, next := model.Update(send1())
	model = updated.(Model)
	var sent2 tea.Msg
	for _, msg := range runMsgs(next) {
		if _, ok := msg.(chatReplySentMsg); ok {
			sent2 = msg
			continue
		}
		updated, _ = model.Update(msg)
		model = updated.(Model)
	}
	if sent2 == nil {
		t.Fatal("the second send did not start after the first")
	}
	updated, after := model.Update(sent2)
	model = updated.(Model)
	if after != nil {
		t.Fatal("a failed send must stop the queue and never auto-retry")
	}
	if got := client.realBodies(); len(got) != 2 || got[0] != "uno" || got[1] != "dos" {
		t.Fatalf("real sends = %q, want uno and dos only (tres never sent)", got)
	}
	if got := model.composer.Value(); got != "dos\ntres\ncuatro" {
		t.Fatalf("composer = %q, want the unsent messages back ahead of the new text", got)
	}
	view := model.View()
	if got := strings.Count(view, "no enviado"); got != 2 {
		t.Fatalf("view has %d \"no enviado\" bubbles, want 2:\n%s", got, view)
	}
	if model.chatSendBusy() || model.chatSendErr == nil {
		t.Fatalf("want the queue stopped with the error shown (busy=%v err=%v)", model.chatSendBusy(), model.chatSendErr)
	}

	// Sending the restored draft replaces the "no enviado" bubbles.
	updated, preview := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, send := model.Update(preview())
	model = updated.(Model)
	if send == nil || strings.Contains(model.View(), "no enviado") {
		t.Fatalf("resending should start at once and drop the failed bubbles:\n%s", model.View())
	}
}
