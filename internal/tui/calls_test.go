package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

type placeRecord struct {
	channel core.Channel
	account string
	to      string
	dryRun  bool
}

type controlRecord struct {
	id     string
	action core.CallAction
	dryRun bool
}

// callClient is replyClient plus CallClient: a scripted list of live calls
// and a record of every place and control request.
type callClient struct {
	replyClient
	live       []core.Call
	listErr    error
	placeErr   error
	controlErr error
	placed     []placeRecord
	controlled []controlRecord
	listCalls  int
}

func (c *callClient) Calls(context.Context) ([]core.Call, error) {
	c.listCalls++
	return c.live, c.listErr
}

func (c *callClient) PlaceCall(_ context.Context, channel core.Channel, account, to string, dryRun bool) (core.Plan, core.Call, error) {
	c.placed = append(c.placed, placeRecord{channel, account, to, dryRun})
	if c.placeErr != nil {
		return core.Plan{}, core.Call{}, c.placeErr
	}
	plan := core.Plan{Action: "call", Channel: channel, Account: account, Target: to, Recipients: []string{to}}
	if dryRun {
		return plan, core.Call{}, nil
	}
	return plan, core.Call{ID: "call-out", Channel: channel, Account: account, Peer: to, PeerName: "Alice", Direction: core.CallOutgoing, State: core.CallStateCalling}, nil
}

func (c *callClient) ControlCall(_ context.Context, id string, action core.CallAction, dryRun bool) (core.Plan, core.Call, error) {
	c.controlled = append(c.controlled, controlRecord{id, action, dryRun})
	if c.controlErr != nil {
		return core.Plan{}, core.Call{}, c.controlErr
	}
	call := core.Call{ID: id, Channel: core.ChannelWhatsApp, Account: "personal", Peer: "5511999999999@s.whatsapp.net", PeerName: "Alice", Direction: core.CallIncoming}
	switch action {
	case core.CallAnswer:
		call.State = core.CallStateConnecting
	default:
		call.State = core.CallStateEnded
	}
	return core.Plan{Action: "call " + string(action)}, call, nil
}

var callsNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func ringingCall() core.Call {
	return core.Call{
		ID: "call-in", Channel: core.ChannelWhatsApp, Account: "personal",
		Peer: "5511999999999@s.whatsapp.net", PeerName: "Alice",
		Direction: core.CallIncoming, State: core.CallStateRinging, StartedAt: callsNow,
	}
}

// callInbox is a loaded inbox with calls polling on.
func callInbox(client Client) Model {
	model := NewModel(client).withGlyphs(nil)
	model.width, model.height = 100, 12
	model.loaded = true
	model.now = func() time.Time { return callsNow }
	model.groups = []inboxGroup{{items: []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Subject: "hola", Unread: true,
	}}}}
	return model
}

// deliverCalls feeds a calls answer into the model.
func deliverCalls(model Model, calls ...core.Call) (Model, tea.Cmd) {
	updated, cmd := model.Update(callsLoadedMsg{calls: calls})
	return updated.(Model), cmd
}

func lastLine(view string) string {
	lines := strings.Split(view, "\n")
	return lines[len(lines)-1]
}

func callChat(t *testing.T, client Client) Model {
	t.Helper()
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.now = func() time.Time { return callsNow }
	model.width, model.height = 100, 30
	model, cmd := openChat(model)
	msg := cmd().(chatThreadLoadedMsg)
	msg.items = []core.Item{
		{ID: "whatsapp:personal:c/1", Channel: core.ChannelWhatsApp, Account: "personal", From: core.Address{Name: "Alice"}, Body: "hola", Timestamp: callsNow},
	}
	updated, _ := model.Update(msg)
	return updated.(Model)
}

