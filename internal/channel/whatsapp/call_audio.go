package whatsapp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/purpshell/meowcaller"
)

// Default audio commands: PulseAudio's parec/pacat, which PipeWire also
// serves through pipewire-pulse. Raw s16le mono at meowcaller.SampleRate
// flows over their stdout/stdin, so bunker stays pure Go (no CGO audio
// bindings) and any other tool speaking the same format can replace them
// through call_capture_command / call_playback_command.
var (
	defaultCaptureCommand  = []string{"parec", "--raw", "--format=s16le", "--rate=16000", "--channels=1", "--latency-msec=60"}
	defaultPlaybackCommand = []string{"pacat", "--playback", "--raw", "--format=s16le", "--rate=16000", "--channels=1", "--latency-msec=60"}
)

const (
	// helperStderrTail bounds how much of a helper's stderr is kept for
	// its exit report.
	helperStderrTail = 2048
	// helperWaitDelay bounds how long reaping a killed helper waits for
	// its stderr to close (a grandchild may hold it open).
	helperWaitDelay = time.Second
	// helperDeathGrace is how long a failed playback write waits for the
	// helper's exit status, so the report carries its stderr instead of
	// a bare "broken pipe".
	helperDeathGrace = 500 * time.Millisecond
)

// commandAudio implements callAudio by spawning one capture process
// (microphone -> stdout) and one playback process (stdin -> speaker) per
// call. An empty command disables that direction. captureGain and
// playbackGain scale the samples in software (0 means 1, unchanged), so
// volume can be raised with any capture/playback tool, not only those
// with a volume flag of their own.
type commandAudio struct {
	capture      []string
	playback     []string
	captureGain  float32
	playbackGain float32
	// log receives the helpers' stderr and exit reports; nil means
	// slog.Default().
	log *slog.Logger
}

func (c commandAudio) logger() *slog.Logger {
	if c.log != nil {
		return c.log
	}
	return slog.Default()
}

// Check reports, before any call starts, why this configuration cannot
// give a call sound: a command that is not installed, or no command at
// all. A call that could never be heard must not ring silently.
func (c commandAudio) Check() error {
	if len(c.capture) == 0 && len(c.playback) == 0 {
		return errors.New("whatsapp: call audio: call_capture_command and call_playback_command are both empty, so a call would have no sound; set at least one (see docs/config.example.toml)")
	}
	var errs []error
	for _, h := range []struct {
		role string
		argv []string
	}{{"capture", c.capture}, {"playback", c.playback}} {
		if len(h.argv) == 0 {
			continue
		}
		if _, err := exec.LookPath(h.argv[0]); err != nil {
			errs = append(errs, missingHelperError(h.role, h.argv[0]))
		}
	}
	return errors.Join(errs...)
}

// missingHelperError says which audio helper is not installed and how to
// fix it.
func missingHelperError(role, name string) error {
	base := filepath.Base(name)
	hint := fmt.Sprintf("install it or set call_%s_command to another tool", role)
	switch base {
	case "parec", "pacat":
		alt := "pw-record"
		if role == "playback" {
			alt = "pw-cat"
		}
		hint = fmt.Sprintf("install pulseaudio-utils (it also works with pipewire-pulse) or set call_%s_command, for example to %s", role, alt)
	case "pw-record", "pw-cat":
		hint = fmt.Sprintf("install PipeWire's command line tools (pipewire-bin or pipewire-utils) or set call_%s_command to parec/pacat", role)
	}
	return fmt.Errorf("whatsapp: call audio: %s not found in PATH; %s", base, hint)
}

// Open starts the capture and playback processes. problem, when not nil,
// is told once per helper if it later fails or dies mid-call.
func (c commandAudio) Open(problem func(short string, err error)) (meowcaller.AudioSource, meowcaller.AudioSink, error) {
	var src meowcaller.AudioSource
	if len(c.capture) > 0 {
		h, out, err := startCapture(c.capture, c.logger(), problem)
		if err != nil {
			return nil, nil, err
		}
		src = meowcaller.PCMStream(&procReader{ReadCloser: out, h: h})
		if g := c.captureGain; g > 0 && g != 1 {
			src = gainSource{AudioSource: src, gain: g}
		}
	}
	var sink meowcaller.AudioSink
	if len(c.playback) > 0 {
		h, in, err := startPlayback(c.playback, c.logger(), problem)
		if err != nil {
			closeAudio(src, nil)
			return nil, nil, err
		}
		sink = &pcmSink{w: in, h: h, gain: c.playbackGain}
	}
	return src, sink, nil
}

// helper is one running audio process. It watches the process so an early
// death is reported (once) with its exit status and stderr tail, instead
// of the call silently going quiet.
type helper struct {
	role    string
	name    string
	cmd     *exec.Cmd
	stderr  *tailBuffer
	done    chan struct{}
	exitErr error // set before done is closed
	closing atomic.Bool
	log     *slog.Logger
	problem func(short string, err error)
	once    sync.Once
}

