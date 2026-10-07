package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	readyOnAnswer              bool // fire onReady from inside Answer, like media racing the answer
	video                      bool // the offer (or a later upgrade) carries video
	// events records Answer and SetVideoEnabled in order, so a test can
	// tell the camera was turned off only after the call was answered.
	events   []string
	videoErr error // SetVideoEnabled's answer
}

func (c *fakeLiveCall) ID() string      { return c.id }
func (c *fakeLiveCall) Peer() types.JID { return c.peer }
func (c *fakeLiveCall) IsVideo() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.video
}
func (c *fakeLiveCall) setVideo(v bool) {
	c.mu.Lock()
	c.video = v
	c.mu.Unlock()
}
func (c *fakeLiveCall) Answer() error {
	c.mu.Lock()
	c.answered++
	c.events = append(c.events, "answer")
	c.mu.Unlock()
	if c.readyOnAnswer && c.onReady != nil {
		c.onReady()
	}
	return nil
}
func (c *fakeLiveCall) SetVideoEnabled(enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, fmt.Sprintf("video=%t", enabled))
	return c.videoErr
}
func (c *fakeLiveCall) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.events...)
}
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
	opens    int
	src      *fakeAudioEnd
	sink     *fakeAudioEnd
	openErr  error
	checkErr error
	problem  func(short string, err error)
}

func (a *fakeCallAudio) Open(problem func(string, error)) (meowcaller.AudioSource, meowcaller.AudioSink, error) {
	a.opens++
	a.problem = problem
	if a.openErr != nil {
		return nil, nil, a.openErr
	}
	a.src, a.sink = &fakeAudioEnd{}, &fakeAudioEnd{}
	return a.src, a.sink, nil
}

func (a *fakeCallAudio) Check() error { return a.checkErr }

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

func TestIncomingVideoCallIsMarkedVideo(t *testing.T) {
	a, engine, _, sink := newCallAdapter(t)
	live := &fakeLiveCall{id: "VID1", peer: callPeer, video: true}
	engine.incoming(live)

	calls := a.ActiveCalls()
	if len(calls) != 1 || !calls[0].Video {
		t.Fatalf("ActiveCalls = %+v, want one video call", calls)
	}
	item := lastUpsert(t, sink)
	if item.Body != "📹 Videollamada entrante" || item.Meta["wa_video"] != "true" || !item.Unread {
		t.Fatalf("ringing video item = %+v", item)
	}

	live.onEnd("timeout")
	if item := lastUpsert(t, sink); item.Body != "📹 Videollamada perdida" || item.Meta["wa_video"] != "true" {
		t.Fatalf("missed video item = %+v", item)
	}
}

func TestVoiceCallIsNotMarkedVideo(t *testing.T) {
	a, engine, _, sink := newCallAdapter(t)
	engine.incoming(&fakeLiveCall{id: "IN1", peer: callPeer})

	if calls := a.ActiveCalls(); len(calls) != 1 || calls[0].Video {
		t.Fatalf("ActiveCalls = %+v, want one voice call", calls)
	}
	item := lastUpsert(t, sink)
	if item.Body != "📞 Llamada entrante" {
		t.Fatalf("voice item body = %q", item.Body)
	}
	if _, ok := item.Meta["wa_video"]; ok {
		t.Fatalf("voice item Meta = %+v, want no wa_video", item.Meta)
	}
}

