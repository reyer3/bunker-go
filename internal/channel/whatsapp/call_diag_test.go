package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/purpshell/meowcaller"

	"github.com/reyer3/bunker-go/internal/core"
)

// hookAudio wraps a commandAudio and forwards every helper problem to a
// channel, so tests wait for the event instead of sleeping.
type hookAudio struct {
	commandAudio
	problems chan string
	errs     chan error
	sinkOut  chan meowcaller.AudioSink
}

func newHookAudio(c commandAudio) *hookAudio {
	return &hookAudio{commandAudio: c, problems: make(chan string, 8), errs: make(chan error, 8), sinkOut: make(chan meowcaller.AudioSink, 1)}
}

func (h *hookAudio) Open(problem func(string, error)) (meowcaller.AudioSource, meowcaller.AudioSink, error) {
	src, sink, err := h.commandAudio.Open(func(short string, err error) {
		if problem != nil {
			problem(short, err)
		}
		h.problems <- short
		h.errs <- err
	})
	if sink != nil {
		h.sinkOut <- sink
	}
	return src, sink, err
}

func wait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func TestPlaceCallRefusedWithoutAudioTools(t *testing.T) {
	a, engine, audio, _ := newCallAdapter(t)
	audio.checkErr = errors.New("whatsapp: call audio: parec not found in PATH; install pulseaudio-utils")
	engine.next = &fakeLiveCall{id: "CALL1", peer: callPeer}
	_, err := a.PlaceCall(context.Background(), "51999888777")
	if err == nil || !strings.Contains(err.Error(), "parec not found") {
		t.Fatalf("PlaceCall = %v, want the missing-binary error", err)
	}
	if len(engine.targets) != 0 {
		t.Fatal("the engine was dialed although no audio was possible")
	}
}

func TestAnswerRefusedWithoutAudioToolsKeepsRinging(t *testing.T) {
	a, engine, audio, _ := newCallAdapter(t)
	live := &fakeLiveCall{id: "IN1", peer: callPeer}
	engine.incoming(live)
	audio.checkErr = errors.New("whatsapp: call audio: pacat not found in PATH")
	if _, err := a.ControlCall(context.Background(), "IN1", core.CallAnswer); err == nil || !strings.Contains(err.Error(), "pacat not found") {
		t.Fatalf("answer = %v, want the missing-binary error", err)
	}
	if live.answered != 0 || a.ActiveCalls()[0].State != core.CallStateRinging {
		t.Fatalf("call answered or moved: answered=%d calls=%+v", live.answered, a.ActiveCalls())
	}
	// It can still be rejected.
	if _, err := a.ControlCall(context.Background(), "IN1", core.CallReject); err != nil {
		t.Fatalf("reject: %v", err)
	}
}

func TestCommandAudioCheck(t *testing.T) {
	err := commandAudio{capture: []string{"bunker-no-such-tool-capture"}, playback: []string{"bunker-no-such-tool-play"}}.Check()
	if err == nil || !strings.Contains(err.Error(), "bunker-no-such-tool-capture not found") || !strings.Contains(err.Error(), "bunker-no-such-tool-play not found") {
		t.Fatalf("Check() = %v, want both missing tools named", err)
	}
	err = commandAudio{capture: []string{"parec-not-installed-xyz"}}.Check()
	if err == nil {
		t.Fatal("missing capture command passed Check")
	}
	if err := (commandAudio{}).Check(); err == nil || !strings.Contains(err.Error(), "no sound") {
		t.Fatalf("Check() with no commands = %v", err)
	}
	if err := (commandAudio{capture: []string{"sh"}}).Check(); err != nil {
		t.Fatalf("Check() with sh = %v", err)
	}
	if err := missingHelperError("capture", "/usr/bin/parec"); !strings.Contains(err.Error(), "pulseaudio-utils") || !strings.Contains(err.Error(), "call_capture_command") {
		t.Fatalf("missing parec hint = %v", err)
	}
}

func TestOpenFailureSurfacesAudioError(t *testing.T) {
	a, engine, audio, _ := newCallAdapter(t)
	live := &fakeLiveCall{id: "CALL1", peer: callPeer}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), "51999888777"); err != nil {
		t.Fatal(err)
	}
	audio.openErr = missingHelperError("capture", "parec")
	live.onReady()
	c := a.ActiveCalls()[0]
	if c.State != core.CallStateActive || c.AudioError == "" {
		t.Fatalf("call = %+v, want active with an audio error", c)
	}
}

