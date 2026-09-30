package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/reyer3/bunker-go/internal/core"
)

// Voice calls in the panel (issue #27). Placing a call is a chat action
// (Alt+C, see chat_actions.go: the same dry-run preview and confirm as an
// edit or a delete). Answering, rejecting and hanging up act on a live call
// from any view, so they run from a banner on the last line of whatever is
// on screen. Calls stay opt-in per account (calls = true): a daemon with
// none simply reports no calls.
//
// Keys: where plain keys are commands (inbox, detail, mail thread) the
// banner uses a / x / h; where a text field has focus (a chat's composer,
// an editor, the filter) plain keys are text, so the same commands are
// Alt+A / Alt+X / Alt+H there. They only act while they apply: a and x
// while an incoming call rings (shadowing "preguntar a Claude" and, in a
// chat, "eliminar último mensaje"), h while a call is live.

// CallClient is the optional Client capability behind the call keys;
// rpc.Client has it, and a connection without it reports calls as
// unavailable.
type CallClient interface {
	Calls(ctx context.Context) ([]core.Call, error)
	PlaceCall(ctx context.Context, channel core.Channel, account, to string, dryRun bool) (core.Plan, core.Call, error)
	ControlCall(ctx context.Context, id string, action core.CallAction, dryRun bool) (core.Plan, core.Call, error)
}

const (
	// callPollIdle is how often calls are polled with none live (so a
	// ringing call shows within a few seconds); callPollLive while one
	// is, so its duration counts in seconds.
	callPollIdle = 3 * time.Second
	callPollLive = time.Second
	// callTimeout bounds one calls listing or control request.
	callTimeout = 10 * time.Second

	callAnswerKey = "a"
	callRejectKey = "x"
	callHangupKey = "h"
	// callPlaceKey is Alt+C: a chat's composer takes every plain key.
	callPlaceKey = "alt+c"
)

var (
	errNoCalls         = errors.New("esta conexión no permite llamadas")
	errCallsNotEnabled = errors.New("las llamadas no están activadas en esta cuenta · añade calls = true a la cuenta de WhatsApp")
	errCallOnlyWA      = errors.New("las llamadas de voz solo están disponibles en WhatsApp")
	errCallGroup       = errors.New("no se puede llamar a un grupo")
	errCallBusy        = errors.New("ya hay una llamada en curso")
)

type callsLoadedMsg struct {
	calls []core.Call
	err   error
}

type callTickMsg struct{}

type callControlDoneMsg struct {
	action core.CallAction
	call   core.Call
	err    error
}

// callNotifiedMsg reports the incoming-call notification attempt.
type callNotifiedMsg struct{ err error }

func loadCalls(client Client) tea.Cmd {
	return func() tea.Msg {
		cc, ok := client.(CallClient)
		if !ok {
			return callsLoadedMsg{err: errNoCalls}
		}
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		calls, err := cc.Calls(ctx)
		return callsLoadedMsg{calls: calls, err: err}
	}
}

func (m Model) nextCallTick() tea.Cmd {
	interval := callPollIdle
	if m.liveCall() != nil {
		interval = callPollLive
	}
	return tea.Tick(interval, func(time.Time) tea.Msg { return callTickMsg{} })
}

// startCalls begins polling calls, once, when the connection supports
// them.
func (m Model) startCalls() (Model, tea.Cmd) {
	if m.callsStarted || m.client == nil {
		return m, nil
	}
	if _, ok := m.client.(CallClient); !ok {
		return m, nil
	}
	m.callsStarted = true
	return m, loadCalls(m.client)
}

func (m Model) handleCallsLoaded(msg callsLoadedMsg) (tea.Model, tea.Cmd) {
	if errors.Is(msg.err, core.ErrUnsupported) || errors.Is(msg.err, errNoCalls) {
		// The daemon does not do calls: stop asking.
		m.calls = nil
		return m, nil
	}
	if msg.err != nil {
		// Keep what is known (the daemon may just be restarting) and
		// keep polling.
		return m, m.nextCallTick()
	}
	var live []core.Call
	for _, c := range msg.calls {
		if c.State != core.CallStateEnded {
			live = append(live, c)
		}
	}
	if m.callNotified == nil {
		m.callNotified = map[string]bool{}
	}
	var notify []tea.Cmd
	for _, c := range live {
		if c.Direction == core.CallIncoming && c.State == core.CallStateRinging && !m.callNotified[c.ID] {
			m.callNotified[c.ID] = true
			notify = append(notify, m.notifyCall(c))
		}
	}
	m.calls = live
	return m, tea.Batch(append(notify, m.nextCallTick())...)
}

