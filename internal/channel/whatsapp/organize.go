package whatsapp

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

// Organize supports only the read/unread state WhatsApp itself exposes:
// setting Seen to true sends a read receipt. Labels, folders and
// unmarking a message as read have no WhatsApp equivalent, so they
// report core.ErrUnsupported instead of silently doing nothing.
func (a *Adapter) Organize(ctx context.Context, id string, op core.OrganizeOp) error {
	if len(op.AddLabels) > 0 || len(op.RemoveLabels) > 0 || op.MoveTo != "" {
		return fmt.Errorf("whatsapp: organize %s: labels and folders: %w", id, core.ErrUnsupported)
	}
	if op.Seen == nil {
		return nil
	}
	if !*op.Seen {
		return fmt.Errorf("whatsapp: organize %s: marking a message unread: %w", id, core.ErrUnsupported)
	}

	chatJID, sender, msgID, err := a.resolveReadTarget(id)
	if err != nil {
		return fmt.Errorf("whatsapp: organize %s: %w", id, err)
	}
	return a.cli.MarkRead(ctx, []types.MessageID{msgID}, time.Now(), chatJID, sender)
}

// resolveReadTarget parses id into the chat/sender JIDs and message ID
// MarkRead needs, filling sender from this adapter's cached item (the
// original message's From) when known, falling back to the chat JID
// itself. Shared by Organize's Seen=true path and MarkRead (T13c), so
// both resolve a read target identically.
func (a *Adapter) resolveReadTarget(id string) (chat, sender types.JID, msgID types.MessageID, err error) {
	_, chatStr, msgIDStr, err := parseItemID(id)
	if err != nil {
		return types.JID{}, types.JID{}, "", err
	}
	chatJID, err := types.ParseJID(chatStr)
	if err != nil {
		return types.JID{}, types.JID{}, "", fmt.Errorf("chat jid %q: %w", chatStr, err)
	}
	sender = chatJID
	if cached, ok := a.cachedItem(id); ok && cached.From.ID != "" {
		if s, err := types.ParseJID(cached.From.ID); err == nil {
			sender = s
		}
	}
	return chatJID, sender, types.MessageID(msgIDStr), nil
}
