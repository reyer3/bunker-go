package matrix

import (
	"context"
	"fmt"
	"time"

	"maunium.net/go/mautrix"
)

// Typing-notification timing (T13d): proportional to the text length at
// ~7 chars/second, clamped so neither a one-word note nor a long
// paragraph looks robotic. Unlike WhatsApp's composingDuration, this
// carries no jitter — Matrix's spec has no "paused" state to transition
// through, so there is nothing for jitter to make more natural.
const (
	typingCharsPerSecond = 7
	minTypingDuration    = 2 * time.Second
	maxTypingDuration    = 15 * time.Second
)

// typingDuration returns how long Send should show the typing indicator
// before delivering a bodyLen-character message.
func typingDuration(bodyLen int) time.Duration {
	d := time.Duration(bodyLen) * time.Second / typingCharsPerSecond
	if d < minTypingDuration {
		return minTypingDuration
	}
	if d > maxTypingDuration {
		return maxTypingDuration
	}
	return d
}

// MarkRead implements core.ReadMarker for `bunker read <id>` (T13c/d): it
// sends an m.read receipt and updates the fully-read marker for the same
// event in one call. No presence: Matrix has no equivalent concept here.
func (a *Adapter) MarkRead(ctx context.Context, id string) error {
	_, roomID, eventID, err := parseItemID(id)
	if err != nil {
		return fmt.Errorf("matrix: mark read %s: %w", id, err)
	}
	if err := a.client.SetReadMarkers(ctx, roomID, &mautrix.ReqSetReadMarkers{Read: eventID, FullyRead: eventID}); err != nil {
		return fmt.Errorf("matrix: mark read %s: %w", id, err)
	}
	return nil
}

// MarkThreadRead implements core.ThreadReader (conversation-view.md's
// read-thread fix, K9): a Matrix read/fully_read marker already covers
// every earlier event in the room, so only ids' newest (last) entry needs
// a receipt — unlike WhatsApp, there is nothing to batch or group here.
func (a *Adapter) MarkThreadRead(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return a.MarkRead(ctx, ids[len(ids)-1])
}
