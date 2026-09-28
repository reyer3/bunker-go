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

// readTarget identifies the (chat, sender) pair whatsmeow's MarkRead needs.
type readTarget struct{ chat, sender types.JID }

// MarkThreadRead implements core.ThreadReader (conversation-view.md's
// read-thread fix, K9): it resolves every id first — so an unparseable id
// fails before any waClient call, exactly like the single-item MarkRead —
// then groups them by (chat, sender), since whatsmeow's MarkRead is scoped
// to one sender per call (a group chat with several unread participants
// needs one call each), and sends one presence available/MarkRead-per-
// group/unavailable sequence, in the order those groups first appear.
func (a *Adapter) MarkThreadRead(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	groups := map[readTarget][]types.MessageID{}
	var order []readTarget
	for _, id := range ids {
		chatJID, sender, msgID, err := a.resolveReadTarget(id)
		if err != nil {
			return fmt.Errorf("whatsapp: mark thread read %s: %w", id, err)
		}
		key := readTarget{chatJID, sender}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], msgID)
	}

	if err := a.cli.SendPresence(ctx, types.PresenceAvailable); err != nil {
		return fmt.Errorf("whatsapp: mark thread read: presence available: %w", err)
	}
	defer func() { _ = a.cli.SendPresence(ctx, types.PresenceUnavailable) }()

	now := time.Now()
	for _, key := range order {
		if err := a.cli.MarkRead(ctx, groups[key], now, key.chat, key.sender); err != nil {
			return fmt.Errorf("whatsapp: mark thread read: %w", err)
		}
	}
	return nil
}