func TestOnReadyTwiceOpensAudioOnce(t *testing.T) {
	a, engine, audio, _ := newCallAdapter(t)
	live := &fakeLiveCall{id: "CALL1", peer: callPeer}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), "51999888777"); err != nil {
		t.Fatal(err)
	}
	live.onReady()
	live.onReady()
	if audio.opens != 1 {
		t.Fatalf("opens = %d, want 1", audio.opens)
	}
}

func TestHelperDeathMidCallReportsExitAndStderr(t *testing.T) {
	audio := newHookAudio(commandAudio{
		capture:  []string{"sh", "-c", "exec sleep 30"},
		playback: []string{"sh", "-c", "echo 'pacat: Connection refused' >&2; exit 3"},
	})
	a := newTestAdapter("personal", &fakeWAClient{linked: true})
	a.sink = newSpySink()
	engine := &fakeCallEngine{}
	a.EnableCalls(engine, audio)
	live := &fakeLiveCall{id: "CALL1", peer: callPeer}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), "51999888777"); err != nil {
		t.Fatal(err)
	}
	live.onReady()

	short := wait(t, audio.problems, "helper exit report")
	if !strings.Contains(short, "terminó") || !strings.Contains(short, "exit status 3") || !strings.Contains(short, "Connection refused") {
		t.Fatalf("short report = %q", short)
	}
	err := wait(t, audio.errs, "helper exit error")
	if !strings.Contains(err.Error(), "Connection refused") {
		t.Fatalf("full error = %v", err)
	}
	if got := a.ActiveCalls()[0].AudioError; got != short {
		t.Fatalf("AudioError = %q, want %q", got, short)
	}
	a.hangupAll()
}

func TestPlaybackWriteErrorReportedOnce(t *testing.T) {
	audio := newHookAudio(commandAudio{playback: []string{"sh", "-c", "exit 0"}})
	_, sink, err := audio.Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	ps := sink.(*pcmSink)
	<-ps.h.done // the helper is gone: every write is now an EPIPE
	frame := make([]float32, meowcaller.FrameSamples)
	var failures int
	for i := 0; i < 100; i++ {
		if ps.WriteFrame(frame) != nil {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("WriteFrame failed %d times, want exactly once (then frames are dropped quietly)", failures)
	}
	wait(t, audio.problems, "the single report")
	select {
	case extra := <-audio.problems:
		t.Fatalf("second report: %q", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMissingBinaryAtOpen(t *testing.T) {
	_, _, err := commandAudio{capture: []string{"bunker-no-such-tool-capture"}}.Open(nil)
	if err == nil || !strings.Contains(err.Error(), "not found in PATH") {
		t.Fatalf("Open = %v", err)
	}
	if got := audioOpenProblem(err); !strings.Contains(got, "audio") {
		t.Fatalf("audioOpenProblem = %q", got)
	}
}

func TestTailBufferIsBounded(t *testing.T) {
	b := &tailBuffer{max: 8}
	_, _ = b.Write([]byte("0123456789"))
	_, _ = b.Write([]byte("abc\n"))
	if got := b.String(); got != "6789abc" || len(b.buf) > 8 {
		t.Fatalf("tail = %q (%d bytes)", got, len(b.buf))
	}
	if lastLine("a\nb\n\n") != "b" {
		t.Fatal("lastLine")
	}
}

// fakeTimers collects the watchdog timers so tests fire them by hand.
type fakeTimers struct {
	mu      sync.Mutex
	fns     []func()
	stopped int
	delays  []time.Duration
}

func (f *fakeTimers) after(d time.Duration, fn func()) func() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fns = append(f.fns, fn)
	f.delays = append(f.delays, d)
	return func() bool { f.mu.Lock(); f.stopped++; f.mu.Unlock(); return true }
}

func TestNoMediaWatchdog(t *testing.T) {
	a, engine, audio, _ := newCallAdapter(t)
	timers := &fakeTimers{}
	a.callAfter = timers.after
	live := &fakeLiveCall{id: "CALL1", peer: callPeer}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), "51999888777"); err != nil {
		t.Fatal(err)
	}
	if len(timers.fns) != 0 {
		t.Fatal("watchdog armed while still ringing")
	}
	live.onPeerAccept()
	if len(timers.fns) != 1 || timers.delays[0] != callMediaTimeout {
		t.Fatalf("timers = %v", timers.delays)
	}

	timers.fns[0]() // ten silent seconds
	c := a.ActiveCalls()[0]
	if c.AudioError != errNoMedia || c.State != core.CallStateConnecting {
		t.Fatalf("call after timeout = %+v", c)
	}

	// Late media clears the diagnosis and opens audio.
	live.onReady()
	c = a.ActiveCalls()[0]
	if c.AudioError != "" || c.State != core.CallStateActive || audio.opens != 1 {
		t.Fatalf("call after late media = %+v opens=%d", c, audio.opens)
	}
}

func TestNoMediaWatchdogStoppedByMediaAndHangup(t *testing.T) {
	a, engine, _, _ := newCallAdapter(t)
	timers := &fakeTimers{}
	a.callAfter = timers.after
	live := &fakeLiveCall{id: "CALL1", peer: callPeer}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), "51999888777"); err != nil {
		t.Fatal(err)
	}
	live.onPeerAccept()
	live.onReady()
	if timers.stopped != 1 {
		t.Fatalf("watchdog stopped %d times on media, want 1", timers.stopped)
	}
	timers.fns[0]() // a timer already in flight must not flag an active call
	if got := a.ActiveCalls()[0].AudioError; got != "" {
		t.Fatalf("active call flagged: %q", got)
	}
	if _, err := a.ControlCall(context.Background(), "CALL1", core.CallHangup); err != nil {
		t.Fatal(err)
	}
	if timers.stopped != 2 {
		t.Fatalf("watchdog stopped %d times after hangup, want 2", timers.stopped)
	}
}