// A voice call the peer upgrades to video is marked video from the next
// state change on, and stays so once ended (the engine forgets the call
// then, so it would read as voice again).
func TestCallUpgradedToVideoIsMarkedVideo(t *testing.T) {
	a, engine, _, sink := newCallAdapter(t)
	live := &fakeLiveCall{id: "OUT1", peer: callPeer}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), "51999888777"); err != nil {
		t.Fatal(err)
	}
	live.onPeerAccept()
	if a.ActiveCalls()[0].Video {
		t.Fatal("voice call marked video before any upgrade")
	}
	live.setVideo(true)
	live.onReady()
	if !a.ActiveCalls()[0].Video {
		t.Fatal("upgraded call not marked video once media flowed")
	}
	live.setVideo(false)
	ended, err := a.ControlCall(context.Background(), "OUT1", core.CallHangup)
	if err != nil || !ended.Video {
		t.Fatalf("ended call = %+v (%v), want it still marked video", ended, err)
	}
	if item := lastUpsert(t, sink); !strings.HasPrefix(item.Body, "📹 Videollamada finalizada (") {
		t.Fatalf("final item body = %q", item.Body)
	}
}

// Answering a video offer must leave our camera off: bunker sends no
// frames yet, so the peer would otherwise stare at a frozen picture. The
// camera goes off once media flows (after the deferred accept), never
// before the answer, and only once.
func TestAnsweredVideoCallTurnsCameraOff(t *testing.T) {
	a, engine, _, _ := newCallAdapter(t)
	live := &fakeLiveCall{id: "VID1", peer: callPeer, video: true}
	engine.incoming(live)

	if _, err := a.ControlCall(context.Background(), "VID1", core.CallAnswer); err != nil {
		t.Fatal(err)
	}
	live.onReady()
	live.onReady()

	if got, want := live.recorded(), []string{"answer", "video=false"}; !slices.Equal(got, want) {
		t.Fatalf("live call events = %v, want %v", got, want)
	}
	call := a.ActiveCalls()[0]
	if !call.Video || call.State != core.CallStateActive || call.AudioError != "" {
		t.Fatalf("answered video call = %+v, want an active video call with no error", call)
	}
}

func TestAnsweredVoiceCallLeavesVideoAlone(t *testing.T) {
	a, engine, _, _ := newCallAdapter(t)
	live := &fakeLiveCall{id: "IN1", peer: callPeer}
	engine.incoming(live)

	if _, err := a.ControlCall(context.Background(), "IN1", core.CallAnswer); err != nil {
		t.Fatal(err)
	}
	live.onReady()

	if got, want := live.recorded(), []string{"answer"}; !slices.Equal(got, want) {
		t.Fatalf("live call events = %v, want %v", got, want)
	}
}

// A camera that cannot be turned off costs the peer a frozen picture, not
// the call: audio still starts, and the failure is logged loudly.
func TestCameraOffFailureKeepsCallWithAudio(t *testing.T) {
	logs := captureSlogDefault(t)
	a, engine, audio, _ := newCallAdapter(t)
	live := &fakeLiveCall{id: "VID1", peer: callPeer, video: true, videoErr: errors.New("relay gone")}
	engine.incoming(live)

	if _, err := a.ControlCall(context.Background(), "VID1", core.CallAnswer); err != nil {
		t.Fatal(err)
	}
	live.onReady()

	call := a.ActiveCalls()[0]
	if call.State != core.CallStateActive || !call.Video {
		t.Fatalf("call = %+v, want it active and still video", call)
	}
	if audio.opens != 1 || live.src == nil || live.sink == nil {
		t.Fatalf("audio opens=%d src=%v sink=%v, want audio attached", audio.opens, live.src, live.sink)
	}
	out := logs.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "relay gone") || !strings.Contains(out, "camera") {
		t.Fatalf("log = %q, want an error about the camera with its cause", out)
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
	got := encodeS16LE(nil, []float32{0, 1, -1, 2}, 0)
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
	src, sink, err := audio.Open(nil)
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
	src, sink, err := commandAudio{}.Open(nil)
	if err != nil || src != nil || sink != nil {
		t.Fatalf("Open() = %v, %v, %v; want nil, nil, nil", src, sink, err)
	}
}

