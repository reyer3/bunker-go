package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// chatReadyModel builds a Model with one loaded WhatsApp group ready to
// open, matching readyModel's mail fixture but on a chat channel so Enter
// routes into the K5 chat view instead of a plain detail/mail thread.
func chatReadyModel(client Client, id string) Model {
	model := NewModel(client).withGlyphs(nil)
	model.width = 40
	model.height = 20
	model.loaded = true
	model.groups = []inboxGroup{{items: []core.Item{{
		ID: id, Channel: core.ChannelWhatsApp, Account: "personal", Thread: "5511999999999@s.whatsapp.net",
		From: core.Address{ID: "5511999999999@s.whatsapp.net", Name: "Alice"}, Unread: true,
	}}}}
	return model
}

func openChat(model Model) (Model, tea.Cmd) {
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return updated.(Model), cmd
}

// TestOpeningWhatsAppItemEntersChatModeAndMarksRead pins K9's fix
// (conversation-view.md): opening a chat must call ReadThread with the
// chat's own (channel, account, thread) and receipt=true — marking the
// WHOLE conversation read, not just the opened item's single id (the live
// bug: Read(id, true) on a single id left every other unread item
// stranded) — and it must load the conversation via Thread rather than
// only showing the one already-loaded item.
func TestOpeningWhatsAppItemEntersChatModeAndMarksRead(t *testing.T) {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	if cmd == nil {
		t.Fatal("Enter on a WhatsApp item returned no command")
	}
	if !model.detail || !model.chatMode {
		t.Fatal("Enter on a WhatsApp item did not enter chat mode")
	}
	updated, _ := model.Update(cmd())
	model = updated.(Model)

	if client.readCalls != 0 {
		t.Fatalf("Read (single-item) calls = %d, want 0: opening a chat must use ReadThread", client.readCalls)
	}
	if len(client.readThreadCalls) != 1 {
		t.Fatalf("readThreadCalls = %+v, want exactly 1", client.readThreadCalls)
	}
	rt := client.readThreadCalls[0]
	if rt.channel != core.ChannelWhatsApp || rt.account != "personal" || rt.thread != "5511999999999@s.whatsapp.net" || !rt.receipt {
		t.Fatalf("readThreadCalls[0] = %+v, want the chat's own thread with receipt=true", rt)
	}
	if len(client.threadCalls) != 1 {
		t.Fatalf("thread calls = %d, want 1", len(client.threadCalls))
	}
	call := client.threadCalls[0]
	if call.channel != core.ChannelWhatsApp || call.account != "personal" || call.thread != "5511999999999@s.whatsapp.net" || !call.before.IsZero() {
		t.Fatalf("thread call = %+v, want the newest page of this conversation", call)
	}
	if client.presenceCalls != 1 {
		t.Fatalf("presence calls = %d, want 1", client.presenceCalls)
	}
}

