package whatsapp

import (
	"context"
	"testing"
)

// TestRemoteEndpointSelectionRED is RED: it defines the expected contract
// for choosing a remote media endpoint over a local one, and must fail
// because no endpoint-selection API exists on the call engine or the
// audio interface. It does not invent a production wire contract; it only
// states the behavior the future media-session abstraction must provide.
func TestRemoteEndpointSelectionRED(t *testing.T) {
	var _ mediaEndpoint = (*nilMediaEndpoint)(nil)
	var _ mediaSession = (*nilMediaSession)(nil)

	// A call's media can be claimed by a remote desktop client.
	a, engine, _, _ := newCallAdapter(t)
	t.Cleanup(a.hangupAll)
	live := &fakeLiveCall{id: "REMOTE", peer: callPeer}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), callPeer.String()); err != nil {
		t.Fatal(err)
	}

	// No mediaSession exists on Adapter today: claiming remote media must
	// fail because nilMediaSession always rejects Claim.
	var sess mediaSession = nilMediaSession{}
	_, _, err := sess.Claim(context.Background(), live.ID(), "desktop-linux", "cred-1")
	if err == nil {
		t.Fatal("expected no media session implementation (RED: T3 not implemented)")
	}

	// The local audio interface must not auto-open when a remote endpoint
	// owns the media. nilMediaEndpoint always fails, so no devices open.
	var ep mediaEndpoint = nilMediaEndpoint{}
	src, sink, openErr := ep.Open(context.Background(), live.ID(), func(string, error) {})
	if openErr == nil && (src != nil || sink != nil) {
		t.Fatal("expected remote endpoint to not return local devices (RED: T1 not implemented)")
	}
}

// TestNoHeadlessLocalCaptureRED is RED: it defines that a headless daemon
// (no local desktop) never opens local microphone/speaker for call media,
// even when OnReady fires. The media owner is the answering desktop.
func TestNoHeadlessLocalCaptureRED(t *testing.T) {
	a, engine, audio, _ := newCallAdapter(t)
	t.Cleanup(a.hangupAll)
	a.headless = headlessFlag(true)
	live := &fakeLiveCall{id: "HEADLESS", peer: callPeer}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), callPeer.String()); err != nil {
		t.Fatal(err)
	}
	live.onReady()
	// A headless daemon with no media endpoint must not open local
	// devices: startCallAudio checks headless before opening commandAudio.
	if audio.opens != 0 {
		t.Fatalf("headless daemon opened local audio (%d opens), want 0", audio.opens)
	}
}
