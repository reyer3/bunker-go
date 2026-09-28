package core

import (
	"context"
	"errors"
	"fmt"
)

// MarkUnread puts an item back in the unread inbox (issue #37: undo a
// mark-read, or keep something for later). Where the channel can mark a
// message unread (mail's \Seen), the server is updated too. Where it
// cannot (WhatsApp, Matrix), only bunker's own store changes and
// localOnly reports it, so callers can say so instead of pretending the
// phone changed too.
func (s *Service) MarkUnread(ctx context.Context, id string) (localOnly bool, err error) {
	item, err := s.store.Get(ctx, id)
	if err != nil {
		return false, err
	}
	unseen := false
	if adapter, ok := s.registry.Get(item.Channel, item.Account); ok {
		if organizer, ok := adapter.(Organizer); ok {
			err := organizer.Organize(ctx, id, OrganizeOp{Seen: &unseen})
			switch {
			case err == nil:
				if err := s.store.MarkRead(ctx, id, false); err != nil {
					return false, fmt.Errorf("core: mark unread: store: %w", err)
				}
				return false, nil
			case !errors.Is(err, ErrUnsupported):
				return false, fmt.Errorf("core: mark unread: %w", err)
			}
		}
	}
	if err := s.store.MarkRead(ctx, id, false); err != nil {
		return false, fmt.Errorf("core: mark unread: store: %w", err)
	}
	return true, nil
}
