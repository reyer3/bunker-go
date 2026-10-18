package whatsapp

import (
	"context"
	"errors"
	"testing"

	"github.com/purpshell/meowcaller"
)

// mediaEndpoint is one side of a call's media: capture (microphone) and
// playback (speaker). It may be local (parec/pacat on this machine) or
// remote (a desktop client over the tailnet). The interface is intentionally
// narrow: Open starts the endpoint for one call and returns the frames the
// engine consumes/produces. Close releases it. Check reports why it cannot
// work before a call starts (missing helper, missing device, no permission).
type mediaEndpoint interface {
	// Open starts capture/playback for callID. problem reports helper
	// failure mid-call. ctx lets the engine cancel a blocked open.
	Open(ctx context.Context, callID string, problem func(short string, err error)) (meowcaller.AudioSource, meowcaller.AudioSink, error)
	// Check reports, before any call starts, why this endpoint cannot give
	// a call sound.
	Check() error
}

// mediaSession is one media owner per call. It is acquired atomically with
// an authenticated identity and a short-lived, call-scoped credential.
// Only the owner may attach capture/playback; observers can see call state
// but not listen or inject audio.
type mediaSession interface {
	// Claim attempts to become this call's media owner. identity is the
	// authenticated client (e.g. a desktop over SSH/tailnet). credential
	// is call-scoped and single-use. ok is false when another client owns
	// the media or the credential is stale/expired/replayed.
	Claim(ctx context.Context, callID string, identity, credential string) (ok bool, endpoint mediaEndpoint, err error)
	// Release gives up media ownership for callID. Idempotent.
	Release(ctx context.Context, callID string) error
	// Owner returns the current media owner's identity, or "" if none.
	Owner(callID string) string
}

// headlessMode reports whether the daemon runs without a local desktop
// (no local microphone/speaker should ever be opened for media).
type headlessMode interface {
	Headless() bool
}

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
	// fail to compile or return an error until T3-T4 exist.
	var sess mediaSession = nilMediaSession{}
	_, _, err := sess.Claim(context.Background(), live.ID(), "desktop-linux", "cred-1")
	if err == nil {
		t.Fatal("expected no media session implementation (RED: T3 not implemented)")
	}

	// The local audio interface must not auto-open when a remote endpoint
	// owns the media. Today Open has no endpoint selector: it always opens
	// local parec/pacat.
	var ep mediaEndpoint = nilMediaEndpoint{}
	src, sink, openErr := ep.Open(context.Background(), live.ID(), func(string, error) {})
	if openErr == nil && (src != nil || sink != nil) {
		t.Fatal("expected remote endpoint to not return local devices (RED: T1 not implemented)")
	}
}

// TestNoHeadlessLocalCaptureRED is RED: it defines that a headless daemon
// (no local desktop) never opens local microphone/speaker for call media,
// even when OnReady fires. The media owner is the answering desktop.
// It must fail because Adapter.startCallAudio unconditionally opens local
// commandAudio when media is ready.
func TestNoHeadlessLocalCaptureRED(t *testing.T) {
	a, engine, audio, _ := newCallAdapter(t)
	t.Cleanup(a.hangupAll)
	live := &fakeLiveCall{id: "HEADLESS", peer: callPeer}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), callPeer.String()); err != nil {
		t.Fatal(err)
	}
	live.onReady()
	// A headless daemon with a remote media owner must not open local
	// devices. Today there is no headless flag and no remote endpoint:
	// startCallAudio opens commandAudio unconditionally.
	if audio.opens != 0 {
		t.Fatalf("headless daemon opened local audio (%d opens), want 0 (RED: T1 not implemented)", audio.opens)
	}
}

// nilMediaEndpoint is a stub so the test file references the interface
// without implementing it (the interface itself is the RED).
type nilMediaEndpoint struct{}

func (nilMediaEndpoint) Open(context.Context, string, func(string, error)) (meowcaller.AudioSource, meowcaller.AudioSink, error) {
	return nil, nil, errors.New("no media endpoint implementation (RED: T3/T4)")
}
func (nilMediaEndpoint) Check() error {
	return errors.New("no media endpoint implementation (RED: T3/T4)")
}

// nilMediaSession is a stub so the test file references the interface
// without implementing it.
type nilMediaSession struct{}

func (nilMediaSession) Claim(context.Context, string, string, string) (bool, mediaEndpoint, error) {
	return false, nil, errors.New("no media session implementation (RED: T3)")
}
func (nilMediaSession) Release(context.Context, string) error {
	return errors.New("no media session implementation (RED: T3)")
}
func (nilMediaSession) Owner(string) string { return "" }
