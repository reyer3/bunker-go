package whatsapp

import (
	"context"
	"errors"

	"github.com/purpshell/meowcaller"
)

// mediaEndpoint is one side of a call's media: capture (microphone) and
// playback (speaker). It may be local (parec/pacat on this machine) or
// remote (a desktop client over the tailnet). The interface is narrow:
// Open starts the endpoint for one call and returns the frames the engine
// consumes/produces. Close releases it. Check reports why it cannot work
// before a call starts (missing helper, missing device, no permission).
//
// No production implementation exists yet: nilMediaEndpoint below is the
// only one, and it always fails. A real remote endpoint (T4) must be added
// behind this same interface, never as a concrete type baked into the
// adapter.
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
//
// No production implementation exists yet: nilMediaSession below is the
// only one, and it always rejects Claim. A real session (T3) must be added
// behind this same interface.
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

// headlessMode reports whether the daemon runs without a local desktop:
// no local microphone/speaker should ever be opened for call media.
type headlessMode interface {
	Headless() bool
}

// nilMediaEndpoint is the only mediaEndpoint today: it always fails, so
// no local or remote devices are opened until T4 provides a real one.
type nilMediaEndpoint struct{}

func (nilMediaEndpoint) Open(context.Context, string, func(string, error)) (meowcaller.AudioSource, meowcaller.AudioSink, error) {
	return nil, nil, errors.New("whatsapp: call media: no media endpoint configured")
}
func (nilMediaEndpoint) Check() error {
	return errors.New("whatsapp: call media: no media endpoint configured")
}

// nilMediaSession is the only mediaSession today: it always rejects
// Claim, so no remote client can own call media until T3 provides a real
// session. Local audio remains the default when no session exists.
type nilMediaSession struct{}

func (nilMediaSession) Claim(context.Context, string, string, string) (bool, mediaEndpoint, error) {
	return false, nil, errors.New("whatsapp: call media: no media session configured")
}
func (nilMediaSession) Release(context.Context, string) error {
	return errors.New("whatsapp: call media: no media session configured")
}
func (nilMediaSession) Owner(string) string { return "" }

// nilHeadless reports that the daemon is not headless: local audio may be
// opened for calls. A real deployment sets headless = true (T3/T6).
type nilHeadless struct{}

func (nilHeadless) Headless() bool { return false }

// headlessFlag is a headlessMode backed by a plain bool, so config can
// switch the daemon into headless mode without a separate type.
type headlessFlag bool

func (h headlessFlag) Headless() bool { return bool(h) }

// errMediaOwnerRemote is returned when startCallAudio is asked to open
// local devices but a remote desktop owns the call's media.
var errMediaOwnerRemote = errors.New("whatsapp: call media: remote desktop owns media, not opening local devices")

// errHeadlessNoLocalAudio is returned when the daemon is headless and no
// remote media endpoint owns the call: there is nothing to play sound on.
var errHeadlessNoLocalAudio = errors.New("whatsapp: call media: headless daemon with no media endpoint; cannot open local audio")
