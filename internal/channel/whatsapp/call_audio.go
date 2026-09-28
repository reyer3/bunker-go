package whatsapp

import (
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"sync"

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
}

func (c commandAudio) Open() (meowcaller.AudioSource, meowcaller.AudioSink, error) {
	var src meowcaller.AudioSource
	if len(c.capture) > 0 {
		cmd := exec.Command(c.capture[0], c.capture[1:]...)
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, fmt.Errorf("whatsapp: call capture: %w", err)
		}
		if err := cmd.Start(); err != nil {
			return nil, nil, fmt.Errorf("whatsapp: call capture %q: %w", c.capture[0], err)
		}
		src = meowcaller.PCMStream(&procReader{ReadCloser: out, cmd: cmd})
		if g := c.captureGain; g > 0 && g != 1 {
			src = gainSource{AudioSource: src, gain: g}
		}
	}
	var sink meowcaller.AudioSink
	if len(c.playback) > 0 {
		cmd := exec.Command(c.playback[0], c.playback[1:]...)
		in, err := cmd.StdinPipe()
		if err != nil {
			closeAudio(src, nil)
			return nil, nil, fmt.Errorf("whatsapp: call playback: %w", err)
		}
		if err := cmd.Start(); err != nil {
			closeAudio(src, nil)
			return nil, nil, fmt.Errorf("whatsapp: call playback %q: %w", c.playback[0], err)
		}
		sink = &pcmSink{w: in, cmd: cmd, gain: c.playbackGain}
	}
	return src, sink, nil
}

// procReader closes its process along with its stdout.
type procReader struct {
	io.ReadCloser
	cmd  *exec.Cmd
	once sync.Once
}

func (p *procReader) Close() error {
	p.once.Do(func() {
		_ = p.cmd.Process.Kill()
		_ = p.ReadCloser.Close()
		_ = p.cmd.Wait()
	})
	return nil
}

// pcmSink writes the peer's float32 frames as s16le to a playback process.
type pcmSink struct {
	mu     sync.Mutex
	w      io.WriteCloser
	cmd    *exec.Cmd
	buf    []byte
	gain   float32
	closed bool
}

func (s *pcmSink) WriteFrame(frame []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.ErrClosedPipe
	}
	s.buf = encodeS16LE(s.buf[:0], frame, s.gain)
	_, err := s.w.Write(s.buf)
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
	_ = s.cmd.Process.Kill()
	_ = s.cmd.Wait()
	return nil
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
