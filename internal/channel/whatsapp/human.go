package whatsapp

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Timing constants for the human-emulation choreography (T13b): a
// composing/typing window proportional to the message length, clamped so
// neither a one-word note nor a long paragraph looks robotic, with
// jitter so consecutive sends never take an identical duration; a
// captionless image instead gets a flat, shorter window, since there is
// no text to time typing against.
const (
	composingCharsPerSecond = 7
	minComposingDuration    = 2 * time.Second
	maxComposingDuration    = 15 * time.Second
	composingJitterFraction = 0.25

	mediaOnlyComposingMin = 2 * time.Second
	mediaOnlyComposingMax = 4 * time.Second
)

// composingDuration returns how long Send/SendMedia should hold the
// "composing" chat presence before delivering: proportional to bodyLen
// at composingCharsPerSecond, clamped to [minComposingDuration,
// maxComposingDuration] with up to ±25% jitter from rand01 (a value in
// [0,1), as math/rand.Float64 returns) — or, when isMedia is true and
// bodyLen is 0 (a captionless image), a flat window within
// [mediaOnlyComposingMin, mediaOnlyComposingMax] instead.
func composingDuration(bodyLen int, isMedia bool, rand01 float64) time.Duration {
	if bodyLen == 0 && isMedia {
		span := mediaOnlyComposingMax - mediaOnlyComposingMin
		return mediaOnlyComposingMin + time.Duration(rand01*float64(span))
	}

	base := time.Duration(bodyLen) * time.Second / composingCharsPerSecond
	if base < minComposingDuration {
		base = minComposingDuration
	} else if base > maxComposingDuration {
		base = maxComposingDuration
	}

	jitter := (rand01*2 - 1) * composingJitterFraction // in [-0.25, 0.25]
	d := base + time.Duration(float64(base)*jitter)
	if d < minComposingDuration {
		d = minComposingDuration
	}
	return d
}

// withHumanEmulation brackets deliver (the real send, of one or several
// messages) with the presence/typing choreography every send performs
// (T13b): available so the linked device does not suppress the phone's
// own push notifications, composing sized to body (or the flat media
// window when isMedia and body is empty), paused just before delivering,
// then unavailable again afterwards — on every path, including deliver's
// own error, via defer.
func (a *Adapter) withHumanEmulation(ctx context.Context, jid types.JID, body string, isMedia bool, deliver func() error) error {
	if err := a.cli.SendPresence(ctx, types.PresenceAvailable); err != nil {
		return fmt.Errorf("whatsapp: send: presence available: %w", err)
	}
	defer func() { _ = a.cli.SendPresence(ctx, types.PresenceUnavailable) }()

	if err := a.cli.SendChatPresence(ctx, jid, types.ChatPresenceComposing, types.ChatPresenceMediaText); err != nil {
		return fmt.Errorf("whatsapp: send: chat presence composing: %w", err)
	}
	a.sleep(composingDuration(len(body), isMedia, a.rand01()))
	if err := a.cli.SendChatPresence(ctx, jid, types.ChatPresencePaused, types.ChatPresenceMediaText); err != nil {
		return fmt.Errorf("whatsapp: send: chat presence paused: %w", err)
	}

	return deliver()
}