func TestChatAltCPlacesCallAfterPreviewAndConfirm(t *testing.T) {
	client := &callClient{}
	model := callChat(t, client)

	model = pressAction(t, model, altKey('c'))
	want := placeRecord{core.ChannelWhatsApp, "personal", "5511999999999@s.whatsapp.net", true}
	if len(client.placed) != 1 || client.placed[0] != want {
		t.Fatalf("Alt+C: %+v, want one dry-run %+v", client.placed, want)
	}
	if !strings.Contains(model.View(), "¿Llamar a «Alice»? ↵ llamar · Esc cancelar") {
		t.Fatalf("no confirm on screen:\n%s", model.View())
	}
	// Nothing is placed before the confirm, and stray letters stay out of
	// the draft.
	model = pressAction(t, model, digitKey('s'))
	if len(client.placed) != 1 || model.composer.Value() != "" {
		t.Fatalf("a letter leaked: %+v, composer %q", client.placed, model.composer.Value())
	}

	model = pressAction(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if len(client.placed) != 2 || client.placed[1].dryRun || client.placed[1].to != want.to {
		t.Fatalf("after confirm: %+v, want the real call", client.placed)
	}
	if flash, ok := model.currentFlash(); !ok || !strings.Contains(flash, "llamando a Alice") {
		t.Errorf("flash = %q, %v", flash, ok)
	}
	// The banner follows at once, before the next poll.
	if line := lastLine(model.View()); !strings.Contains(line, "Llamando a Alice… · Alt+H colgar") {
		t.Errorf("banner = %q", line)
	}
}

func TestChatCallEscCancelsWithoutPlacing(t *testing.T) {
	client := &callClient{}
	model := callChat(t, client)
	model = pressAction(t, model, altKey('c'))
	model = pressAction(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.chatAction != nil || len(client.placed) != 1 || !client.placed[0].dryRun || !model.chatMode {
		t.Fatalf("Esc should drop the confirm and place nothing: %+v", client.placed)
	}
}

func TestChatCallOnAccountWithoutCallsSaysSo(t *testing.T) {
	client := &callClient{placeErr: fmt.Errorf("core: adapter whatsapp/personal: whatsapp: calls are off: %w", core.ErrUnsupported)}
	model := callChat(t, client)
	model = pressAction(t, model, altKey('c'))
	if model.chatAction != nil || model.chatSendErr == nil {
		t.Fatalf("action %+v, err %v; want an error", model.chatAction, model.chatSendErr)
	}
	view := model.View()
	if !strings.Contains(view, "Error: las llamadas no están activadas en esta cuenta") || !strings.Contains(view, "calls = true") {
		t.Errorf("no clear error:\n%s", view)
	}
	if len(client.placed) != 1 || !client.placed[0].dryRun {
		t.Errorf("placed = %+v", client.placed)
	}
}

func TestChatCallErrorsAreLoud(t *testing.T) {
	t.Run("connection without calls", func(t *testing.T) {
		model := callChat(t, &replyClient{})
		model = pressAction(t, model, altKey('c'))
		if !errors.Is(model.chatSendErr, errNoCalls) || model.chatAction != nil {
			t.Errorf("err = %v, action %+v", model.chatSendErr, model.chatAction)
		}
	})
	t.Run("not WhatsApp", func(t *testing.T) {
		client := &callClient{}
		model := callChat(t, client)
		model.chatChannel = core.ChannelMatrix
		model = pressAction(t, model, altKey('c'))
		if !errors.Is(model.chatSendErr, errCallOnlyWA) || len(client.placed) != 0 {
			t.Errorf("err = %v, placed %+v", model.chatSendErr, client.placed)
		}
	})
	t.Run("group", func(t *testing.T) {
		client := &callClient{}
		model := callChat(t, client)
		model.chatThread = "120363000000000000@g.us"
		model = pressAction(t, model, altKey('c'))
		if !errors.Is(model.chatSendErr, errCallGroup) || len(client.placed) != 0 {
			t.Errorf("err = %v, placed %+v", model.chatSendErr, client.placed)
		}
	})
	t.Run("call already live", func(t *testing.T) {
		client := &callClient{}
		model := callChat(t, client)
		model, _ = deliverCalls(model, core.Call{ID: "x", State: core.CallStateActive, Peer: "1"})
		model = pressAction(t, model, altKey('c'))
		if !errors.Is(model.chatSendErr, errCallBusy) || len(client.placed) != 0 {
			t.Errorf("err = %v, placed %+v", model.chatSendErr, client.placed)
		}
	})
}

func TestIncomingCallBannerOnEveryViewAndAnswer(t *testing.T) {
	client := &callClient{}
	model := callInbox(client)
	model, _ = deliverCalls(model, ringingCall())

	view := model.View()
	if line := lastLine(view); !strings.Contains(line, "Llamada entrante de Alice · a contestar · x rechazar") {
		t.Fatalf("inbox banner = %q", line)
	}
	if len(strings.Split(view, "\n")) != len(strings.Split(model.baseView(), "\n")) {
		t.Error("the banner changed the view's height")
	}

	// Mail thread: same plain keys.
	thread := model
	thread.detail, thread.threadMode = true, true
	if line := lastLine(thread.View()); !strings.Contains(line, "a contestar") {
		t.Errorf("thread banner = %q", line)
	}
	// Chat: the composer owns plain keys, so the banner says Alt.
	chat := callChat(t, client)
	chat, _ = deliverCalls(chat, ringingCall())
	if line := lastLine(chat.View()); !strings.Contains(line, "Alt+A contestar · Alt+X rechazar") {
		t.Errorf("chat banner = %q", line)
	}

	model = pressAction(t, model, digitKey('a'))
	if len(client.controlled) != 1 || client.controlled[0] != (controlRecord{"call-in", core.CallAnswer, false}) {
		t.Fatalf("controlled = %+v", client.controlled)
	}
	if flash, ok := model.currentFlash(); !ok || flash != "llamada contestada" {
		t.Errorf("flash = %q, %v", flash, ok)
	}
	if line := lastLine(model.View()); !strings.Contains(line, "Conectando con Alice…") {
		t.Errorf("banner after answering = %q", line)
	}
}

func TestIncomingCallRejectWithX(t *testing.T) {
	client := &callClient{}
	model := callInbox(client)
	model, _ = deliverCalls(model, ringingCall())
	model = pressAction(t, model, digitKey('x'))
	if len(client.controlled) != 1 || client.controlled[0] != (controlRecord{"call-in", core.CallReject, false}) {
		t.Fatalf("controlled = %+v", client.controlled)
	}
	if model.ringingCall() != nil || strings.Contains(model.View(), "📞") {
		t.Errorf("the banner should be gone:\n%s", model.View())
	}
	if flash, _ := model.currentFlash(); flash != "llamada rechazada" {
		t.Errorf("flash = %q", flash)
	}
}

func TestCallKeysDoNothingWithoutACall(t *testing.T) {
	client := &callClient{}
	model := callInbox(client)
	for _, r := range []rune{'x', 'h'} {
		model = pressAction(t, model, digitKey(r))
	}
	_ = pressAction(t, model, altKey('h'))
	if len(client.controlled) != 0 {
		t.Errorf("controlled = %+v", client.controlled)
	}
}

func TestAltCallKeysWorkWhereTextIsTyped(t *testing.T) {
	client := &callClient{}
	model := callChat(t, client)
	model, _ = deliverCalls(model, ringingCall())
	model = typeRunes(model, "ax")
	if model.composer.Value() != "ax" {
		t.Fatalf("plain a/x must stay text, composer %q", model.composer.Value())
	}
	model = pressAction(t, model, altKey('x'))
	if len(client.controlled) != 1 || client.controlled[0].action != core.CallReject {
		t.Fatalf("Alt+X while ringing: %+v", client.controlled)
	}
	// With nothing ringing Alt+X is the chat's own "eliminar".
	if model.chatAction != nil {
		t.Errorf("Alt+X rejecting the call also started a delete: %+v", model.chatAction)
	}
}

func TestActiveCallStatusLineAndHangup(t *testing.T) {
	client := &callClient{}
	model := callInbox(client)
	active := ringingCall()
	active.State = core.CallStateActive
	active.ConnectedAt = callsNow.Add(-95 * time.Second)
	model, cmd := deliverCalls(model, active)
	if cmd == nil {
		t.Fatal("no next poll scheduled")
	}
	if line := lastLine(model.View()); !strings.Contains(line, "En llamada con Alice · 1:35 · h colgar") {
		t.Fatalf("status line = %q", line)
	}
	// The duration follows the clock.
	model.now = func() time.Time { return callsNow.Add(10 * time.Second) }
	if line := lastLine(model.View()); !strings.Contains(line, "1:45") {
		t.Errorf("status line = %q", line)
	}

	model = pressAction(t, model, digitKey('h'))
	if len(client.controlled) != 1 || client.controlled[0] != (controlRecord{"call-in", core.CallHangup, false}) {
		t.Fatalf("controlled = %+v", client.controlled)
	}
	if model.liveCall() != nil || strings.Contains(model.View(), "📞") {
		t.Errorf("the status line should be gone:\n%s", model.View())
	}
	if flash, _ := model.currentFlash(); flash != "llamada terminada" {
		t.Errorf("flash = %q", flash)
	}
}

func TestCallControlFailureShowsErrorAndRefreshes(t *testing.T) {
	client := &callClient{controlErr: errors.New("whatsapp: call \"call-in\": not found")}
	model := callInbox(client)
	model, _ = deliverCalls(model, ringingCall())
	updated, cmd := model.Update(digitKey('a'))
	model = updated.(Model)
	updated, refresh := model.Update(cmd())
	model = updated.(Model)
	flash, _ := model.currentFlash()
	if !strings.HasPrefix(flash, "no se pudo contestar: ") {
		t.Errorf("flash = %q", flash)
	}
	if model.callBusy || refresh == nil {
		t.Errorf("busy %v, refresh %v", model.callBusy, refresh)
	}
}

func TestEndedCallsAreDroppedAndPollingContinues(t *testing.T) {
	model := callInbox(&callClient{})
	ended := ringingCall()
	ended.State = core.CallStateEnded
	model, cmd := deliverCalls(model, ended)
	if len(model.calls) != 0 || cmd == nil {
		t.Errorf("calls %+v, cmd %v", model.calls, cmd)
	}
}

func TestCallsPollStartsOnceWithACallClient(t *testing.T) {
	client := &callClient{live: []core.Call{ringingCall()}}
	model := callInbox(client)
	model, cmd := model.startCalls()
	if cmd == nil || !model.callsStarted {
		t.Fatal("polling did not start")
	}
	if _, again := model.startCalls(); again != nil {
		t.Error("polling started twice")
	}
	msg := cmd().(callsLoadedMsg)
	model, next := deliverCalls(model, msg.calls...)
	if client.listCalls != 1 || model.ringingCall() == nil || next == nil {
		t.Fatalf("list calls %d, ringing %v, next %v", client.listCalls, model.ringingCall(), next)
	}
	_, refresh := model.Update(callTickMsg{})
	if refresh == nil {
		t.Error("a tick did not ask for the calls again")
	}

	// A connection without the capability never polls.
	plain := callInbox(&replyClient{})
	if plain, cmd := plain.startCalls(); cmd != nil || plain.callsStarted {
		t.Error("polled a connection without calls")
	}
}

func TestCallsUnsupportedStopsPolling(t *testing.T) {
	model := callInbox(&callClient{})
	model, _ = model.startCalls()
	updated, cmd := model.Update(callsLoadedMsg{err: fmt.Errorf("tui: calls: %w", core.ErrUnsupported)})
	if cmd != nil || len(updated.(Model).calls) != 0 {
		t.Errorf("cmd %v: an unsupported daemon should stop the polling", cmd)
	}
	// Any other error keeps polling and keeps the last known calls.
	model, _ = deliverCalls(model, ringingCall())
	updated, cmd = model.Update(callsLoadedMsg{err: errors.New("rpc: connection closed")})
	if cmd == nil || updated.(Model).ringingCall() == nil {
		t.Errorf("a transient error dropped the calls or the polling (cmd %v)", cmd)
	}
}

func TestIncomingCallNotifiesOnce(t *testing.T) {
	t.Run("osc 777", func(t *testing.T) {
		var out bytes.Buffer
		model := callInbox(&callClient{})
		model.notifyEnabled, model.notifyWriter = true, &out
		model, cmd := deliverCalls(model, ringingCall())
		runBatch(cmd)
		if !strings.Contains(out.String(), "Llamada entrante de Alice") {
			t.Fatalf("notification = %q", out.String())
		}
		out.Reset()
		_, cmd = deliverCalls(model, ringingCall())
		runBatch(cmd)
		if out.Len() != 0 {
			t.Errorf("the same call notified twice: %q", out.String())
		}
	})
	t.Run("herdr notifier", func(t *testing.T) {
		var bodies []string
		model := callInbox(&callClient{})
		model.messageNotify = func(body string) error { bodies = append(bodies, body); return nil }
		_, cmd := deliverCalls(model, ringingCall())
		runBatch(cmd)
		if len(bodies) != 1 || bodies[0] != "Llamada entrante de Alice" {
			t.Errorf("bodies = %q", bodies)
		}
	})
	t.Run("notifications off", func(t *testing.T) {
		var out bytes.Buffer
		model := callInbox(&callClient{})
		model.notifyWriter = &out
		_, cmd := deliverCalls(model, ringingCall())
		runBatch(cmd)
		if out.Len() != 0 {
			t.Errorf("notified with notifications off: %q", out.String())
		}
	})
}

// runBatch runs a command and any commands it batches, ignoring their
// messages: the tests look at the side effects.
func runBatch(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runBatch(c)
		}
	}
}

