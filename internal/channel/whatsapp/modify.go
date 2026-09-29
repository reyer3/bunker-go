package whatsapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

// Edit, delete for everyone and react (issues #76 and #17). Each one is a
// protocol message sent to the chat like any other message, so each one
// goes through the same human pacing as Send: the presence bracket (so
// the phone keeps its notifications) and the adapter's minimum interval
// between outgoing messages. An edit is typed, so it also shows
// composing for the new text; a delete or a reaction is a tap, so it
// only waits a short, jittered moment before going out.

// deleteForEveryoneWindow is how long after sending WhatsApp still
// honors a revoke. whatsmeow has no constant for it; WhatsApp documents
// "about two days" (clients currently accept a little more), so bunker
// stays at two days rather than send a revoke the other side ignores.
const deleteForEveryoneWindow = 48 * time.Hour

// Tap pause bounds (see withTapPause): a reaction or a delete is a quick
// gesture, not typing, but never instantaneous.
const (
	tapPauseMin = 1 * time.Second
	tapPauseMax = 3 * time.Second
)

var (
	_ core.Editor               = (*Adapter)(nil)
	_ core.Deleter              = (*Adapter)(nil)
	_ core.Reactor              = (*Adapter)(nil)
	_ core.MessageWindowLimiter = (*Adapter)(nil)
)

// MessageWindows implements core.MessageWindowLimiter: whatsmeow's
// EditWindow for edits (the server refuses later ones) and two days for
// delete for everyone.
func (a *Adapter) MessageWindows() core.MessageWindows {
	return core.MessageWindows{Edit: whatsmeow.EditWindow, Delete: deleteForEveryoneWindow}
}

// OwnReactionSender implements core.Reactor: this account's own JID
// without its device part, the key handleReaction also stores our
// reactions under (whichever device made them).
func (a *Adapter) OwnReactionSender() string {
	own := a.cli.OwnJID()
	if own.IsEmpty() {
		return ""
	}
	return own.ToNonAD().String()
}

// EditMessage implements core.Editor: it replaces the text of our own
// message item with newText. Service has already checked the item is
// ours, text only and within whatsmeow.EditWindow.
func (a *Adapter) EditMessage(ctx context.Context, item core.Item, newText string) (core.Receipt, error) {
	chat, msgID, err := a.messageKey(item, "edit")
	if err != nil {
		return core.Receipt{}, err
	}
	msg := a.cli.BuildEdit(chat, msgID, &waE2E.Message{Conversation: ptrString(newText)})
	var resp whatsmeow.SendResponse
	err = a.withHumanEmulation(ctx, chat, newText, false, func() error {
		var sendErr error
		resp, sendErr = a.pacedSend(ctx, chat, msg)
		return sendErr
	})
	if err != nil {
		return core.Receipt{}, fmt.Errorf("whatsapp: edit %s: %w", item.ID, err)
	}
	a.updateCached(item.ID, func(it *core.Item) { it.Body, it.Edited = newText, true })
	return a.receipt(chat, resp), nil
}

// DeleteMessage implements core.Deleter: it revokes our own message item
// for everyone in the chat.
func (a *Adapter) DeleteMessage(ctx context.Context, item core.Item) (core.Receipt, error) {
	chat, msgID, err := a.messageKey(item, "delete")
	if err != nil {
		return core.Receipt{}, err
	}
	msg := a.cli.BuildRevoke(chat, types.EmptyJID, msgID)
	var resp whatsmeow.SendResponse
	err = a.withTapPause(ctx, "delete", func() error {
		var sendErr error
		resp, sendErr = a.pacedSend(ctx, chat, msg)
		return sendErr
	})
	if err != nil {
		return core.Receipt{}, fmt.Errorf("whatsapp: delete %s: %w", item.ID, err)
	}
	a.updateCached(item.ID, func(it *core.Item) { it.Body, it.Deleted = "", true })
	return a.receipt(chat, resp), nil
}

