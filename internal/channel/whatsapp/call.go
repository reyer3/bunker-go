package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

// errCallsDisabled is CanCall's answer on an account that did not opt in
// with "calls = true": calling runs an experimental, reverse-engineered
// VoIP stack (meowcaller), so it is never on by default.
var errCallsDisabled = fmt.Errorf("whatsapp: calls are disabled for this account, set calls = true in its [[account]] block: %w", core.ErrUnsupported)

// errCallBusy is PlaceCall's answer while another call is still live: the
// daemon has one microphone and one speaker, so it holds one call at a time.
var errCallBusy = errors.New("whatsapp: another call is already in progress")

// callEngine is the narrow slice of meowcaller.Client this adapter drives,
// so every call behavior is testable with a fake (see meowEngine for the
// real one).
type callEngine interface {
	Call(ctx context.Context, target string) (liveCall, error)
	OnIncomingCall(fn func(liveCall))
}

// liveCall is the narrow slice of *meowcaller.Call this adapter drives.
type liveCall interface {
	ID() string
	Peer() types.JID
	Answer() error
	Reject() error
	Hangup() error
	// OnReady fires once media is flowing; audio is attached only then,
	// so no microphone audio is buffered while the call is still ringing.
	OnReady(fn func())
	OnPeerAccept(fn func())
	OnEnd(fn func(reason string))
	AttachAudio(src meowcaller.AudioSource, sink meowcaller.AudioSink)
}

// callAudio opens the local microphone (src) and speaker (sink) for one
// call, both 16 kHz mono (meowcaller.SampleRate). Either may be nil when
// the account configured no capture/playback command. problem is told
// (short text for the user, full error for the log) when a helper fails or
// dies after Open returned.
type callAudio interface {
	Open(problem func(short string, err error)) (meowcaller.AudioSource, meowcaller.AudioSink, error)
}

// audioChecker is an optional callAudio capability: Check reports, before
// a call starts, why it could not have any sound (a missing helper).
type audioChecker interface {
	Check() error
}

// callMediaTimeout is how long an answered call may go without any media
// from the peer before it is flagged (see noMedia).
const callMediaTimeout = 10 * time.Second

// errNoMedia is the AudioError of a call whose peer never sent media.
const errNoMedia = "no llega audio del otro lado (sin medios)"

// callRecord is one call this adapter tracks until it ends.
type callRecord struct {
	live     liveCall
	info     core.Call
	item     core.Item
	answered bool
	src      meowcaller.AudioSource
	sink     meowcaller.AudioSink
	// audioOpened is set once the audio helpers were started, so a
	// repeated OnReady never spawns a second set.
	audioOpened bool
	// mediaTimer is the no-media watchdog, armed when the call is
	// answered/accepted and stopped when media arrives or the call ends.
	mediaTimer func() bool
}

// EnableCalls turns on voice calls for this account, driving engine for
// signaling and audio for the local microphone/speaker. NewFromAccount
// calls it only when the account opted in with "calls = true".
func (a *Adapter) EnableCalls(engine callEngine, audio callAudio) {
	a.callMu.Lock()
	a.calls = engine
	a.callAudio = audio
	a.liveCalls = make(map[string]*callRecord)
	a.callMu.Unlock()
	engine.OnIncomingCall(a.handleIncomingCall)
	// Say at startup, not mid-call, that the audio tools are missing: the
	// call itself is refused with the same message (see checkCallAudio).
	if err := a.checkCallAudio(); err != nil {
		a.callLog().Warn("calls are enabled but cannot have audio", "error", err)
	}
}

// callLog is the logger of this account's call activity.
func (a *Adapter) callLog() *slog.Logger {
	return slog.Default().With("channel", string(core.ChannelWhatsApp), "account", a.account, "component", "call")
}

// checkCallAudio fails when the configured audio helpers cannot work, so a
// call with no possible sound is refused instead of ringing silently.
func (a *Adapter) checkCallAudio() error {
	a.callMu.Lock()
	audio := a.callAudio
	a.callMu.Unlock()
	if c, ok := audio.(audioChecker); ok {
		return c.Check()
	}
	return nil
}

// afterFunc runs f after d; tests replace callAfter to fire it by hand.
func (a *Adapter) afterFunc(d time.Duration, f func()) (stop func() bool) {
	if a.callAfter != nil {
		return a.callAfter(d, f)
	}
	return time.AfterFunc(d, f).Stop
}

