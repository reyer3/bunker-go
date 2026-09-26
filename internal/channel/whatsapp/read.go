package whatsapp

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// MarkRead implements core.ReadMarker for `bunker read <id>` (T13c):
// presence available (so the linked device does not suppress the phone's
// own push notification while it looks "active"), the read receipt
// itself (blue ticks), then presence unavailable again — on every path,
// including an error, via defer.
func (a *Adapter) MarkRead(ctx context.Context, id string) error {
	chatJID, sender, msgID, err := a.resolveReadTarget(id)
	if err != nil {
		return fmt.Errorf("whatsapp: mark read %s: %w", id, err)
	}

	if err := a.cli.SendPresence(ctx, types.PresenceAvailable); err != nil {
		return fmt.Errorf("whatsapp: mark read %s: presence available: %w", id, err)
	}
	defer func() { _ = a.cli.SendPresence(ctx, types.PresenceUnavailable) }()

	if err := a.cli.MarkRead(ctx, []types.MessageID{msgID}, time.Now(), chatJID, sender); err != nil {
		return fmt.Errorf("whatsapp: mark read %s: %w", id, err)
	}
	return nil
}