// notifyCall announces an incoming call the way new messages are
// (herdr's notifier when wired, else OSC 777), once per call and without
// the focus or rate limits messages have: a ringing call is worth an
// interruption.
func (m Model) notifyCall(c core.Call) tea.Cmd {
	body := "Llamada entrante de " + callPeerName(c)
	if m.messageNotify != nil {
		notify := m.messageNotify
		return func() tea.Msg { return callNotifiedMsg{err: notify(body)} }
	}
	if !m.notifyEnabled {
		return nil
	}
	payload := notifyPayload("bunker", body)
	if m.tmuxPassthrough {
		payload = tmuxPassthrough(payload)
	}
	return notifyCmd(m.notifyWriter, payload)
}

func (m Model) handleCallNotified(msg callNotifiedMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil {
		m.messageNotifyFailed = false
		return m, nil
	}
	if m.messageNotifyFailed {
		return m, nil
	}
	m.messageNotifyFailed = true
	return m.withFlash("no se pudo notificar en herdr: " + humanError(msg.err)), nil
}

func (m Model) handleCallTick() (tea.Model, tea.Cmd) {
	if !m.callsStarted || m.client == nil {
		return m, nil
	}
	return m, loadCalls(m.client)
}

// callPeerName is who the call is with, never a raw identifier.
func callPeerName(c core.Call) string {
	if name := strings.TrimSpace(c.PeerName); name != "" && !looksLikeRawIdentifier(name) {
		return safeLine(name)
	}
	peer := c.Peer
	if i := strings.Index(peer, "@"); i > 0 {
		peer = peer[:i]
	}
	if peer = strings.TrimSpace(peer); peer != "" {
		return safeLine(peer)
	}
	return "un contacto"
}

// ringingCall is the newest incoming call still ringing.
func (m Model) ringingCall() *core.Call {
	for i := len(m.calls) - 1; i >= 0; i-- {
		c := m.calls[i]
		if c.Direction == core.CallIncoming && c.State == core.CallStateRinging {
			return &m.calls[i]
		}
	}
	return nil
}

// liveCall is the newest call that has not ended.
func (m Model) liveCall() *core.Call {
	for i := len(m.calls) - 1; i >= 0; i-- {
		if m.calls[i].State != core.CallStateEnded {
			return &m.calls[i]
		}
	}
	return nil
}

// callBannerCall is the call the banner shows: a ringing one first, else
// the newest live one.
func (m Model) callBannerCall() *core.Call {
	if c := m.ringingCall(); c != nil {
		return c
	}
	return m.liveCall()
}

// callKeysNeedAlt reports whether plain keys are text (or belong to an
// overlay) in the current view, so the call commands need Alt.
func (m Model) callKeysNeedAlt() bool {
	return m.helpOpen || m.palette != nil || m.openPending() || m.picker != nil || m.filtering ||
		m.composing || m.previewing || m.marking || m.downloadActive || m.viewer != nil ||
		m.chatMode || m.mailComposing
}

// callKeyNames are the answer, reject and hang-up keys as the banner shows
// them in the current view.
func (m Model) callKeyNames() (answer, reject, hangup string) {
	if m.callKeysNeedAlt() {
		return "Alt+A", "Alt+X", "Alt+H"
	}
	return callAnswerKey, callRejectKey, callHangupKey
}

// callBannerLine is the call line drawn over the view's last line, or
// false with no call.
func (m Model) callBannerLine() (string, bool) {
	c := m.callBannerCall()
	if c == nil {
		return "", false
	}
	answer, reject, hangup := m.callKeyNames()
	name := callPeerName(*c)
	switch {
	case c.Direction == core.CallIncoming && c.State == core.CallStateRinging:
		return fmt.Sprintf("📞 Llamada entrante de %s · %s contestar · %s rechazar", name, answer, reject), true
	case c.State == core.CallStateActive:
		return fmt.Sprintf("📞 En llamada con %s · %s · %s colgar", name, core.FormatCallDuration(c.Duration(m.clock())), hangup), true
	case c.State == core.CallStateConnecting:
		return fmt.Sprintf("📞 Conectando con %s… · %s colgar", name, hangup), true
	}
	return fmt.Sprintf("📞 Llamando a %s… · %s colgar", name, hangup), true
}

// withCallBanner draws the call line over the view's last line (the key
// hints), so it shows in every view without moving any other line: the
// views fit their height and map mouse clicks by line.
func (m Model) withCallBanner(view string) string {
	line, ok := m.callBannerLine()
	if !ok {
		return view
	}
	style := m.renderer().NewStyle().Bold(true)
	if m.ringingCall() != nil {
		style = style.Reverse(true)
	} else {
		style = style.Foreground(lipgloss.Color("2"))
	}
	if m.width > 0 {
		line = runewidth.Truncate(line, m.width, "…")
	}
	lines := strings.Split(view, "\n")
	lines[len(lines)-1] = style.Render(line)
	return strings.Join(lines, "\n")
}