// React implements core.Reactor: it sets our reaction to item (anyone's
// message) to emoji, or removes it when emoji is empty, which is how
// WhatsApp itself withdraws a reaction.
func (a *Adapter) React(ctx context.Context, item core.Item, emoji string) (core.Receipt, error) {
	chat, msgID, err := a.messageKey(item, "react")
	if err != nil {
		return core.Receipt{}, err
	}
	sender, err := reactionTargetSender(item, chat)
	if err != nil {
		return core.Receipt{}, err
	}
	msg := a.cli.BuildReaction(chat, sender, msgID, emoji)
	var resp whatsmeow.SendResponse
	err = a.withTapPause(ctx, "react", func() error {
		var sendErr error
		resp, sendErr = a.pacedSend(ctx, chat, msg)
		return sendErr
	})
	if err != nil {
		return core.Receipt{}, fmt.Errorf("whatsapp: react to %s: %w", item.ID, err)
	}
	return a.receipt(chat, resp), nil
}

// messageKey returns the chat and message id item.ID addresses, refusing
// an item of another account: the id's account part is what the rest of
// bunker routed on, so a mismatch means a bug upstream, not a request.
func (a *Adapter) messageKey(item core.Item, op string) (types.JID, types.MessageID, error) {
	account, chat, msgID, err := parseItemID(item.ID)
	if err != nil {
		return types.JID{}, "", fmt.Errorf("whatsapp: %s: %w", op, err)
	}
	if account != a.account {
		return types.JID{}, "", fmt.Errorf("whatsapp: %s %s: item belongs to account %q, not %q", op, item.ID, account, a.account)
	}
	jid, err := types.ParseJID(chat)
	if err != nil {
		return types.JID{}, "", fmt.Errorf("whatsapp: %s %s: chat %q: %w", op, item.ID, chat, err)
	}
	return jid, types.MessageID(msgID), nil
}

// reactionTargetSender is who sent the message a reaction targets, which
// WhatsApp's message key needs: empty for our own, the stored sender for
// anyone else's, or the chat itself in a one-to-one chat. In a group
// without a known sender the key cannot be built, and guessing would
// react to nothing.
func reactionTargetSender(item core.Item, chat types.JID) (types.JID, error) {
	if item.FromMe {
		return types.EmptyJID, nil
	}
	if item.From.ID != "" && strings.Contains(item.From.ID, "@") {
		sender, err := types.ParseJID(item.From.ID)
		if err != nil {
			return types.JID{}, fmt.Errorf("whatsapp: react to %s: sender %q: %w", item.ID, item.From.ID, err)
		}
		return sender, nil
	}
	if chat.Server == types.GroupServer {
		return types.JID{}, fmt.Errorf("whatsapp: react to %s: the group message has no known sender", item.ID)
	}
	return chat, nil
}

// pacedSend waits for the adapter's minimum send interval, then sends.
func (a *Adapter) pacedSend(ctx context.Context, chat types.JID, msg *waE2E.Message) (whatsmeow.SendResponse, error) {
	if err := a.waitForPacing(ctx); err != nil {
		return whatsmeow.SendResponse{}, err
	}
	return a.cli.SendMessage(ctx, chat, msg)
}

// withTapPause brackets deliver with presence available/unavailable,
// like withHumanEmulation, but instead of composing it waits a short,
// jittered moment within [tapPauseMin, tapPauseMax]: a reaction or a
// delete is a tap, and a linked device firing it the instant it is asked
// looks automated.
func (a *Adapter) withTapPause(ctx context.Context, op string, deliver func() error) error {
	if err := a.cli.SendPresence(ctx, types.PresenceAvailable); err != nil {
		return fmt.Errorf("presence available: %w", err)
	}
	defer a.revokePresence(ctx, op)
	a.sleep(tapPauseMin + time.Duration(a.rand01()*float64(tapPauseMax-tapPauseMin)))
	return deliver()
}

func (a *Adapter) receipt(chat types.JID, resp whatsmeow.SendResponse) core.Receipt {
	return core.Receipt{
		ID:      itemID(a.account, chat.String(), string(resp.ID)),
		Channel: core.ChannelWhatsApp,
		At:      resp.Timestamp,
	}
}

// updateCached applies change to the cached copy of id, if any, so Fetch
// and the quote of a later reply show what the chat now shows.
func (a *Adapter) updateCached(id string, change func(*core.Item)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if it, ok := a.items[id]; ok {
		change(&it)
		a.items[id] = it
	}
}