func TestPaletteListsCallCommands(t *testing.T) {
	client := &callClient{}
	model := callInbox(client)
	model.agentAsk = func(context.Context, string) error { return nil }
	model, _ = deliverCalls(model, ringingCall())
	model = model.openPalette()
	byLabel := map[string]paletteEntry{}
	for _, e := range model.paletteCommands() {
		byLabel[e.label] = e
	}
	answer, ok := byLabel["Contestar llamada"]
	if !ok || answer.key != "a" || answer.reason != "" {
		t.Fatalf("answer entry = %+v, %v", answer, ok)
	}
	if _, ok := byLabel["Rechazar llamada"]; !ok {
		t.Error("no reject entry")
	}
	if ask := byLabel["Preguntar a Claude"]; ask.reason == "" {
		t.Error("the ask key answers the call while it rings; the palette should say so")
	}

	// Running the entry answers the call.
	model = pressAction(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Contestar")})
	model = pressAction(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if len(client.controlled) != 1 || client.controlled[0].action != core.CallAnswer {
		t.Fatalf("controlled = %+v", client.controlled)
	}
}

func TestPaletteChatListsCallAndReasons(t *testing.T) {
	client := &callClient{}
	model := callChat(t, client)
	entries := map[string]paletteEntry{}
	for _, e := range model.paletteCommands() {
		entries[e.label] = e
	}
	if call, ok := entries["Llamar"]; !ok || call.key != "Alt+C" || call.reason != "" {
		t.Fatalf("Llamar = %+v, %v", call, ok)
	}
	model.chatThread = "120363000000000000@g.us"
	for _, e := range model.paletteCommands() {
		if e.label == "Llamar" && e.reason != errCallGroup.Error() {
			t.Errorf("reason = %q", e.reason)
		}
	}
	model, _ = deliverCalls(model, ringingCall())
	got := map[string]paletteEntry{}
	for _, e := range model.paletteCommands() {
		got[e.label] = e
	}
	if e := got["Contestar llamada"]; e.key != "Alt+A" {
		t.Errorf("chat answer key = %q", e.key)
	}
}

func TestHelpDescribesCalls(t *testing.T) {
	chat := strings.Join(helpBody("chat", helpOptions{}), "\n")
	for _, want := range []string{"Alt+C", "calls = true"} {
		if !strings.Contains(chat, want) {
			t.Errorf("chat help does not mention %q", want)
		}
	}
	ringing := strings.Join(helpBody("inbox", helpOptions{answer: "a", reject: "x", hangup: "h", ringing: true}), "\n")
	live := strings.Join(helpBody("chat", helpOptions{answer: "Alt+A", reject: "Alt+X", hangup: "Alt+H", live: true}), "\n")
	for help, wants := range map[string][]string{ringing: {"Llamadas de voz", "a/x", "contestar"}, live: {"Llamadas de voz", "Alt+H", "colgar"}} {
		for _, want := range wants {
			if !strings.Contains(help, want) {
				t.Errorf("help during a call does not mention %q:\n%s", want, help)
			}
		}
	}
}