// callKey handles the Alt call keys, which work in every view; ok is
// false when key is not one that applies now.
func (m Model) callKey(key string) (tea.Model, tea.Cmd, bool) {
	var action core.CallAction
	var c *core.Call
	switch key {
	case "alt+" + callAnswerKey:
		action, c = core.CallAnswer, m.ringingCall()
	case "alt+" + callRejectKey:
		action, c = core.CallReject, m.ringingCall()
	case "alt+" + callHangupKey:
		action, c = core.CallHangup, m.liveCall()
	}
	if c == nil {
		return m, nil, false
	}
	next, cmd := m.controlCall(*c, action)
	return next, cmd, true
}

// plainCallKey handles a / x / h in the views where plain keys are
// commands.
func (m Model) plainCallKey(key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case callAnswerKey, callRejectKey, callHangupKey:
		return m.callKey("alt+" + key)
	}
	return m, nil, false
}

// plainCallKeyOrNothing is plainCallKey for keys that mean nothing else.
func (m Model) plainCallKeyOrNothing(key string) (tea.Model, tea.Cmd) {
	next, cmd, _ := m.plainCallKey(key)
	return next, cmd
}

// controlCall answers, rejects or hangs up c. There is no extra confirm:
// the banner names the call and the key. The daemon call is the same
// core.ControlCall the CLI's bunker call uses.
func (m Model) controlCall(c core.Call, action core.CallAction) (tea.Model, tea.Cmd) {
	if m.callBusy {
		return m, nil
	}
	m.callBusy = true
	client, id := m.client, c.ID
	return m, func() tea.Msg {
		cc, ok := client.(CallClient)
		if !ok {
			return callControlDoneMsg{action: action, err: errNoCalls}
		}
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		_, call, err := cc.ControlCall(ctx, id, action, false)
		return callControlDoneMsg{action: action, call: call, err: err}
	}
}

var callControlDone = map[core.CallAction]string{
	core.CallAnswer: "llamada contestada",
	core.CallReject: "llamada rechazada",
	core.CallHangup: "llamada terminada",
}

var callControlFailed = map[core.CallAction]string{
	core.CallAnswer: "no se pudo contestar",
	core.CallReject: "no se pudo rechazar",
	core.CallHangup: "no se pudo colgar",
}

func (m Model) handleCallControlDone(msg callControlDoneMsg) (tea.Model, tea.Cmd) {
	m.callBusy = false
	if msg.err != nil {
		// The call may have ended on its own: refresh so the banner
		// does not offer a dead call.
		m = m.withFlash(callControlFailed[msg.action] + ": " + humanError(msg.err))
		return m, loadCalls(m.client)
	}
	m = m.withFlash(callControlDone[msg.action])
	return m.upsertCall(msg.call), nil
}

// upsertCall records c (dropping it once ended) so the banner follows an
// action at once instead of at the next poll.
func (m Model) upsertCall(c core.Call) Model {
	if c.ID == "" {
		return m
	}
	out := make([]core.Call, 0, len(m.calls)+1)
	for _, old := range m.calls {
		if old.ID != c.ID {
			out = append(out, old)
		}
	}
	if c.State != core.CallStateEnded {
		out = append(out, c)
	}
	m.calls = out
	return m
}

// startChatCall previews a call to the open chat's contact (Alt+C).
func (m Model) startChatCall() (tea.Model, tea.Cmd) {
	if reason := m.chatCallBlocked(); reason != nil {
		m.chatSendErr = reason
		return m, nil
	}
	m.chatSendErr = nil
	return m.planChatAction(&chatAction{kind: "call", id: m.chatCallTarget(), quoted: m.chatName})
}

// chatCallTarget is who Alt+C dials: the address the chat was opened
// with, else its thread (a contact's JID).
func (m Model) chatCallTarget() string {
	if m.chatNewTo != "" {
		return m.chatNewTo
	}
	return m.chatThread
}

// chatCallBlocked says why the open chat cannot be called, or nil.
func (m Model) chatCallBlocked() error {
	if _, ok := m.client.(CallClient); !ok {
		return errNoCalls
	}
	switch {
	case m.chatChannel != core.ChannelWhatsApp:
		return errCallOnlyWA
	case strings.HasSuffix(m.chatCallTarget(), "@g.us"):
		return errCallGroup
	case m.liveCall() != nil:
		return errCallBusy
	}
	return nil
}

// callActionError turns the daemon's "cannot call" (calls not opted in
// for the account) into the panel's Spanish sentence; anything else
// passes through.
func callActionError(err error) error {
	if errors.Is(err, core.ErrUnsupported) {
		return errCallsNotEnabled
	}
	return err
}