func TestIncomingNoMediaAfterAnswer(t *testing.T) {
	a, engine, _, _ := newCallAdapter(t)
	timers := &fakeTimers{}
	a.callAfter = timers.after
	live := &fakeLiveCall{id: "IN1", peer: callPeer}
	engine.incoming(live)
	if _, err := a.ControlCall(context.Background(), "IN1", core.CallAnswer); err != nil {
		t.Fatal(err)
	}
	if len(timers.fns) != 1 {
		t.Fatalf("watchdog not armed on answer: %d", len(timers.fns))
	}
	timers.fns[0]()
	if got := a.ActiveCalls()[0].AudioError; got != errNoMedia {
		t.Fatalf("AudioError = %q", got)
	}
}

// Regression: media can start while Answer is still running (OnReady
// fires from the engine's media loop); the state must stay active instead
// of being moved back to "connecting" when Answer returns, which hid the
// connected call behind "Conectando…" and re-armed the no-media timer.
func TestMediaDuringAnswerKeepsCallActive(t *testing.T) {
	a, engine, audio, _ := newCallAdapter(t)
	timers := &fakeTimers{}
	a.callAfter = timers.after
	live := &fakeLiveCall{id: "IN1", peer: callPeer, readyOnAnswer: true}
	engine.incoming(live)
	call, err := a.ControlCall(context.Background(), "IN1", core.CallAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if call.State != core.CallStateActive || audio.opens != 1 || len(timers.fns) != 0 {
		t.Fatalf("call=%+v opens=%d timers=%d", call, audio.opens, len(timers.fns))
	}
}

func TestPeerAcceptAfterReadyKeepsCallActive(t *testing.T) {
	a, engine, _, _ := newCallAdapter(t)
	live := &fakeLiveCall{id: "CALL1", peer: callPeer}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), "51999888777"); err != nil {
		t.Fatal(err)
	}
	live.onReady()
	live.onPeerAccept()
	if got := a.ActiveCalls()[0].State; got != core.CallStateActive {
		t.Fatalf("state = %q, want active", got)
	}
}