// CanCall implements core.Caller.
func (a *Adapter) CanCall() error {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	if a.calls == nil {
		return errCallsDisabled
	}
	return nil
}

// PlaceCall implements core.Caller: it rings to (a +E164 phone number or
// a JID) and returns once the offer is on the wire. Audio starts when the
// peer answers and media flows (see OnReady).
func (a *Adapter) PlaceCall(ctx context.Context, to string) (core.Call, error) {
	if err := a.CanCall(); err != nil {
		return core.Call{}, err
	}
	if err := a.checkCallAudio(); err != nil {
		return core.Call{}, err
	}
	a.callMu.Lock()
	if len(a.liveCalls) > 0 || a.placingCall {
		a.callMu.Unlock()
		return core.Call{}, errCallBusy
	}
	a.placingCall = true
	engine := a.calls
	a.callMu.Unlock()
	defer func() {
		a.callMu.Lock()
		a.placingCall = false
		a.callMu.Unlock()
	}()

	number, err := callNumber(to)
	if err != nil {
		return core.Call{}, err
	}
	live, err := engine.Call(ctx, number)
	if err != nil {
		return core.Call{}, fmt.Errorf("whatsapp: call %s: %w", to, err)
	}
	rec := a.trackCall(ctx, live, core.CallOutgoing, core.CallStateCalling)
	live.OnPeerAccept(func() { a.setCallState(live.ID(), core.CallStateConnecting, true) })
	return rec, nil
}

// ControlCall implements core.Caller.
func (a *Adapter) ControlCall(ctx context.Context, callID string, action core.CallAction) (core.Call, error) {
	a.callMu.Lock()
	rec, ok := a.liveCalls[callID]
	var info core.Call
	if ok {
		info = rec.info
	}
	a.callMu.Unlock()
	if !ok {
		return core.Call{}, fmt.Errorf("whatsapp: call %q: %w", callID, core.ErrNotFound)
	}
	ringing := info.Direction == core.CallIncoming && info.State == core.CallStateRinging

	switch action {
	case core.CallAnswer:
		if !ringing {
			return core.Call{}, fmt.Errorf("whatsapp: call %q is not ringing", callID)
		}
		// Refuse before answering: the call stays ringing, so it can still
		// be rejected, instead of connecting into silence.
		if err := a.checkCallAudio(); err != nil {
			return core.Call{}, err
		}
		if err := rec.live.Answer(); err != nil {
			return core.Call{}, fmt.Errorf("whatsapp: answer: %w", err)
		}
		return a.setCallState(callID, core.CallStateConnecting, true), nil
	case core.CallReject:
		if !ringing {
			return core.Call{}, fmt.Errorf("whatsapp: call %q is not ringing, use hangup", callID)
		}
		if err := rec.live.Reject(); err != nil {
			return core.Call{}, fmt.Errorf("whatsapp: reject: %w", err)
		}
		return a.endCallOr(ctx, rec, "rejected"), nil
	case core.CallHangup:
		if err := rec.live.Hangup(); err != nil {
			return core.Call{}, fmt.Errorf("whatsapp: hangup: %w", err)
		}
		return a.endCallOr(ctx, rec, "hangup"), nil
	default:
		return core.Call{}, fmt.Errorf("whatsapp: unknown call action %q", action)
	}
}

// ActiveCalls implements core.Caller.
func (a *Adapter) ActiveCalls() []core.Call {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	out := make([]core.Call, 0, len(a.liveCalls))
	for _, rec := range a.liveCalls {
		out = append(out, rec.info)
	}
	return out
}

// handleIncomingCall surfaces an inbound offer as a ringing call and an
// unread "incoming call" item, so it shows up in list, counts and tmux.
// It never answers on its own: that is always an explicit "call answer".
func (a *Adapter) handleIncomingCall(live liveCall) {
	a.trackCall(context.Background(), live, core.CallIncoming, core.CallStateRinging)
}