func TestCallDurationCountsFromConnect(t *testing.T) {
	a, engine, _, sink := newCallAdapter(t)
	live := &fakeLiveCall{id: "CALL1", peer: callPeer}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), "51999888777"); err != nil {
		t.Fatal(err)
	}
	if c := a.ActiveCalls()[0]; !c.ConnectedAt.IsZero() {
		t.Fatalf("ConnectedAt set while ringing: %+v", c)
	}
	live.onReady()
	c := a.ActiveCalls()[0]
	if c.ConnectedAt.IsZero() || c.ConnectedAt.Before(c.StartedAt) {
		t.Fatalf("ConnectedAt = %v, StartedAt = %v", c.ConnectedAt, c.StartedAt)
	}
	ended, _ := a.ControlCall(context.Background(), "CALL1", core.CallHangup)
	if ended.EndedAt.IsZero() {
		t.Fatalf("EndedAt not set: %+v", ended)
	}
	if item := lastUpsert(t, sink); item.Body != "📞 Llamada finalizada (0:00)" {
		t.Fatalf("body = %q", item.Body)
	}
}

func TestOutgoingCallUnanswered(t *testing.T) {
	a, engine, _, sink := newCallAdapter(t)
	live := &fakeLiveCall{id: "CALL1", peer: callPeer}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), "51999888777"); err != nil {
		t.Fatal(err)
	}
	live.onEnd("timeout")
	if item := lastUpsert(t, sink); item.Body != "📞 Llamada sin respuesta" || item.Unread {
		t.Fatalf("item = %+v", item)
	}
}

func TestCallGain(t *testing.T) {
	got := encodeS16LE(nil, []float32{0.25, 0.75}, 2)
	want := encodeS16LE(nil, []float32{0.5, 1}, 1)
	if string(got) != string(want) {
		t.Fatalf("gain 2 = %x, want %x", got, want)
	}
	src := gainSource{AudioSource: &fakeAudioEnd{}, gain: 3}
	if frame, err := src.ReadFrame(); err != nil || len(frame) != meowcaller.FrameSamples {
		t.Fatalf("gainSource.ReadFrame = %d, %v", len(frame), err)
	}

	opts := map[string]interface{}{"i": int64(2), "f": 1.5, "neg": -1.0, "big": 100.0, "s": "x"}
	for key, want := range map[string]float32{"i": 2, "f": 1.5, "neg": 1, "big": maxCallGain, "s": 1, "missing": 1} {
		if got := gainOption(opts, key); got != want {
			t.Errorf("gainOption(%q) = %v, want %v", key, got, want)
		}
	}
}

// TestIncomingCallFromDeviceUsesPersonJID: a call offer arrives from the
// caller's device JID (number:device@server). The call item must key the
// person's chat, not the device, and resolve the person's saved name, so
// it lands in the existing conversation instead of creating a separate,
// number-named chat and contact.
func TestIncomingCallFromDeviceUsesPersonJID(t *testing.T) {
	a, engine, _, sink := newCallAdapter(t)
	names := newFakeNameResolver()
	names.contacts[callPeer] = types.ContactInfo{Found: true, FullName: "Ana Ejemplo"}
	a.SetNameResolver(names)

	device := callPeer
	device.Device = 16
	engine.incoming(&fakeLiveCall{id: "IN1", peer: device})

	item := lastUpsert(t, sink)
	if item.Thread != callPeer.String() || item.From.ID != callPeer.String() {
		t.Errorf("call item Thread=%q From=%q, want both %q", item.Thread, item.From.ID, callPeer.String())
	}
	if item.ID != itemID("personal", callPeer.String(), "call-IN1") {
		t.Errorf("call item ID = %q, want it keyed on the person's JID", item.ID)
	}
	if item.ThreadName != "Ana Ejemplo" {
		t.Errorf("call item ThreadName = %q, want %q", item.ThreadName, "Ana Ejemplo")
	}
	calls := a.ActiveCalls()
	if len(calls) != 1 || calls[0].Peer != callPeer.String() || calls[0].PeerName != "Ana Ejemplo" {
		t.Errorf("ActiveCalls = %+v, want peer %q named %q", calls, callPeer.String(), "Ana Ejemplo")
	}
}
