package whatsapp

import (
	"bytes"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakePactl answers the pactl subcommands the echo canceller issues and
// records every call, so no test ever touches the real sound server.
type fakePactl struct {
	mu    sync.Mutex
	calls [][]string
	// fail makes the named subcommand (args[0]) fail.
	fail map[string]error
}

func (f *fakePactl) run(args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	if err := f.fail[args[0]]; err != nil {
		return "", err
	}
	switch args[0] {
	case "get-default-sink":
		return "alsa_output.fake-speaker", nil
	case "get-default-source":
		return "alsa_input.fake-mic", nil
	case "load-module":
		return "42", nil
	}
	return "", nil
}

func (f *fakePactl) find(sub string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if c[0] == sub {
			out = append(out, c)
		}
	}
	return out
}

func echoAudio(f *fakePactl, capture, playback []string) commandAudio {
	return commandAudio{
		capture:    capture,
		playback:   playback,
		echoCancel: newPulseEchoCancel(f.run),
	}
}

func TestEchoCancelLoadsModuleAndRoutesHelpers(t *testing.T) {
	f := &fakePactl{}
	c := echoAudio(f, defaultCaptureCommand, defaultPlaybackCommand)
	origCapture := slices.Clone(c.capture)

	got, release := c.withEchoCancel()

	loads := f.find("load-module")
	if len(loads) != 1 {
		t.Fatalf("load-module calls = %v, want 1", f.calls)
	}
	load := loads[0]
	if load[1] != "module-echo-cancel" || !slices.Contains(load, "aec_method=webrtc") ||
		!slices.Contains(load, "source_master=alsa_input.fake-mic") ||
		!slices.Contains(load, "sink_master=alsa_output.fake-speaker") {
		t.Fatalf("load-module args = %q", load)
	}
	var sinkName, sourceName string
	for _, a := range load {
		if v, ok := strings.CutPrefix(a, "sink_name="); ok {
			sinkName = v
		}
		if v, ok := strings.CutPrefix(a, "source_name="); ok {
			sourceName = v
		}
	}
	if !strings.HasPrefix(sinkName, "bunker_call_aec_") || !strings.HasPrefix(sourceName, "bunker_call_aec_") || sinkName == sourceName {
		t.Fatalf("sink_name=%q source_name=%q, want distinct bunker_call_aec_ names", sinkName, sourceName)
	}
	if last := got.capture[len(got.capture)-1]; last != "--device="+sourceName {
		t.Fatalf("capture argv = %q, want --device=%s last", got.capture, sourceName)
	}
	if last := got.playback[len(got.playback)-1]; last != "--device="+sinkName {
		t.Fatalf("playback argv = %q, want --device=%s last", got.playback, sinkName)
	}
	if !slices.Equal(c.capture, origCapture) || !slices.Equal(defaultCaptureCommand[:len(origCapture)], origCapture) {
		t.Fatalf("withEchoCancel mutated the original command: %q", c.capture)
	}

	release()
	release()
	unloads := f.find("unload-module")
	if len(unloads) != 1 || unloads[0][1] != "42" {
		t.Fatalf("unload-module calls = %v, want exactly one for module 42", unloads)
	}
}

func TestEchoCancelNamesAreUniquePerCall(t *testing.T) {
	f := &fakePactl{}
	c := echoAudio(f, defaultCaptureCommand, defaultPlaybackCommand)
	a, ra := c.withEchoCancel()
	b, rb := c.withEchoCancel()
	defer ra()
	defer rb()
	if slices.Equal(a.playback, b.playback) {
		t.Fatalf("two calls share the echo-cancel sink: %q", a.playback)
	}
}

func TestEchoCancelLoadFailureFallsBackWithWarning(t *testing.T) {
	for _, sub := range []string{"get-default-sink", "get-default-source", "load-module"} {
		t.Run(sub, func(t *testing.T) {
			f := &fakePactl{fail: map[string]error{sub: errors.New("Failure: Module initialization failed")}}
			var logs bytes.Buffer
			c := echoAudio(f, defaultCaptureCommand, defaultPlaybackCommand)
			c.log = slog.New(slog.NewTextHandler(&logs, nil))

			got, release := c.withEchoCancel()
			release()

			if !slices.Equal(got.capture, defaultCaptureCommand) || !slices.Equal(got.playback, defaultPlaybackCommand) {
				t.Fatalf("fallback argv = %q / %q, want the plain defaults", got.capture, got.playback)
			}
			if len(f.find("unload-module")) != 0 {
				t.Fatalf("unload issued for a module that never loaded: %v", f.calls)
			}
			if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "echo cancel") {
				t.Fatalf("log = %q, want a WARN about echo cancel", out)
			}
		})
	}
}

func TestEchoCancelOffLoadsNothing(t *testing.T) {
	c := commandAudio{capture: defaultCaptureCommand, playback: defaultPlaybackCommand}
	got, release := c.withEchoCancel()
	release()
	if !slices.Equal(got.capture, defaultCaptureCommand) || !slices.Equal(got.playback, defaultPlaybackCommand) {
		t.Fatalf("argv changed with echo cancel off: %q / %q", got.capture, got.playback)
	}
}

func TestEchoCancelOptions(t *testing.T) {
	if c := commandAudioFromOptions(map[string]interface{}{}); c.echoCancel == nil {
		t.Fatal("echo cancel is off by default, want on")
	}
	if c := commandAudioFromOptions(map[string]interface{}{"call_echo_cancel": false}); c.echoCancel != nil {
		t.Fatal("call_echo_cancel = false left echo cancel on")
	}
	for _, key := range []string{"call_capture_command", "call_playback_command"} {
		opts := map[string]interface{}{key: []interface{}{"pw-cat", "--raw"}}
		c := commandAudioFromOptions(opts)
		if c.echoCancel != nil {
			t.Fatalf("custom %s: echo cancel on, want custom commands untouched", key)
		}
		if key == "call_capture_command" && !slices.Equal(c.capture, []string{"pw-cat", "--raw"}) {
			t.Fatalf("custom capture = %q", c.capture)
		}
	}
}

func TestEchoCancelUnloadedWhenBothHelpersClose(t *testing.T) {
	f := &fakePactl{}
	c := echoAudio(f, []string{"sh", "-c", "exec sleep 30"}, []string{"sh", "-c", "exec cat >/dev/null"})
	src, sink, err := c.Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.find("load-module")) != 1 {
		t.Fatalf("calls = %v, want one load-module", f.calls)
	}
	src.Close()
	if n := len(f.find("unload-module")); n != 0 {
		t.Fatalf("module unloaded while playback still runs (%d unloads)", n)
	}
	sink.Close()
	sink.Close()
	if n := len(f.find("unload-module")); n != 1 {
		t.Fatalf("unload-module calls = %d after both closed, want 1", n)
	}
}

func TestEchoCancelUnloadedWhenHelperFailsToStart(t *testing.T) {
	f := &fakePactl{}
	c := echoAudio(f, []string{"sh", "-c", "exec sleep 30"}, []string{"bunker-no-such-tool-play"})
	if _, _, err := c.Open(nil); err == nil {
		t.Fatal("Open succeeded with a missing playback helper")
	}
	if n := len(f.find("unload-module")); n != 1 {
		t.Fatalf("unload-module calls = %d after a failed Open, want 1", n)
	}
}