// TestChatViewRendersOwnAndOtherBubblesWithDaySeparator checks the visual
// contract: own messages align right, others align left, a day separator
// appears once per calendar day, and attachments show as "📎 name (size)".
func TestChatViewRendersOwnAndOtherBubblesWithDaySeparator(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.now = func() time.Time { return now }
	model, cmd := openChat(model)
	msg := cmd().(chatThreadLoadedMsg)
	msg.items = []core.Item{
		{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", FromMe: false, From: core.Address{Name: "Alice"}, Body: "hola", Timestamp: now.AddDate(0, 0, -1)},
		{ID: "whatsapp:personal:2", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", FromMe: true, Body: "hey", Timestamp: now, Attachments: []core.Attachment{{Name: "photo.jpg", Size: 2048}}},
	}
	updated, _ := model.Update(msg)
	model = updated.(Model)

	view := model.View()
	if !strings.Contains(view, "ayer") {
		t.Fatalf("chat view = %q, want a day separator for yesterday's message", view)
	}
	if !strings.Contains(view, "hoy") {
		t.Fatalf("chat view = %q, want a day separator for today's message", view)
	}
	if !strings.Contains(view, "Alice") {
		t.Fatalf("chat view = %q, want the other party's name on their bubble", view)
	}
	if !strings.Contains(view, "📎 photo.jpg (2048 bytes)") {
		t.Fatalf("chat view = %q, want the attachment line", view)
	}
}

// TestChatEnterShowsInlineConfirmBeforeSending pins the "never send
// without explicit confirmation" rule for the chat view specifically:
// Enter must preview first and show "¿Enviar a ...? Enter/Esc", a second
// Enter sends for real, and Esc cancels without ever sending.
func TestChatEnterShowsInlineConfirmBeforeSending(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"5511999999999@s.whatsapp.net"}}}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.confirmChatSend = true // these pin the explicit two-step flow
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)

	model = typeRunes(model, "hola")
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter with draft text did not request a preview")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if !model.chatConfirm || len(client.calls) != 1 || !client.calls[0].dryRun {
		t.Fatalf("expected an inline confirm after exactly one dry-run preview, chatConfirm=%v calls=%+v", model.chatConfirm, client.calls)
	}
	// The confirm text is long enough that a 40-column width wraps it
	// across lines (correctly, per fitInbox's "every line fits the
	// width"); join lines with a space so this checks its content, not
	// its exact wrap point.
	unwrapped := strings.Join(strings.Fields(model.View()), " ")
	if !strings.Contains(unwrapped, "Enviar a 5511999999999@s.whatsapp.net? ↵ enviar · Esc cancelar") {
		t.Fatalf("chat view = %q, want the inline confirm text", model.View())
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("confirming Enter did not send")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if len(client.calls) != 2 || client.calls[1].dryRun {
		t.Fatalf("calls = %+v, want a second, real (non-dry-run) send", client.calls)
	}
}

// TestChatNoDoubleSendWhileOneIsInFlight mirrors
// TestReplyNoDoubleSendWhileOneIsInFlight (mail) for the chat send path:
// a second Enter while a real send is blocked in flight must not launch
// another one.
func TestChatNoDoubleSendWhileOneIsInFlight(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"alice"}}}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.confirmChatSend = true // these pin the explicit two-step flow
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model = typeRunes(model, "hola")
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if !model.chatConfirm {
		t.Fatal("expected an inline confirm before sending")
	}

	client.block = make(chan struct{})
	updated, sendCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if sendCmd == nil {
		t.Fatal("confirming Enter did not start the send")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- sendCmd() }()

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

func TestChatEscCancelsConfirmWithoutSending(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"alice"}}}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.confirmChatSend = true // these pin the explicit two-step flow
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model = typeRunes(model, "hola")
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if !model.chatConfirm {
		t.Fatal("expected an inline confirm")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.chatConfirm || len(client.calls) != 1 {
		t.Fatalf("Esc must cancel without a real send: chatConfirm=%v calls=%+v", model.chatConfirm, client.calls)
	}
	if model.composer.Value() != "hola" {
		t.Fatalf("draft after Esc = %q, want it kept for editing", model.composer.Value())
	}
}

// TestChatAltEnterInsertsNewlineInstead ensures Alt+Enter, not plain
// Enter, is the chat composer's newline key (Enter is reserved for
// send/confirm in the chat view, unlike the mail reply composer).
func TestChatAltEnterInsertsNewlineInstead(t *testing.T) {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model = typeRunes(model, "line one")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	model = updated.(Model)
	model = typeRunes(model, "line two")
	if got := model.composer.Value(); got != "line one\nline two" {
		t.Fatalf("draft = %q, want a literal newline from Alt+Enter", got)
	}
}

// TestChatPresenceKeepaliveOnOpenAndFalseOnLeave pins the presence lease
// contract: opening sends focused=true, and leaving (Esc) sends
// focused=false.
func TestChatPresenceKeepaliveOnOpenAndFalseOnLeave(t *testing.T) {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)

	if len(client.keepaliveCalls) != 1 || !client.keepaliveCalls[0].focused {
		t.Fatalf("keepalive calls = %+v, want exactly one focused=true on open", client.keepaliveCalls)
	}

	updated, escCmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.detail || model.chatMode {
		t.Fatal("Esc did not leave the chat view")
	}
	if escCmd != nil {
		updated, _ = model.Update(escCmd())
		model = updated.(Model)
	}
	if len(client.keepaliveCalls) != 2 || client.keepaliveCalls[1].focused {
		t.Fatalf("keepalive calls = %+v, want a second focused=false on leave", client.keepaliveCalls)
	}
}