// The wire format the helpers speak must match meowcaller's: 16 kHz mono
// s16le, 960-sample (1920-byte) frames, little endian both ways.
func TestPCMRoundTripMatchesMeowcaller(t *testing.T) {
	if meowcaller.SampleRate != 16000 || meowcaller.FrameSamples != 960 {
		t.Fatalf("meowcaller format changed: %d Hz, %d samples", meowcaller.SampleRate, meowcaller.FrameSamples)
	}
	if !slices.Contains(defaultCaptureCommand, "--rate=16000") || !slices.Contains(defaultPlaybackCommand, "--rate=16000") {
		t.Fatal("default audio commands are not 16 kHz")
	}
	frame := make([]float32, meowcaller.FrameSamples)
	for i := range frame {
		frame[i] = 0.5 * float32(math.Sin(float64(i)/7))
	}
	raw := encodeS16LE(nil, frame, 1)
	if len(raw) != meowcaller.FrameSamples*2 {
		t.Fatalf("encoded frame is %d bytes, want %d", len(raw), meowcaller.FrameSamples*2)
	}
	got, err := meowcaller.PCMStream(io.NopCloser(bytes.NewReader(raw))).ReadFrame()
	if err != nil || len(got) != len(frame) {
		t.Fatalf("ReadFrame = %d samples, %v", len(got), err)
	}
	for i := range frame {
		if d := math.Abs(float64(got[i] - frame[i])); d > 2.0/32768 {
			t.Fatalf("sample %d: %v -> %v (off by %v)", i, frame[i], got[i], d)
		}
	}
}

func TestMeowLoggerRoutesInfoAndAbove(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})).With("account", "personal")
	zl := meowLogger(log)
	zl.Debug().Msg("hidden debug")
	zl.Info().Int("bytes", 42).Msg("first RTP decoded from relay")
	zl.Warn().Msg("failed to write audio")
	out := buf.String()
	if strings.Contains(out, "hidden debug") {
		t.Fatalf("debug leaked: %s", out)
	}
	for _, want := range []string{"meowcaller: first RTP decoded from relay", "bytes=42", "level=WARN", "meowcaller: failed to write audio", "account=personal"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
}

// fake commands: constant 0x4040 samples (~0.5) and digital silence.
const (
	fakeLoudMic   = "head -c 100000 /dev/zero | tr '\\000' '\\100'"
	fakeSilentMic = "head -c 100000 /dev/zero"
)

func TestAudioTestHealthy(t *testing.T) {
	played := filepath.Join(t.TempDir(), "played.pcm")
	r := audioTest(context.Background(), commandAudio{
		capture:  []string{"sh", "-c", fakeLoudMic},
		playback: []string{"sh", "-c", "cat > " + played},
	}, 1)
	if !r.OK || !r.Capture.Started || !r.Playback.Started || r.Capture.Error != "" || r.Playback.Error != "" {
		t.Fatalf("report = %+v", r)
	}
	if r.SecondsRecorded < 0.99 || r.MicPeak < 0.49 || r.MicPeak > 0.51 || r.MicRMS < 0.49 {
		t.Fatalf("levels: %.2fs peak=%v rms=%v", r.SecondsRecorded, r.MicPeak, r.MicRMS)
	}
	data, err := os.ReadFile(played)
	if err != nil || len(data) != AudioTestToneSeconds*meowcaller.SampleRate*2 {
		t.Fatalf("playback received %d bytes (%v), want the tone", len(data), err)
	}
	if !strings.Contains(r.String(), "result: ok") {
		t.Fatalf("text:\n%s", r.String())
	}
}

func TestAudioTestSilentMicHints(t *testing.T) {
	r := audioTest(context.Background(), commandAudio{
		capture:  []string{"sh", "-c", fakeSilentMic},
		playback: []string{"sh", "-c", "cat >/dev/null"},
	}, 1)
	if r.OK || r.MicPeak != 0 || r.MicPeakDBFS != -120 {
		t.Fatalf("report = %+v", r)
	}
	if !strings.Contains(strings.Join(r.Hints, "\n"), "muted") {
		t.Fatalf("hints = %v", r.Hints)
	}
}

func TestAudioTestHelperProblems(t *testing.T) {
	r := audioTest(context.Background(), commandAudio{
		capture:  []string{"bunker-no-such-tool-capture"},
		playback: []string{"sh", "-c", "echo 'no sink' >&2; exit 4"},
	}, 1)
	if r.OK || r.Capture.Found || !strings.Contains(r.Capture.Error, "not found") {
		t.Fatalf("capture = %+v", r.Capture)
	}
	if !r.Playback.Started || !strings.Contains(r.Playback.Error, "exit status 4") || !strings.Contains(r.Playback.Stderr, "no sink") {
		t.Fatalf("playback = %+v", r.Playback)
	}

	r = audioTest(context.Background(), commandAudio{capture: []string{"sh", "-c", "echo 'no source' >&2; exit 2"}}, 1)
	if r.OK || !strings.Contains(r.Capture.Error, "exited early") || !strings.Contains(r.Capture.Stderr, "no source") || !r.Playback.Disabled {
		t.Fatalf("report = %+v", r)
	}
}
