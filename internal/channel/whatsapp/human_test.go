package whatsapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// writeTestPNG writes a minimal PNG-signature file under a fresh
// t.TempDir() and returns its path.
func writeTestPNG(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(path, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// --- composingDuration: proportional 7 chars/s, clamped [2s,15s], ±25%
// jitter; a captionless image gets a flat 2-4s window instead (T13b). ---

func TestComposingDurationProportionalToBodyLength(t *testing.T) {
	// 70 chars at 7 chars/s = 10s. rand01=0.5 yields zero jitter
	// ((0.5*2-1)*0.25 == 0), so the duration is exactly the base.
	body := make([]byte, 70)
	for i := range body {
		body[i] = 'x'
	}
	got := composingDuration(len(body), false, 0.5)
	if got != 10*time.Second {
		t.Fatalf("composingDuration = %v, want 10s", got)
	}
}

func TestComposingDurationClampsToMin(t *testing.T) {
	got := composingDuration(1, false, 0.5) // ~0.14s, under the 2s floor
	if got != 2*time.Second {
		t.Fatalf("composingDuration = %v, want the 2s floor", got)
	}
}

func TestComposingDurationClampsToMax(t *testing.T) {
	got := composingDuration(1000, false, 0.5) // ~143s, over the 15s ceiling
	if got != 15*time.Second {
		t.Fatalf("composingDuration = %v, want the 15s ceiling", got)
	}
}

func TestComposingDurationJitterBounds(t *testing.T) {
	base := 10 * time.Second                      // 70 chars
	min := composingDuration(70, false, 0)        // -25%
	max := composingDuration(70, false, 0.999999) // +25%
	if min >= base {
		t.Fatalf("min-jitter duration = %v, want less than the %v base", min, base)
	}
	if max <= base {
		t.Fatalf("max-jitter duration = %v, want more than the %v base", max, base)
	}
	if min < 7*time.Second || min > base {
		t.Fatalf("min-jitter duration = %v, want within ~25%% below %v", min, base)
	}
	if max > 13*time.Second || max < base {
		t.Fatalf("max-jitter duration = %v, want within ~25%% above %v", max, base)
	}
}

func TestComposingDurationMediaOnlyUsesFlatTwoToFourSecondWindow(t *testing.T) {
	if got := composingDuration(0, true, 0); got != 2*time.Second {
		t.Fatalf("composingDuration(media, rand=0) = %v, want 2s", got)
	}
	if got := composingDuration(0, true, 1); got != 4*time.Second {
		t.Fatalf("composingDuration(media, rand=1) = %v, want 4s", got)
	}
}

// --- Send/SendMedia human-emulation choreography (T13b) ---

// TestSendPerformsHumanEmulationInOrder covers T13(b): every send does
// presence available, chat presence composing, (wait), chat presence
// paused, the real send, then presence unavailable — in that exact
// order.
func TestSendPerformsHumanEmulationInOrder(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, time.Millisecond)

	if _, err := a.Send(context.Background(), core.Outgoing{To: []string{"1234@s.whatsapp.net"}, Body: "hola"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	want := []string{"presence:available", "chatpresence:composing", "chatpresence:paused", "send", "presence:unavailable"}
	got := cli.callLog()
	if len(got) != len(want) {
		t.Fatalf("call log = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call log = %+v, want %+v", got, want)
		}
	}
}

// TestSendHumanEmulationRunsOnErrorPathToo covers "also on error paths
// (defer)": a failing send still ends with presence unavailable.
func TestSendHumanEmulationRunsOnErrorPathToo(t *testing.T) {
	cli := newFakeWAClient()
	cli.sendErr = errors.New("network gone")
	a := newTestAdapter("personal", cli, time.Millisecond)

	_, err := a.Send(context.Background(), core.Outgoing{To: []string{"1234@s.whatsapp.net"}, Body: "hola"})
	if err == nil {
		t.Fatal("Send() error = nil, want the underlying send error")
	}

	got := cli.callLog()
	if len(got) == 0 || got[len(got)-1] != "presence:unavailable" {
		t.Fatalf("call log = %+v, want it to end with presence:unavailable even on error", got)
	}
}

// TestSendMediaPerformsHumanEmulationOnceForTheWholeBatch covers T13(b):
// a caption is typed once, then every image is sent — not one
// composing/paused cycle per image.
func TestSendMediaPerformsHumanEmulationOnceForTheWholeBatch(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, time.Millisecond)
	path1 := writeTestPNG(t)
	path2 := writeTestPNG(t)

	out := core.Outgoing{To: []string{"1234@s.whatsapp.net"}, Body: "mira", Attachments: []string{path1, path2}}
	if _, err := a.SendMedia(context.Background(), out); err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}

	want := []string{"presence:available", "chatpresence:composing", "chatpresence:paused", "send", "send", "presence:unavailable"}
	got := cli.callLog()
	if len(got) != len(want) {
		t.Fatalf("call log = %+v, want %+v (ONE composing/paused cycle for both images)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call log = %+v, want %+v", got, want)
		}
	}
}

// TestSendMediaWithoutCaptionUsesMediaOnlyComposingWindow proves the
// flat 2-4s window is used (via composingDuration's isMedia branch) when
// out.Body is empty: the send still happens, and emulation still
// brackets it, only the duration formula changes (see composingDuration
// tests above for the exact bound).
func TestSendMediaWithoutCaptionUsesMediaOnlyComposingWindow(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, time.Millisecond)
	var sleeps []time.Duration
	a.SetSleeper(func(d time.Duration) { sleeps = append(sleeps, d) })
	a.SetRand01(func() float64 { return 0 }) // media-only floor: 2s

	path := writeTestPNG(t)
	out := core.Outgoing{To: []string{"1234@s.whatsapp.net"}, Attachments: []string{path}}
	if _, err := a.SendMedia(context.Background(), out); err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	if len(sleeps) != 1 || sleeps[0] != 2*time.Second {
		t.Fatalf("sleeps = %+v, want [2s]", sleeps)
	}
}

// TestSendUsesInjectedSleeperForComposingDuration proves Send waits via
// the injected sleeper (T13f: never a real sleep in a test), for the
// proportional duration.
func TestSendUsesInjectedSleeperForComposingDuration(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli, time.Millisecond)
	var sleeps []time.Duration
	a.SetSleeper(func(d time.Duration) { sleeps = append(sleeps, d) })
	a.SetRand01(func() float64 { return 0.5 }) // zero jitter

	body := make([]byte, 70) // 70 chars / 7cps = 10s
	for i := range body {
		body[i] = 'x'
	}
	if _, err := a.Send(context.Background(), core.Outgoing{To: []string{"1234@s.whatsapp.net"}, Body: string(body)}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(sleeps) != 1 || sleeps[0] != 10*time.Second {
		t.Fatalf("sleeps = %+v, want [10s]", sleeps)
	}
}