// trackCall registers live, wires its lifecycle callbacks and writes its
// conversation item.
func (a *Adapter) trackCall(ctx context.Context, live liveCall, direction, state string) core.Call {
	peer := live.Peer()
	item := core.Item{
		ID:        itemID(a.account, peer.String(), "call-"+live.ID()),
		Channel:   core.ChannelWhatsApp,
		Account:   a.account,
		Thread:    peer.String(),
		From:      core.Address{ID: peer.String()},
		Timestamp: time.Now(),
		Meta:      map[string]string{"wa_call_id": live.ID(), "wa_call": direction},
	}
	if direction == core.CallOutgoing {
		item.FromMe = true
	}
	item = a.enrichItem(ctx, item, peer, peer, "call-"+live.ID(), "")

	rec := &callRecord{
		live: live,
		item: item,
		info: core.Call{
			ID:        live.ID(),
			Channel:   core.ChannelWhatsApp,
			Account:   a.account,
			Peer:      item.Thread,
			PeerName:  item.ThreadName,
			Direction: direction,
			State:     state,
			StartedAt: item.Timestamp,
		},
	}
	a.callMu.Lock()
	a.liveCalls[live.ID()] = rec
	a.callMu.Unlock()

	id := live.ID()
	live.OnReady(func() { a.startCallAudio(id) })
	live.OnEnd(func(reason string) { a.endCall(context.Background(), id, reason) })
	a.writeCallItem(ctx, rec)
	return rec.info
}

// setCallState moves callID to state, optionally marking it answered.
func (a *Adapter) setCallState(callID, state string, answered bool) core.Call {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	rec, ok := a.liveCalls[callID]
	if !ok {
		return core.Call{}
	}
	// Media can start (OnReady -> active) before the accept/answer path
	// gets here; never move an active call back to connecting.
	if rec.info.State != core.CallStateActive {
		rec.info.State = state
	}
	rec.answered = rec.answered || answered
	if answered && rec.info.State != core.CallStateActive && rec.mediaTimer == nil {
		rec.mediaTimer = a.afterFunc(callMediaTimeout, func() { a.noMedia(callID) })
	}
	return rec.info
}

// noMedia flags a call that was answered but never received a packet from
// the peer: the local audio setup may be fine, the network, the relay or
// the codec is not. It is a diagnosis, not a hangup.
func (a *Adapter) noMedia(callID string) {
	a.callMu.Lock()
	rec, ok := a.liveCalls[callID]
	flag := ok && rec.info.State != core.CallStateActive && rec.info.AudioError == ""
	if flag {
		rec.info.AudioError = errNoMedia
	}
	a.callMu.Unlock()
	if flag {
		a.callLog().Warn("answered call received no media", "call_id", callID, "after", callMediaTimeout.String(),
			"hint", "no RTP from the relay: check network/firewall (UDP), not local audio")
	}
}

// setAudioError records why callID has no sound (first problem wins, it is
// the root cause) and logs every one.
func (a *Adapter) setAudioError(callID, short string, err error) {
	a.callLog().Error("call audio problem", "call_id", callID, "problem", short, "error", err)
	a.callMu.Lock()
	defer a.callMu.Unlock()
	if rec, ok := a.liveCalls[callID]; ok && rec.info.AudioError == "" {
		rec.info.AudioError = short
	}
}

// audioOpenProblem is the short, user-facing text for an Open failure.
func audioOpenProblem(err error) string {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return "falta el programa de audio, revisa call_capture_command y call_playback_command"
	}
	msg := strings.TrimPrefix(err.Error(), "whatsapp: ")
	return "no se pudo abrir el audio: " + msg
}

// startCallAudio opens the microphone and speaker once media flows.
func (a *Adapter) startCallAudio(callID string) {
	a.callMu.Lock()
	rec, ok := a.liveCalls[callID]
	audio := a.callAudio
	opened := false
	if ok {
		rec.info.State = core.CallStateActive
		rec.answered = true
		if rec.info.ConnectedAt.IsZero() {
			rec.info.ConnectedAt = time.Now()
		}
		if rec.mediaTimer != nil {
			rec.mediaTimer()
		}
		if rec.info.AudioError == errNoMedia {
			rec.info.AudioError = ""
		}
		opened = rec.audioOpened
		rec.audioOpened = true
	}
	a.callMu.Unlock()
	if !ok {
		return
	}
	a.callLog().Info("call media is flowing", "call_id", callID)
	if audio == nil || opened {
		return
	}
	src, sink, err := audio.Open(func(short string, err error) { a.setAudioError(callID, short, err) })
	if err != nil {
		a.setAudioError(callID, audioOpenProblem(err), err)
		return
	}
	a.callMu.Lock()
	if _, still := a.liveCalls[callID]; !still {
		a.callMu.Unlock()
		closeAudio(src, sink)
		return
	}
	rec.src, rec.sink = src, sink
	a.callMu.Unlock()
	rec.live.AttachAudio(src, sink)
}

