// Package mediaprobe is a synthetic audio-transport characterization harness
// for the central-server remote-call audio spike (odd/tasks/
// central-server-remote-calls.md, T2). It does NOT contact a tailnet, a
// real audio device, or a provider. All loss/latency/jitter values are
// synthetic inputs, not measured network behavior. It exists so T2 can
// record repeatable synthetic RED/GREEN and mutation evidence before any
// transport choice is made.
//
// The model is deliberately tiny: a source produces PCM frames, a transport
// drops or delays some of them, and a sink records what arrived. Results are
// deterministic for a fixed seed.
package mediaprobe

import (
	"errors"
	"fmt"
	"math/rand"
)

// frameSamples matches meowcaller's 60 ms source frame at 16 kHz mono:
// 16000 samples/s * 0.060 s = 960 samples per frame.
const frameSamples = 960

// frameBytes is one s16le frame (2 bytes per sample).
const frameBytes = frameSamples * 2

// Result reports one synthetic transport run.
type Result struct {
	// Scenario is the name of the run (e.g. "baseline", "stall").
	Scenario string
	// Sent is how many frames the source produced.
	Sent int
	// Delivered is how many frames the sink received in order.
	Delivered int
	// Dropped is how many frames the transport dropped before delivery.
	Dropped int
	// Stale is how many delivered frames arrived after their deadline
	// (delivery time > playout deadline).
	Stale int
	// Injected is how many drops the scenario explicitly requested
	// (as opposed to loss from congestion/delay).
	Injected int
	// MaxLatencyMS is the largest one-way delay observed, in milliseconds.
	MaxLatencyMS float64
	// MeanLatencyMS is the average one-way delay of delivered frames.
	MeanLatencyMS float64
}

// Scenario defines one synthetic transport configuration. LossRate is the
// fraction of frames dropped (0..1). LatencyMS is the base one-way delay.
// JitterMS is a random delay added on top of LatencyMS. DeadlineMS is the
// playout deadline after which a delivered frame is stale. Seed makes the
// run deterministic.
type Scenario struct {
	Name          string
	LossRate      float64
	LatencyMS     float64
	JitterMS      float64
	DeadlineMS    float64
	FrameCount    int
	InjectedDrops int
	Seed          int64
}

// Transport simulates delivering frames from a source to a sink under the
// given scenario. It is deterministic for a fixed seed.
func Transport(s Scenario) (Result, error) {
	if s.FrameCount <= 0 {
		return Result{}, errors.New("mediaprobe: FrameCount must be > 0")
	}
	if s.LossRate < 0 || s.LossRate > 1 {
		return Result{}, errors.New("mediaprobe: LossRate must be in [0, 1]")
	}
	rng := rand.New(rand.NewSource(s.Seed))

	result := Result{Scenario: s.Name, Sent: s.FrameCount}
	delivered := 0
	var totalLatency float64

	for i := 0; i < s.FrameCount; i++ {
		// The source produces one frame.
		_ = make([]byte, frameBytes) // synthetic frame content

		// Injected drops are applied first (deterministic positions).
		injected := i < s.InjectedDrops

		// Random loss from the scenario's loss rate.
		randomLoss := rng.Float64() < s.LossRate

		if injected || randomLoss {
			result.Dropped++
			if injected {
				result.Injected++
			}
			continue
		}

		// Latency: base + jitter.
		latency := s.LatencyMS + rng.Float64()*s.JitterMS
		totalLatency += latency

		// Stale if delivery exceeds the playout deadline.
		if latency > s.DeadlineMS {
			result.Stale++
		}
		delivered++
	}

	result.Delivered = delivered
	if delivered > 0 {
		result.MeanLatencyMS = totalLatency / float64(delivered)
	}
	if s.JitterMS > 0 {
		result.MaxLatencyMS = s.LatencyMS + s.JitterMS
	} else {
		result.MaxLatencyMS = s.LatencyMS
	}

	return result, nil
}

// String renders a result as a single-line summary for spike reports.
func (r Result) String() string {
	return fmt.Sprintf("%s: sent=%d delivered=%d dropped=%d stale=%d injected=%d meanLat=%.1fms maxLat=%.1fms",
		r.Scenario, r.Sent, r.Delivered, r.Dropped, r.Stale, r.Injected,
		r.MeanLatencyMS, r.MaxLatencyMS)
}

// Presets are the canonical synthetic scenarios from the spike report.
// They are labeled synthetic: they reproduce the report's numbers, not
// measured network behavior.
var Presets = []Scenario{
	{
		Name:          "baseline",
		LossRate:      0.0,
		LatencyMS:     5.0,
		JitterMS:      1.0,
		DeadlineMS:    100.0,
		FrameCount:    12,
		InjectedDrops: 0,
		Seed:          1,
	},
	{
		Name:          "stall",
		LossRate:      0.0,
		LatencyMS:     20.0,
		JitterMS:      40.0,
		DeadlineMS:    100.0,
		FrameCount:    12,
		InjectedDrops: 1,
		Seed:          2,
	},
}