// startHelper starts argv with the given stdin/stdout (either may be nil)
// and a bounded stderr capture, and begins watching it.
func startHelper(role string, argv []string, stdin, stdout *os.File, log *slog.Logger, problem func(string, error)) (*helper, error) {
	h := &helper{
		role:    role,
		name:    filepath.Base(argv[0]),
		stderr:  &tailBuffer{max: helperStderrTail},
		done:    make(chan struct{}),
		log:     log.With("helper", filepath.Base(argv[0]), "role", role),
		problem: problem,
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	if stdout != nil {
		cmd.Stdout = stdout
	}
	cmd.Stderr = h.stderr
	cmd.WaitDelay = helperWaitDelay
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, missingHelperError(role, argv[0])
		}
		return nil, fmt.Errorf("whatsapp: call %s %q: %w", role, argv[0], err)
	}
	h.cmd = cmd
	h.log.Info("call audio helper started", "args", strings.Join(argv[1:], " "))
	go h.watch()
	return h, nil
}

// watch reaps the process and reports an exit nobody asked for.
func (h *helper) watch() {
	err := h.cmd.Wait()
	h.exitErr = err
	close(h.done)
	tail := h.stderr.String()
	if h.closing.Load() {
		if tail != "" {
			h.log.Info("call audio helper stderr", "stderr", tail)
		}
		return
	}
	status := "exit status 0"
	if err != nil {
		status = err.Error()
	}
	short := fmt.Sprintf("%s terminó (%s)", h.name, status)
	if line := lastLine(tail); line != "" {
		short += ": " + line
	}
	h.report(short, fmt.Errorf("whatsapp: call %s %s exited early (%s); stderr: %q", h.role, h.name, status, tail))
}

// report tells the owner of the call about a problem, once per helper.
func (h *helper) report(short string, err error) {
	h.once.Do(func() {
		h.log.Error("call audio helper failed", "error", err)
		if h.problem != nil {
			h.problem(short, err)
		}
	})
}

// close stops the process and waits for it. Idempotent.
func (h *helper) close() {
	h.closing.Store(true)
	_ = h.cmd.Process.Kill()
	<-h.done
}

// startCapture runs argv with its stdout piped to the returned reader.
// The pipe is an os.Pipe handed to the child directly, so reaping the
// process never truncates data not yet read.
func startCapture(argv []string, log *slog.Logger, problem func(string, error)) (*helper, *os.File, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("whatsapp: call capture: %w", err)
	}
	h, err := startHelper("capture", argv, nil, pw, log, problem)
	_ = pw.Close()
	if err != nil {
		_ = pr.Close()
		return nil, nil, err
	}
	return h, pr, nil
}

// startPlayback runs argv with its stdin fed from the returned writer.
func startPlayback(argv []string, log *slog.Logger, problem func(string, error)) (*helper, *os.File, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("whatsapp: call playback: %w", err)
	}
	h, err := startHelper("playback", argv, pr, nil, log, problem)
	_ = pr.Close()
	if err != nil {
		_ = pw.Close()
		return nil, nil, err
	}
	return h, pw, nil
}

// procReader closes its process along with its stdout.
type procReader struct {
	io.ReadCloser
	h    *helper
	once sync.Once
}

func (p *procReader) Close() error {
	p.once.Do(func() {
		p.h.close()
		_ = p.ReadCloser.Close()
	})
	return nil
}

// pcmSink writes the peer's float32 frames as s16le to a playback process.
type pcmSink struct {
	mu     sync.Mutex
	w      io.WriteCloser
	h      *helper
	buf    []byte
	gain   float32
	closed bool
	failed bool
}

// WriteFrame feeds the playback process. When it fails (the process died,
// typically EPIPE) the failure is reported once, and later frames are
// dropped without an error: meowcaller would otherwise log the same
// warning 16 times a second for the rest of the call.
func (s *pcmSink) WriteFrame(frame []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.ErrClosedPipe
	}
	if s.failed {
		return nil
	}
	s.buf = encodeS16LE(s.buf[:0], frame, s.gain)
	_, err := s.w.Write(s.buf)
	if err == nil {
		return nil
	}
	s.failed = true
	// A dead helper reports itself with its exit status and stderr; give
	// it a moment to do so rather than reporting a bare broken pipe.
	select {
	case <-s.h.done:
	case <-time.After(helperDeathGrace):
		s.h.report(s.h.name+": no se puede escribir el audio ("+err.Error()+")",
			fmt.Errorf("whatsapp: call playback %s: write: %w", s.h.name, err))
	}
	return err
}

func (s *pcmSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	_ = s.w.Close()
	s.h.close()
	return nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// lastLine is the last non-empty line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// gainSource scales every frame of its AudioSource by gain.
type gainSource struct {
	meowcaller.AudioSource
	gain float32
}

func (g gainSource) ReadFrame() ([]float32, error) {
	frame, err := g.AudioSource.ReadFrame()
	for i, v := range frame {
		frame[i] = clampSample(v * g.gain)
	}
	return frame, err
}

func clampSample(v float32) float32 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

// encodeS16LE appends frame, scaled by gain (0 means 1) and clamped to
// [-1, 1], as signed 16-bit little-endian PCM.
func encodeS16LE(dst []byte, frame []float32, gain float32) []byte {
	if gain <= 0 {
		gain = 1
	}
	for _, v := range frame {
		dst = binary.LittleEndian.AppendUint16(dst, uint16(int16(clampSample(v*gain)*32767)))
	}
	return dst
}