// endCall forgets callID, releases its audio and rewrites its item as a
// finished (or missed) call. It is idempotent: a local hangup and the
// engine's own OnEnd both land here.
func (a *Adapter) endCall(ctx context.Context, callID, reason string) core.Call {
	a.callMu.Lock()
	rec, ok := a.liveCalls[callID]
	if ok {
		delete(a.liveCalls, callID)
		rec.info.State = core.CallStateEnded
		rec.info.EndReason = reason
		rec.info.EndedAt = time.Now()
		if rec.mediaTimer != nil {
			rec.mediaTimer()
		}
	}
	a.callMu.Unlock()
	if !ok {
		return core.Call{}
	}
	closeAudio(rec.src, rec.sink)
	a.writeCallItem(ctx, rec)
	return rec.info
}

// endCallOr ends rec's call and returns its final state, even when the
// engine's own OnEnd already ended it while Reject/Hangup ran.
func (a *Adapter) endCallOr(ctx context.Context, rec *callRecord, reason string) core.Call {
	if info := a.endCall(ctx, rec.info.ID, reason); info.ID != "" {
		return info
	}
	a.callMu.Lock()
	defer a.callMu.Unlock()
	info := rec.info
	info.State = core.CallStateEnded
	if info.EndReason == "" {
		info.EndReason = reason
	}
	if info.EndedAt.IsZero() {
		info.EndedAt = time.Now()
	}
	return info
}

// writeCallItem upserts rec's conversation item: its body says what
// happened, and only a missed incoming call stays unread.
func (a *Adapter) writeCallItem(ctx context.Context, rec *callRecord) {
	item := rec.item
	item.Meta = map[string]string{
		"wa_call_id": rec.info.ID,
		"wa_call":    rec.info.Direction,
		"wa_state":   rec.info.State,
	}
	incoming := rec.info.Direction == core.CallIncoming
	switch {
	case rec.info.State != core.CallStateEnded && incoming:
		item.Body = "📞 Llamada entrante"
		item.Unread = true
	case rec.info.State != core.CallStateEnded:
		item.Body = "📞 Llamada saliente"
	case incoming && !rec.answered:
		item.Body = "📞 Llamada perdida"
		item.Unread = true
	case rec.info.ConnectedAt.IsZero() && !incoming:
		item.Body = "📞 Llamada sin respuesta"
	default:
		item.Body = "📞 Llamada finalizada (" + core.FormatCallDuration(rec.info.Duration(time.Now())) + ")"
	}
	a.cacheItem(item)

	a.mu.Lock()
	sink := a.sink
	a.mu.Unlock()
	if sink == nil {
		return
	}
	if err := sink.Upsert(ctx, item); err != nil {
		core.LogSinkError(core.ChannelWhatsApp, a.account, "upsert_call", err)
	}
}

func closeAudio(src meowcaller.AudioSource, sink meowcaller.AudioSink) {
	if src != nil {
		src.Close()
	}
	if sink != nil {
		sink.Close()
	}
}

// hangupAll ends every live call, when Run stops.
func (a *Adapter) hangupAll() {
	a.callMu.Lock()
	recs := make([]*callRecord, 0, len(a.liveCalls))
	for _, rec := range a.liveCalls {
		recs = append(recs, rec)
	}
	a.callMu.Unlock()
	for _, rec := range recs {
		_ = rec.live.Hangup()
		a.endCall(context.Background(), rec.info.ID, "shutdown")
	}
}

var _ core.Caller = (*Adapter)(nil)

// callNumber turns a call target into the phone number the call engine
// dials: a number (with or without "+"), or a contact's phone-number JID
// as "bunker contacts" lists it. Groups and other JIDs cannot be called.
func callNumber(to string) (string, error) {
	to = strings.TrimSpace(to)
	if !strings.Contains(to, "@") {
		return strings.TrimPrefix(to, "+"), nil
	}
	jid, err := types.ParseJID(to)
	if err != nil {
		return "", fmt.Errorf("whatsapp: call %q: %w", to, err)
	}
	if jid.Server != types.DefaultUserServer || jid.User == "" {
		return "", fmt.Errorf("whatsapp: call %q: only a person can be called, not a group or a %s address: %w", to, jid.Server, core.ErrUnsupported)
	}
	return jid.User, nil
}