// TestChatTypingThrottledToOnceEveryFiveSeconds pins the typing throttle:
// consecutive keystrokes within 5s only send composing=true once.
func TestChatTypingThrottledToOnceEveryFiveSeconds(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.now = func() time.Time { return now }
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)

	model = typeRunes(model, "a")
	model = typeRunes(model, "b")
	if len(client.typingCalls) != 1 || !client.typingCalls[0].composing {
		t.Fatalf("typing calls = %+v, want exactly one throttled composing=true", client.typingCalls)
	}

	now = now.Add(6 * time.Second)
	model.now = func() time.Time { return now }
	model = typeRunes(model, "c")
	if len(client.typingCalls) != 2 {
		t.Fatalf("typing calls = %d, want a second call once 5s have passed", len(client.typingCalls))
	}
}

// TestChatScrollUpAtTopPaginatesOlderMessages pins scroll-up pagination:
// reaching the top of the loaded thread requests an older page with
// before=the oldest loaded item's timestamp.
func TestChatScrollUpAtTopPaginatesOlderMessages(t *testing.T) {
	oldest := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	loadedMsg := chatThreadLoadedMsg{token: model.chatToken, items: []core.Item{
		{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", Timestamp: oldest, Body: "hi"},
	}}
	updated, _ = model.Update(loadedMsg)
	model = updated.(Model)

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("scrolling up at the top of the thread did not request an older page")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	var found bool
	for _, call := range client.threadCalls {
		if call.before.Equal(oldest) {
			found = true
		}
	}
	if !found {
		t.Fatalf("thread calls = %+v, want one with before=%v", client.threadCalls, oldest)
	}
}

// TestChatEnterIgnoredWhilePreviewPending pins K10's fix for the reported
// "tengo que dar como 4 enters" bug: the root cause was that a second
// Enter typed before the first preview's round trip resolved reissued
// previewChatReply with a bumped chatReplyToken, discarding the in-flight
// one — so its eventual chatReplyPreviewMsg was silently ignored on
// arrival (wrong token) and the user had to keep pressing Enter hoping to
// land one after the last preview actually returned. With the fix, a
// second Enter while chatPreviewPending is true must be a complete no-op:
// no new command, no bumped token, no second Reply(dryRun=true) call.
func TestChatEnterIgnoredWhilePreviewPending(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"alice"}}}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.confirmChatSend = true // these pin the explicit two-step flow
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model = typeRunes(model, "hola")

	client.block = make(chan struct{})
	updated, previewCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if previewCmd == nil {
		t.Fatal("Enter with draft text did not request a preview")
	}
	if !model.chatPreviewPending {
		t.Fatal("expected chatPreviewPending=true while the preview is in flight")
	}
	tokenBefore := model.chatReplyToken

	done := make(chan tea.Msg, 1)
	go func() { done <- previewCmd() }()

	updated, again := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if again != nil {
		t.Fatal("a second Enter while the preview was pending issued another command")
	}
	if model.chatReplyToken != tokenBefore {
		t.Fatalf("chatReplyToken changed from %d to %d on the extra Enter, want unchanged", tokenBefore, model.chatReplyToken)
	}
	if !strings.Contains(model.View(), "Preparando…") {
		t.Fatalf("chat view = %q, want the pending state shown", model.View())
	}

	close(client.block)
	updated, _ = model.Update(<-done)
	model = updated.(Model)

	if len(client.calls) != 1 {
		t.Fatalf("preview calls = %d, want exactly 1 despite the extra Enter", len(client.calls))
	}
	if !model.chatConfirm {
		t.Fatal("expected the inline confirm once the (single) preview resolved")
	}
}

