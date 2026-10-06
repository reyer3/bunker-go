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

// Leaving a chat while its sends are still being delivered would strand
// the queued text: Esc says so and stays until the queue drains.
func TestChatEscWaitsForPendingSends(t *testing.T) {
	client := &queueClient{rcpts: []string{"whatsapp:personal:91"}}
	model := queueChat(t, client)
	model, send := sendDraft(t, model, "uno")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if !model.chatMode || cmd != nil {
		t.Fatal("Esc left the chat while a send was pending")
	}
	if !strings.Contains(model.View(), "esperando") {
		t.Fatalf("view = %q, want a notice that the sends are pending", model.View())
	}
	if len(model.chatBubbles()) != 1 {
		t.Fatal("the pending bubble was dropped")
	}

	updated, _ = model.Update(send())
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(Model).chatMode {
		t.Fatal("Esc should leave once every send returned")
	}
}

// Ctrl+C with sends pending asks for a second Ctrl+C instead of quitting
// and losing the queued messages.
func TestChatCtrlCAsksTwiceWithPendingSends(t *testing.T) {
	model := queueChat(t, &queueClient{})
	model, _ = sendDraft(t, model, "uno")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("the first Ctrl+C quit with a send pending")
	}
	if !strings.Contains(model.View(), "Ctrl+C") {
		t.Fatalf("view = %q, want the second-Ctrl+C notice", model.View())
	}
	if _, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Fatal("the second Ctrl+C should quit anyway")
	}
}

// A forward leaves for another chat, so it waits for pending sends too.
func TestChatForwardWaitsForPendingSends(t *testing.T) {
	client := &queueClient{}
	client.threadItems = []core.Item{{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", Body: "hi", From: core.Address{Name: "Alice"}}}
	model := queueChat(t, client)
	model, _ = sendDraft(t, model, "uno")
	updated, _ := model.Update(keyAlt('f'))
	model = updated.(Model)
	if model.picker != nil || !model.chatMode {
		t.Fatal("a forward started while a send was pending")
	}
}

// Should anything else switch chats with sends pending, they keep being
// delivered in order, and a failure there keeps the text as that chat's
// draft instead of losing it.
func TestChatSendsKeepDeliveringAfterSwitchingChats(t *testing.T) {
	client := &queueClient{failAt: 1}
	model := queueChat(t, client)
	conv := model.chatConvKey()
	model, send := sendDraft(t, model, "uno")
	model, _ = sendDraft(t, model, "dos")

	model, _ = model.openChat(core.Item{ID: "whatsapp:personal:7", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "otro"})
	if model.chatPendingSends(conv) != 2 {
		t.Fatalf("pending sends after switching = %d, want 2", model.chatPendingSends(conv))
	}
	updated, next := model.Update(send())
	model = updated.(Model)
	if next != nil {
		t.Fatal("a failed send must stop its queue")
	}
	if got := model.drafts[conv]; got != "uno\ndos" {
		t.Fatalf("draft of the first chat = %q, want the unsent text kept", got)
	}
	if model.chatSendBusy() || len(model.chatBubbles()) != 0 {
		t.Fatal("the failed sends should be settled and not drawn in the other chat")
	}
}
