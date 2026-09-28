package whatsapp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

// fakeLiveCall is an in-memory liveCall; tests fire its callbacks by hand.
type fakeLiveCall struct {
	mu                         sync.Mutex
	id                         string
	peer                       types.JID
	answered, rejected, hungUp int
	onReady, onPeerAccept      func()
	onEnd                      func(string)
	src                        meowcaller.AudioSource
	sink                       meowcaller.AudioSink
	endOnHangup                bool // fire onEnd synchronously from Hangup/Reject, like a racing engine
}

func (c *fakeLiveCall) ID() string      { return c.id }
func (c *fakeLiveCall) Peer() types.JID { return c.peer }
func (c *fakeLiveCall) Answer() error   { c.mu.Lock(); c.answered++; c.mu.Unlock(); return nil }
func (c *fakeLiveCall) Reject() error {
	c.mu.Lock()
	c.rejected++
	c.mu.Unlock()
	c.maybeEnd("rejected")
	return nil
}
func (c *fakeLiveCall) Hangup() error {
	c.mu.Lock()
	c.hungUp++
	c.mu.Unlock()
	c.maybeEnd("hangup")
	return nil
}
func (c *fakeLiveCall) OnReady(fn func()) { c.onReady = fn }
func (c *fakeLiveCall) OnPeerAccept(fn func()) {
	c.onPeerAccept = fn
}
func (c *fakeLiveCall) OnEnd(fn func(string)) { c.onEnd = fn }
func (c *fakeLiveCall) AttachAudio(src meowcaller.AudioSource, sink meowcaller.AudioSink) {
	c.src, c.sink = src, sink
}
func (c *fakeLiveCall) maybeEnd(reason string) {
	if c.endOnHangup && c.onEnd != nil {
		c.onEnd(reason)
	}
}

type fakeCallEngine struct {
	incoming func(liveCall)
	next     *fakeLiveCall
	callErr  error
	targets  []string
}

func (e *fakeCallEngine) Call(_ context.Context, target string) (liveCall, error) {
	e.targets = append(e.targets, target)
	if e.callErr != nil {
		return nil, e.callErr
	}
	return e.next, nil
}
func (e *fakeCallEngine) OnIncomingCall(fn func(liveCall)) { e.incoming = fn }

type fakeAudioEnd struct{ closed int }

func (f *fakeAudioEnd) ReadFrame() ([]float32, error) {
	return make([]float32, meowcaller.FrameSamples), nil
}
func (f *fakeAudioEnd) WriteFrame(frame []float32) error { return nil }
func (f *fakeAudioEnd) Close() error                     { f.closed++; return nil }

type fakeCallAudio struct {
	opens int
	src   *fakeAudioEnd
	sink  *fakeAudioEnd
}

func (a *fakeCallAudio) Open() (meowcaller.AudioSource, meowcaller.AudioSink, error) {
	a.opens++
	a.src, a.sink = &fakeAudioEnd{}, &fakeAudioEnd{}
	return a.src, a.sink, nil
}

var callPeer = types.NewJID("51999888777", types.DefaultUserServer)

func newCallAdapter(t *testing.T) (*Adapter, *fakeCallEngine, *fakeCallAudio, *spySink) {
	t.Helper()
	a := newTestAdapter("personal", &fakeWAClient{linked: true})
	sink := newSpySink()
	a.sink = sink
	engine := &fakeCallEngine{}
	audio := &fakeCallAudio{}
	a.EnableCalls(engine, audio)
	return a, engine, audio, sink
}

func lastUpsert(t *testing.T, s *spySink) core.Item {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.upserted) == 0 {
		t.Fatal("no item upserted")
	}
	return s.upserted[len(s.upserted)-1]
}

func TestCallsDisabledByDefault(t *testing.T) {
	a := newTestAdapter("personal", &fakeWAClient{linked: true})
	if err := a.CanCall(); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("CanCall() = %v, want ErrUnsupported", err)
	}
	if _, err := a.PlaceCall(context.Background(), "+51999888777"); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("PlaceCall on a disabled account = %v, want ErrUnsupported", err)
	}
}

func TestPlaceCallLifecycle(t *testing.T) {
	a, engine, audio, sink := newCallAdapter(t)
	live := &fakeLiveCall{id: "CALL1", peer: callPeer}
	engine.next = live

	call, err := a.PlaceCall(context.Background(), " +51999888777 ")
	if err != nil {
		t.Fatalf("PlaceCall: %v", err)
	}
	if engine.targets[0] != "51999888777" {
		t.Fatalf("engine target = %q, want the bare number", engine.targets[0])
	}
	if call.ID != "CALL1" || call.Direction != core.CallOutgoing || call.State != core.CallStateCalling {
		t.Fatalf("call = %+v", call)
	}
	item := lastUpsert(t, sink)
	if !item.FromMe || item.Unread || !strings.Contains(item.Body, "saliente") || item.Thread != callPeer.String() {
		t.Fatalf("outgoing call item = %+v", item)
	}
	if audio.opens != 0 {
		t.Fatal("audio opened before media was ready")
	}

	live.onPeerAccept()
	if got := a.ActiveCalls()[0].State; got != core.CallStateConnecting {
		t.Fatalf("state after peer accept = %q", got)
	}
	live.onReady()
	if audio.opens != 1 || live.src == nil || live.sink == nil {
		t.Fatalf("audio not attached on ready: opens=%d", audio.opens)
	}
	if got := a.ActiveCalls()[0].State; got != core.CallStateActive {
		t.Fatalf("state after ready = %q", got)
	}

	ended, err := a.ControlCall(context.Background(), "CALL1", core.CallHangup)
	if err != nil {
		t.Fatalf("hangup: %v", err)
	}
	if ended.State != core.CallStateEnded || live.hungUp != 1 {
		t.Fatalf("after hangup call=%+v hungUp=%d", ended, live.hungUp)
	}
	if audio.src.closed != 1 || audio.sink.closed != 1 {
		t.Fatal("audio not released on hangup")
	}
	if len(a.ActiveCalls()) != 0 {
		t.Fatal("call still active after hangup")
	}
	if item := lastUpsert(t, sink); !strings.Contains(item.Body, "finalizada") {
		t.Fatalf("final item body = %q", item.Body)
	}
}

