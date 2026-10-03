package whatsapp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// pactlTimeout bounds every pactl invocation: a stuck sound server must
// never hold a call's audio back for long.
const pactlTimeout = 5 * time.Second

// pactlRunner runs "pactl args..." and returns its trimmed stdout.
type pactlRunner func(args ...string) (string, error)

// runPactl is the real pactl runner.
func runPactl(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pactlTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pactl", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("pactl %s: %w: %s", args[0], err, msg)
		}
		return "", fmt.Errorf("pactl %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

// pulseEchoCancel loads one PulseAudio/PipeWire module-echo-cancel
// (WebRTC AEC) per call, between the default speaker and microphone at
// the moment the call's audio starts, so the peer does not hear her own
// voice coming back through laptop speakers and mic.
//
// It is per call, not per daemon: the default devices may change between
// calls (headphones plugged in), and nothing is left loaded while no call
// is live.
type pulseEchoCancel struct {
	run pactlRunner
	// prefix makes this daemon's device names unique across processes;
	// seq makes them unique across calls.
	prefix string
	seq    atomic.Uint64
}

func newPulseEchoCancel(run pactlRunner) *pulseEchoCancel {
	if run == nil {
		run = runPactl
	}
	return &pulseEchoCancel{run: run, prefix: fmt.Sprintf("bunker_call_aec_%d", os.Getpid())}
}

// echoDevices is one loaded echo canceller: play into sink, record from
// source.
type echoDevices struct {
	module string
	sink   string
	source string
}

// load creates the echo-cancel devices for one call.
func (p *pulseEchoCancel) load() (echoDevices, error) {
	speaker, err := p.run("get-default-sink")
	if err != nil {
		return echoDevices{}, err
	}
	mic, err := p.run("get-default-source")
	if err != nil {
		return echoDevices{}, err
	}
	if speaker == "" || mic == "" {
		return echoDevices{}, fmt.Errorf("no default sink (%q) or source (%q)", speaker, mic)
	}
	name := fmt.Sprintf("%s_%d", p.prefix, p.seq.Add(1))
	d := echoDevices{sink: name + "_sink", source: name + "_source"}
	d.module, err = p.run("load-module", "module-echo-cancel",
		"aec_method=webrtc",
		"source_master="+mic,
		"sink_master="+speaker,
		"source_name="+d.source,
		"sink_name="+d.sink,
	)
	if err != nil {
		return echoDevices{}, err
	}
	if d.module == "" {
		return echoDevices{}, fmt.Errorf("pactl load-module printed no module index")
	}
	return d, nil
}

// unload removes the module load returned.
func (p *pulseEchoCancel) unload(d echoDevices) error {
	_, err := p.run("unload-module", d.module)
	return err
}

// withEchoCancel returns c with its capture/playback commands pointed at
// a freshly loaded echo canceller, plus the function that unloads it
// (idempotent). With echo cancellation off, or when it cannot be loaded
// (no pactl, module missing), c comes back unchanged with a no-op
// release: a call never fails because AEC is unavailable.
//
// Only the built-in parec/pacat commands get here (see
// commandAudioFromOptions): a custom command is never rewritten.
func (c commandAudio) withEchoCancel() (commandAudio, func()) {
	if c.echoCancel == nil {
		return c, func() {}
	}
	log := c.logger().With("feature", "echo cancel")
	d, err := c.echoCancel.load()
	if err != nil {
		log.Warn("call echo cancel unavailable, the peer may hear an echo; continuing without it",
			"error", err, "hint", "needs pactl and module-echo-cancel (pipewire-pulse); set call_echo_cancel = false to silence this")
		return c, func() {}
	}
	log.Info("call echo cancel loaded", "module", d.module, "sink", d.sink, "source", d.source)
	// The --device target also makes EasyEffects leave these streams
	// alone: it ignores a stream whose target.object is a device other
	// than its own.
	if len(c.capture) > 0 {
		c.capture = append(c.capture[:len(c.capture):len(c.capture)], "--device="+d.source)
	}
	if len(c.playback) > 0 {
		c.playback = append(c.playback[:len(c.playback):len(c.playback)], "--device="+d.sink)
	}
	var once sync.Once
	return c, func() {
		once.Do(func() {
			if err := c.echoCancel.unload(d); err != nil {
				log.Warn("call echo cancel: unload failed", "module", d.module, "error", err)
				return
			}
			log.Info("call echo cancel unloaded", "module", d.module)
		})
	}
}

// releaseAfter calls release once all n holders called the returned
// done function (each holder's extra calls are its own business: pass a
// once-guarded done to it).
func releaseAfter(n int, release func()) func() {
	var left atomic.Int64
	left.Store(int64(n))
	return func() {
		if left.Add(-1) == 0 {
			release()
		}
	}
}