// TestChatConfirmShowsOptimisticBubbleAndClearsComposer pins the K10
// "sent and I don't see it" fix: confirming must show an own bubble right
// away, dim and marked "enviando…", and clear the composer immediately —
// not wait for the real send (which may hold WhatsApp's simulated
// composing delay of several seconds) to do either.
func TestChatConfirmShowsOptimisticBubbleAndClearsComposer(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"alice"}}}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.confirmChatSend = true // these pin the explicit two-step flow
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model = typeRunes(model, "hola")
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if !model.chatConfirm {
		t.Fatal("expected an inline confirm before sending")
	}

	// Only the real (non-dry-run) send blocks, so the just-finished
	// preview above is never at risk of deadlocking on this channel.
	client.block = make(chan struct{})
	updated, sendCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if sendCmd == nil {
		t.Fatal("confirming Enter did not start the send")
	}
	if model.composer.Value() != "" {
		t.Fatalf("composer after confirm = %q, want cleared immediately", model.composer.Value())
	}
	if model.chatOptimistic == nil || model.chatOptimistic.body != "hola" {
		t.Fatalf("chatOptimistic = %+v, want a pending optimistic bubble carrying the draft", model.chatOptimistic)
	}
	view := model.View()
	if !strings.Contains(view, "hola") {
		t.Fatalf("chat view = %q, want the optimistic bubble's own text", view)
	}
	if !strings.Contains(view, "enviando…") {
		t.Fatalf("chat view = %q, want the optimistic bubble marked \"enviando…\"", view)
	}

	close(client.block)
	done := make(chan tea.Msg, 1)
	go func() { done <- sendCmd() }()
	<-done
}

// TestChatSendSuccessReloadsAndDedupesOptimisticBubble pins the K10 fix
// for chatReplySentMsg (update.go) never reloading the thread: once the
// real send succeeds, the thread must be reloaded, and once the reload
// contains the stored FromMe item (K7b) matching the send's own receipt
// ID, the optimistic bubble must be gone — leaving exactly one bubble for
// the message just sent, never two.
func TestChatSendSuccessReloadsAndDedupesOptimisticBubble(t *testing.T) {
	client := &replyClient{
		previewOut: core.Plan{Recipients: []string{"alice"}},
		sendRcpt:   core.Receipt{ID: "whatsapp:personal:99"},
	}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.confirmChatSend = true // these pin the explicit two-step flow
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model = typeRunes(model, "hola")
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	updated, sendCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, reloadCmd := model.Update(sendCmd())
	model = updated.(Model)
	if model.chatOptimistic == nil || model.chatOptimistic.id != "whatsapp:personal:99" {
		t.Fatalf("chatOptimistic = %+v, want id set from the send's own receipt", model.chatOptimistic)
	}
	if reloadCmd == nil {
		t.Fatal("a successful send did not reload the thread")
	}

	// The reload now finds the stored FromMe item K7b wrote, sharing the
	// receipt's own ID (as Service.storeSentItem keys it).
	client.threadItems = []core.Item{
		{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", FromMe: false, Body: "hi", From: core.Address{Name: "Alice"}},
		{ID: "whatsapp:personal:99", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", FromMe: true, Body: "hola"},
	}
	updated, _ = model.Update(reloadCmd())
	model = updated.(Model)

	if model.chatOptimistic != nil {
		t.Fatalf("chatOptimistic = %+v, want nil once the reload contains the sent item", model.chatOptimistic)
	}
	if got := strings.Count(model.View(), "hola"); got != 1 {
		t.Fatalf("chat view contains %q %d times, want exactly 1 (deduped, not duplicated): %q", "hola", got, model.View())
	}
}

// TestChatSendFailureMarksBubbleAndRestoresDraft pins the K10 failure
// path: the optimistic bubble must stay visible marked "no enviado", the
// draft must be restored to the composer for editing, and no automatic
// retry may ever be issued.
func TestChatSendFailureMarksBubbleAndRestoresDraft(t *testing.T) {
	client := &replyClient{
		previewOut: core.Plan{Recipients: []string{"alice"}},
		sendErr:    context.DeadlineExceeded,
	}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.confirmChatSend = true // pins the explicit two-step flow
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model = typeRunes(model, "hola")
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	updated, sendCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, after := model.Update(sendCmd())
	model = updated.(Model)
	if after != nil {
		t.Fatal("a failed send must never auto-retry")
	}

	if model.composer.Value() != "hola" {
		t.Fatalf("draft after a failed send = %q, want it restored for editing", model.composer.Value())
	}
	if model.chatOptimistic == nil || !model.chatOptimistic.failed {
		t.Fatalf("chatOptimistic = %+v, want it marked failed", model.chatOptimistic)
	}
	if !strings.Contains(model.View(), "no enviado") {
		t.Fatalf("chat view = %q, want the bubble marked \"no enviado\"", model.View())
	}
	if client.sendCalls() != 1 {
		t.Fatalf("send calls = %d, want exactly 1 (no auto-retry)", client.sendCalls())
	}
}