func TestPlaceCallRefusesSecondCall(t *testing.T) {
	a, engine, _, _ := newCallAdapter(t)
	engine.next = &fakeLiveCall{id: "CALL1", peer: callPeer}
	if _, err := a.PlaceCall(context.Background(), "51999888777"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PlaceCall(context.Background(), "51999888777"); !errors.Is(err, errCallBusy) {
		t.Fatalf("second PlaceCall = %v, want errCallBusy", err)
	}
}

func TestIncomingCallMissed(t *testing.T) {
	a, engine, audio, sink := newCallAdapter(t)
	live := &fakeLiveCall{id: "IN1", peer: callPeer}
	engine.incoming(live)

	calls := a.ActiveCalls()
	if len(calls) != 1 || calls[0].State != core.CallStateRinging || calls[0].Direction != core.CallIncoming {
		t.Fatalf("ActiveCalls = %+v", calls)
	}
	item := lastUpsert(t, sink)
	if !item.Unread || !strings.Contains(item.Body, "entrante") || item.Meta["wa_call_id"] != "IN1" {
		t.Fatalf("ringing item = %+v", item)
	}
	if live.answered != 0 {
		t.Fatal("incoming call answered on its own")
	}

	live.onEnd("timeout")
	item = lastUpsert(t, sink)
	if !item.Unread || !strings.Contains(item.Body, "perdida") {
		t.Fatalf("missed item = %+v", item)
	}
	if audio.opens != 0 || len(a.ActiveCalls()) != 0 {
		t.Fatal("missed call left audio or state behind")
	}
}

func TestIncomingCallAnswerAndReject(t *testing.T) {
	a, engine, _, sink := newCallAdapter(t)
	live := &fakeLiveCall{id: "IN1", peer: callPeer}
	engine.incoming(live)

	call, err := a.ControlCall(context.Background(), "IN1", core.CallAnswer)
	if err != nil || live.answered != 1 || call.State != core.CallStateConnecting {
		t.Fatalf("answer: call=%+v err=%v answered=%d", call, err, live.answered)
	}
	if _, err := a.ControlCall(context.Background(), "IN1", core.CallReject); err == nil {
		t.Fatal("reject of an answered call succeeded")
	}
	live.onEnd("peer hangup")
	if item := lastUpsert(t, sink); item.Unread || !strings.Contains(item.Body, "finalizada") {
		t.Fatalf("answered call item = %+v", item)
	}

	second := &fakeLiveCall{id: "IN2", peer: callPeer, endOnHangup: true}
	engine.incoming(second)
	call, err = a.ControlCall(context.Background(), "IN2", core.CallReject)
	if err != nil || second.rejected != 1 || call.State != core.CallStateEnded {
		t.Fatalf("reject: call=%+v err=%v", call, err)
	}
}

func TestControlUnknownCall(t *testing.T) {
	a, _, _, _ := newCallAdapter(t)
	if _, err := a.ControlCall(context.Background(), "nope", core.CallHangup); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("ControlCall(unknown) = %v, want ErrNotFound", err)
	}
}

func TestEncodeS16LE(t *testing.T) {
	got := encodeS16LE(nil, []float32{0, 1, -1, 2})
	want := []byte{0, 0, 0xff, 0x7f, 0x01, 0x80, 0xff, 0x7f}
	if string(got) != string(want) {
		t.Fatalf("encodeS16LE = %x, want %x", got, want)
	}
}

func TestCallOptions(t *testing.T) {
	opts := map[string]interface{}{
		"calls":                 true,
		"call_capture_command":  []interface{}{"arecord", "-"},
		"call_playback_command": []interface{}{},
	}
	if !boolOption(opts, "calls", false) {
		t.Fatal("calls option not read")
	}
	if got := stringSliceOption(opts, "call_capture_command", nil); len(got) != 2 || got[0] != "arecord" {
		t.Fatalf("capture = %v", got)
	}
	if got := stringSliceOption(opts, "call_playback_command", defaultPlaybackCommand); len(got) != 0 {
		t.Fatalf("empty playback = %v, want empty", got)
	}
	if got := stringSliceOption(opts, "missing", defaultCaptureCommand); got[0] != "parec" {
		t.Fatalf("default capture = %v", got)
	}
}

func TestCommandAudioPipesPCM(t *testing.T) {
	audio := commandAudio{
		capture:  []string{"sh", "-c", "head -c 1920 /dev/zero"},
		playback: []string{"sh", "-c", "cat >/dev/null"},
	}
	src, sink, err := audio.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	frame, err := src.ReadFrame()
	if err != nil || len(frame) != meowcaller.FrameSamples {
		t.Fatalf("ReadFrame = %d samples, %v", len(frame), err)
	}
	if err := sink.WriteFrame(frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	closeAudio(src, sink)
	if err := sink.WriteFrame(frame); err == nil {
		t.Fatal("WriteFrame after Close succeeded")
	}
}

func TestCommandAudioEmptyDisablesDirection(t *testing.T) {
	src, sink, err := commandAudio{}.Open()
	if err != nil || src != nil || sink != nil {
		t.Fatalf("Open() = %v, %v, %v; want nil, nil, nil", src, sink, err)
	}
}
