package whatsapp

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"

	"github.com/reyer3/bunker-go/internal/config"
)

const (
	// AudioTestToneSeconds is how long the test tone plays.
	AudioTestToneSeconds = 1
	audioTestToneHz      = 440
	audioTestToneLevel   = 0.3
	// audioTestSilentPeak is the mic peak (fraction of full scale) under
	// which the capture is treated as digital silence: a live microphone
	// always has a few LSBs of noise, a muted or wrong source has none.
	audioTestSilentPeak = 0.001
	// audioTestSlack is how long past the requested seconds the capture
	// may take to deliver them before the test gives up on it.
	audioTestSlack = 4 * time.Second
	// audioTestReap is how long a capture that fell short may take to be
	// reaped, so its exit status makes the report.
	audioTestReap = 500 * time.Millisecond
	// audioTestDrain is how long the playback tool may take to finish the
	// tone once its input is closed.
	audioTestDrain = 3 * time.Second
)

// AudioTestSide is what the test found about one audio helper.
type AudioTestSide struct {
	// Command is the configured argv; empty with Disabled set.
	Command  []string `json:"command"`
	Disabled bool     `json:"disabled,omitempty"`
	Found    bool     `json:"found"`
	Started  bool     `json:"started"`
	// Error is why the helper is missing, did not start, or exited early.
	Error string `json:"error,omitempty"`
	// Stderr is the tail of the helper's stderr.
	Stderr string `json:"stderr,omitempty"`
}

// AudioTestReport is the result of AudioTest. It is local only: nothing in
// it touches WhatsApp or the network.
type AudioTestReport struct {
	Account         string        `json:"account"`
	Capture         AudioTestSide `json:"capture"`
	Playback        AudioTestSide `json:"playback"`
	SecondsWanted   int           `json:"seconds_wanted"`
	SecondsRecorded float64       `json:"seconds_recorded"`
	// MicPeak and MicRMS are fractions of full scale (0..1) after
	// call_capture_gain; the dBFS fields are the same levels in decibels
	// (floored at -120).
	MicPeak     float64  `json:"mic_peak"`
	MicRMS      float64  `json:"mic_rms"`
	MicPeakDBFS float64  `json:"mic_peak_dbfs"`
	MicRMSDBFS  float64  `json:"mic_rms_dbfs"`
	Hints       []string `json:"hints,omitempty"`
	// OK is true when both helpers ran and the microphone heard something.
	OK bool `json:"ok"`
}

// AudioTest plays a short 440 Hz tone through acc's playback command and
// records seconds from its capture command, without WhatsApp: it is how to
// tell a local audio problem from a call problem. It never touches the
// network, so it needs no dry run.
func AudioTest(ctx context.Context, acc config.Account, seconds int) AudioTestReport {
	report := audioTest(ctx, commandAudioFromOptions(acc.Options), seconds)
	report.Account = acc.Name
	return report
}

