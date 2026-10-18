package mediaprobe

import (
	"testing"
)

// TestTransportBaselineDeliversAllFrames verifies that a zero-loss scenario
// delivers every frame. Synthetic only: no network, no audio device.
func TestTransportBaselineDeliversAllFrames(t *testing.T) {
	s := Presets[0] // "baseline"
	r, err := Transport(s)
	if err != nil {
		t.Fatalf("Transport baseline: %v", err)
	}
	if r.Sent != s.FrameCount {
		t.Fatalf("sent = %d, want %d", r.Sent, s.FrameCount)
	}
	if r.Delivered != s.FrameCount {
		t.Fatalf("delivered = %d, want %d (zero loss)", r.Delivered, s.FrameCount)
	}
	if r.Dropped != 0 {
		t.Fatalf("dropped = %d, want 0", r.Dropped)
	}
	if r.Injected != 0 {
		t.Fatalf("injected = %d, want 0", r.Injected)
	}
}

// TestTransportStallDropsFrames verifies that the stall scenario drops some
// frames (injected + stale) and delivers fewer than sent. Synthetic only.
func TestTransportStallDropsFrames(t *testing.T) {
	s := Presets[1] // "stall"
	r, err := Transport(s)
	if err != nil {
		t.Fatalf("Transport stall: %v", err)
	}
	if r.Sent != s.FrameCount {
		t.Fatalf("sent = %d, want %d", r.Sent, s.FrameCount)
	}
	if r.Delivered >= s.FrameCount {
		t.Fatalf("delivered = %d, want < %d (stall drops frames)", r.Delivered, s.FrameCount)
	}
	if r.Dropped == 0 {
		t.Fatal("dropped = 0, want > 0 (stall drops frames)")
	}
	if r.Delivered+r.Dropped != s.FrameCount {
		t.Fatalf("delivered(%d) + dropped(%d) != sent(%d)", r.Delivered, r.Dropped, r.Sent)
	}
	if r.Injected != s.InjectedDrops {
		t.Fatalf("injected = %d, want %d", r.Injected, s.InjectedDrops)
	}
}

// TestTransportIsDeterministic verifies that the same seed produces the
// same result, so spike measurements are repeatable.
func TestTransportIsDeterministic(t *testing.T) {
	s := Presets[1]
	r1, err := Transport(s)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Transport(s)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Delivered != r2.Delivered || r1.Dropped != r2.Dropped || r1.Stale != r2.Stale {
		t.Fatalf("non-deterministic: r1=%+v r2=%+v", r1, r2)
	}
}

// TestTransportRejectsInvalidScenario verifies input validation.
func TestTransportRejectsInvalidScenario(t *testing.T) {
	cases := []Scenario{
		{Name: "zero-frames", FrameCount: 0},
		{Name: "bad-loss", LossRate: 1.5, FrameCount: 10},
		{Name: "neg-loss", LossRate: -0.1, FrameCount: 10},
	}
	for _, c := range cases {
		if _, err := Transport(c); err == nil {
			t.Fatalf("scenario %q should be rejected", c.Name)
		}
	}
}

// TestTransportLatencyBounds verifies reported latency stays within the
// scenario's configured bounds.
func TestTransportLatencyBounds(t *testing.T) {
	s := Presets[0]
	r, err := Transport(s)
	if err != nil {
		t.Fatal(err)
	}
	if r.MaxLatencyMS > s.LatencyMS+s.JitterMS {
		t.Errorf("maxLatency=%.1fms > base+jitter=%.1fms", r.MaxLatencyMS, s.LatencyMS+s.JitterMS)
	}
	if r.MeanLatencyMS > s.LatencyMS+s.JitterMS {
		t.Errorf("meanLatency=%.1fms > base+jitter=%.1fms", r.MeanLatencyMS, s.LatencyMS+s.JitterMS)
	}
}

// TestResultString verifies the summary renders all fields.
func TestResultString(t *testing.T) {
	r := Result{Scenario: "x", Sent: 10, Delivered: 8, Dropped: 2, Stale: 1, Injected: 1, MeanLatencyMS: 12.5, MaxLatencyMS: 45.0}
	s := r.String()
	for _, want := range []string{"x", "sent=10", "delivered=8", "dropped=2", "stale=1", "injected=1", "12.5ms", "45.0ms"} {
		if !contains(s, want) {
			t.Errorf("Result.String() = %q, missing %q", s, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