func audioTest(ctx context.Context, c commandAudio, seconds int) AudioTestReport {
	r := AudioTestReport{SecondsWanted: seconds, Capture: AudioTestSide{Command: c.capture}, Playback: AudioTestSide{Command: c.playback}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	var playH *helper
	var playW io.WriteCloser
	if len(c.playback) == 0 {
		r.Playback.Disabled = true
	} else if r.Playback.Found = lookPath(c.playback[0]); !r.Playback.Found {
		r.Playback.Error = missingHelperError("playback", c.playback[0]).Error()
	} else {
		h, w, err := startPlayback(c.playback, log, nil)
		if err != nil {
			r.Playback.Error = err.Error()
		} else {
			playH, playW, r.Playback.Started = h, w, true
		}
	}

	var capH *helper
	var capR io.ReadCloser
	if len(c.capture) == 0 {
		r.Capture.Disabled = true
	} else if r.Capture.Found = lookPath(c.capture[0]); !r.Capture.Found {
		r.Capture.Error = missingHelperError("capture", c.capture[0]).Error()
	} else {
		h, rd, err := startCapture(c.capture, log, nil)
		if err != nil {
			r.Capture.Error = err.Error()
		} else {
			capH, capR, r.Capture.Started = h, rd, true
		}
	}

	// The tone goes out while the microphone records, so a speaker next to
	// the microphone is heard in the level too.
	if playW != nil {
		go func(w io.Writer) { _, _ = w.Write(encodeS16LE(nil, tone(AudioTestToneSeconds), c.playbackGain)) }(playW)
	}

	var pcm []byte
	if capR != nil {
		pcm = recordPCM(ctx, capR, seconds)
		if len(pcm) < seconds*meowcaller.SampleRate*2 {
			// Short of data: give a dying process a moment to be reaped so
			// its exit status is in the report.
			select {
			case <-capH.done:
			case <-time.After(audioTestReap):
			}
		}
		select {
		case <-capH.done:
			// It ended on its own: fine if it delivered everything, an
			// error if it died (or stopped short) mid-recording.
			if capH.exitErr != nil || len(pcm) < seconds*meowcaller.SampleRate*2 {
				r.Capture.Error = fmt.Sprintf("%s exited early (%s)", capH.name, exitStatus(capH.exitErr))
			}
		default:
		}
		capH.close()
		_ = capR.Close()
		r.Capture.Stderr = capH.stderr.String()
	}
	if playH != nil {
		select {
		case <-playH.done:
			r.Playback.Error = fmt.Sprintf("%s exited before the tone was played (%s)", playH.name, exitStatus(playH.exitErr))
		default:
		}
		playH.closing.Store(true) // exiting after its input closes is expected
		_ = playW.Close()
		select {
		case <-playH.done:
			if playH.exitErr != nil && r.Playback.Error == "" {
				r.Playback.Error = fmt.Sprintf("%s exited with an error (%s)", playH.name, exitStatus(playH.exitErr))
			}
		case <-time.After(audioTestDrain):
			playH.close()
		case <-ctx.Done():
			playH.close()
		}
		r.Playback.Stderr = playH.stderr.String()
	}

	r.SecondsRecorded = float64(len(pcm)/2) / meowcaller.SampleRate
	r.MicPeak, r.MicRMS = pcmLevels(pcm, c.captureGain)
	r.MicPeakDBFS, r.MicRMSDBFS = dbfs(r.MicPeak), dbfs(r.MicRMS)
	r.hint(c, seconds)
	return r
}

func exitStatus(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// tone is seconds of a 440 Hz sine at 16 kHz mono.
func tone(seconds int) []float32 {
	out := make([]float32, seconds*meowcaller.SampleRate)
	for i := range out {
		out[i] = audioTestToneLevel * float32(math.Sin(2*math.Pi*audioTestToneHz*float64(i)/meowcaller.SampleRate))
	}
	return out
}

// recordPCM reads up to seconds of s16le from r, giving up on a capture
// that stalls; whatever arrived is returned.
func recordPCM(ctx context.Context, r io.Reader, seconds int) []byte {
	want := seconds * meowcaller.SampleRate * 2
	var mu sync.Mutex
	var got []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			mu.Lock()
			got = append(got, buf[:n]...)
			full := len(got) >= want
			mu.Unlock()
			if err != nil || full {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Duration(seconds)*time.Second + audioTestSlack):
	case <-ctx.Done():
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) > want {
		got = got[:want]
	}
	return append([]byte(nil), got...)
}

// pcmLevels is the peak and RMS of s16le pcm after gain, as fractions of
// full scale.
func pcmLevels(pcm []byte, gain float32) (peak, rms float64) {
	if gain <= 0 {
		gain = 1
	}
	n := len(pcm) / 2
	if n == 0 {
		return 0, 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		v := float64(clampSample(float32(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768 * gain))
		sum += v * v
		if a := math.Abs(v); a > peak {
			peak = a
		}
	}
	return peak, math.Sqrt(sum / float64(n))
}

func dbfs(level float64) float64 {
	if level <= 1e-6 {
		return -120
	}
	return math.Round(20*math.Log10(level)*10) / 10
}

// hint fills in what to try next and the overall verdict.
func (r *AudioTestReport) hint(c commandAudio, seconds int) {
	ok := true
	if r.Capture.Disabled && r.Playback.Disabled {
		r.Hints = append(r.Hints, "both call_capture_command and call_playback_command are empty: a call would have no sound")
		ok = false
	}
	for _, s := range []AudioTestSide{r.Capture, r.Playback} {
		if s.Error != "" {
			ok = false
		}
	}
	if r.Capture.Error != "" || r.Playback.Error != "" {
		r.Hints = append(r.Hints, "fix the helper above, or point call_capture_command / call_playback_command at a working tool (see docs/config.example.toml)")
	}
	if r.Capture.Started && r.Capture.Error == "" {
		switch {
		case r.SecondsRecorded < float64(seconds)*0.5:
			ok = false
			r.Hints = append(r.Hints, fmt.Sprintf("the capture command delivered %.1f s of the %d s asked: it is stalled or ended early; check the device with `pactl list short sources` (or `wpctl status`)", r.SecondsRecorded, seconds))
		case r.MicPeak < audioTestSilentPeak:
			ok = false
			r.Hints = append(r.Hints, "the microphone level is ~0 (digital silence): the mic is muted or the wrong source is selected; check `pactl get-source-mute @DEFAULT_SOURCE@`, `pactl list short sources`, and select it with --device=<source> in call_capture_command")
		case r.MicPeak > 0.99:
			r.Hints = append(r.Hints, "the microphone clips (peak at full scale): lower call_capture_gain or the input volume")
		}
	}
	if r.Playback.Started && r.Playback.Error == "" {
		r.Hints = append(r.Hints, fmt.Sprintf("playback accepted a %d s 440 Hz tone; if you did not hear it, check the output device and volume (`pactl list short sinks`) or select it with --device=<sink> in call_playback_command", AudioTestToneSeconds))
	}
	r.OK = ok
}

// String renders the report for a terminal.
func (r AudioTestReport) String() string {
	var b strings.Builder
	side := func(name string, s AudioTestSide, extra string) {
		fmt.Fprintf(&b, "%s: ", name)
		switch {
		case s.Disabled:
			fmt.Fprintln(&b, "disabled (command is empty)")
			return
		case s.Error != "":
			fmt.Fprintf(&b, "%s\n  command: %s\n  error: %s\n", status(s), strings.Join(s.Command, " "), s.Error)
		default:
			fmt.Fprintf(&b, "%s%s\n  command: %s\n", status(s), extra, strings.Join(s.Command, " "))
		}
		if s.Stderr != "" {
			fmt.Fprintf(&b, "  stderr: %s\n", strings.ReplaceAll(s.Stderr, "\n", "\n          "))
		}
	}
	side("capture ", r.Capture, fmt.Sprintf(", recorded %.1f s of %d s", r.SecondsRecorded, r.SecondsWanted))
	side("playback", r.Playback, fmt.Sprintf(", played a %d s 440 Hz tone", AudioTestToneSeconds))
	if r.Capture.Started {
		fmt.Fprintf(&b, "mic level: peak %.4f (%.1f dBFS), rms %.4f (%.1f dBFS)\n", r.MicPeak, r.MicPeakDBFS, r.MicRMS, r.MicRMSDBFS)
	}
	for _, h := range r.Hints {
		fmt.Fprintf(&b, "hint: %s\n", h)
	}
	if r.OK {
		fmt.Fprintln(&b, "result: ok")
	} else {
		fmt.Fprintln(&b, "result: PROBLEM")
	}
	return b.String()
}

func status(s AudioTestSide) string {
	switch {
	case !s.Found:
		return "not found"
	case !s.Started:
		return "did not start"
	case s.Error != "":
		return "failed"
	}
	return "started"
}
