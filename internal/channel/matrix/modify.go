package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// Edit, delete and react (issues #76 and #17). An edit is an ordinary
// m.room.message carrying m.new_content and an m.replace relation, so it
// goes through SendMessageEvent and is encrypted exactly like a message
// in an encrypted room. A delete is a redaction of our event. A reaction
// is an m.annotation; removing it redacts our reaction event, which the
// mapping relations.go keeps (reactions/liveReactions and their
// persisted records) is what finds.
//
// Our own sync echo of each of these comes back through relations.go.
// Everything it would change is recorded here first (the edit target,
// the reaction record, the redacted tombstone), so the echo is either
// recognized as already applied or applies the very same change again,
// never a second one.

var (
	_ core.Editor  = (*Adapter)(nil)
	_ core.Deleter = (*Adapter)(nil)
	_ core.Reactor = (*Adapter)(nil)
)

// OwnReactionSender implements core.Reactor: handleReaction keys every
// reaction by its sender's MXID, ours included.
func (a *Adapter) OwnReactionSender() string { return a.client.UserID.String() }

// EditMessage implements core.Editor: an m.replace of our event with
// newText. Matrix has no edit window.
func (a *Adapter) EditMessage(ctx context.Context, item core.Item, newText string) (core.Receipt, error) {
	roomID, eventID, err := a.ownEvent(item, "edit")
	if err != nil {
		return core.Receipt{}, err
	}
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: newText}
	content.SetEdit(eventID)
	resp, err := a.client.SendMessageEvent(ctx, roomID, event.EventMessage, content)
	if err != nil {
		return core.Receipt{}, fmt.Errorf("matrix: edit %s: %w", item.ID, err)
	}
	editID := itemID(a.account, roomID, resp.EventID)
	a.mu.Lock()
	a.editTargets[editID] = item.ID
	if cached, ok := a.items[item.ID]; ok {
		cached.Body, cached.Edited = newText, true
		a.items[item.ID] = cached
	}
	a.mu.Unlock()
	return core.Receipt{ID: editID, Channel: core.ChannelMatrix, At: time.Now()}, nil
}

// DeleteMessage implements core.Deleter: a redaction of our event.
// Redactions are never encrypted, in any room.
func (a *Adapter) DeleteMessage(ctx context.Context, item core.Item) (core.Receipt, error) {
	roomID, eventID, err := a.ownEvent(item, "delete")
	if err != nil {
		return core.Receipt{}, err
	}
	resp, err := a.client.RedactEvent(ctx, roomID, eventID)
	if err != nil {
		return core.Receipt{}, fmt.Errorf("matrix: delete %s: %w", item.ID, err)
	}
	a.mu.Lock()
	if cached, ok := a.items[item.ID]; ok {
		cached.Body, cached.Deleted = "", true
		a.items[item.ID] = cached
	}
	a.mu.Unlock()
	return core.Receipt{ID: itemID(a.account, roomID, resp.EventID), Channel: core.ChannelMatrix, At: time.Now()}, nil
}

// React implements core.Reactor. core keeps one reaction per sender and
// so does WhatsApp, while Matrix allows several annotations per user on
// one event: to keep what others see equal to what bunker shows, a new
// reaction first redacts our other ones on that event, and an empty
// emoji redacts all of them. Reacting with the emoji we already have is
// a no-op that returns that reaction.
func (a *Adapter) React(ctx context.Context, item core.Item, emoji string) (core.Receipt, error) {
	roomID, eventID, err := a.itemEvent(item, "react")
	if err != nil {
		return core.Receipt{}, err
	}
	slot := reactionSlot{target: item.ID, user: a.OwnReactionSender()}
	live := a.ownReactions(ctx, slot)
	if emoji == "" && len(live) == 0 {
		return core.Receipt{}, fmt.Errorf("matrix: remove reaction on %s: no reaction of ours is known on it", item.ID)
	}

	var kept string
	for _, reactionID := range live {
		a.mu.Lock()
		rec := a.reactions[reactionID]
		a.mu.Unlock()
		if emoji != "" && rec.Key == emoji && kept == "" {
			kept = reactionID
			continue
		}
		if err := a.redactOwnReaction(ctx, roomID, reactionID, rec); err != nil {
			return core.Receipt{}, fmt.Errorf("matrix: react to %s: %w", item.ID, err)
		}
	}
	if emoji == "" {
		return core.Receipt{ID: item.ID, Channel: core.ChannelMatrix, At: time.Now()}, nil
	}
	if kept != "" {
		return core.Receipt{ID: kept, Channel: core.ChannelMatrix, At: time.Now()}, nil
	}

	content := &event.ReactionEventContent{RelatesTo: event.RelatesTo{Type: event.RelAnnotation, EventID: eventID, Key: emoji}}
	resp, err := a.sendReaction(ctx, roomID, content)
	if err != nil {
		return core.Receipt{}, fmt.Errorf("matrix: react to %s: %w", item.ID, err)
	}
	reactionID := itemID(a.account, roomID, resp)
	a.rememberOwnReaction(ctx, slot, reactionID, reactionRecord{Target: item.ID, User: slot.user, Key: emoji})
	return core.Receipt{ID: reactionID, Channel: core.ChannelMatrix, At: time.Now()}, nil
}

// sendReaction sends an m.reaction, encrypted in an encrypted room.
// mautrix's SendMessageEvent deliberately leaves reactions in the clear;
// bunker encrypts them like Element does, so the homeserver does not
// learn which emoji we reacted with (m.relates_to stays readable outside
// the ciphertext for aggregation), and encryptedHandler already decrypts
// reactions coming the other way.
func (a *Adapter) sendReaction(ctx context.Context, roomID id.RoomID, content *event.ReactionEventContent) (id.EventID, error) {
	if a.client.Crypto != nil {
		encrypted, err := a.client.StateStore.IsEncrypted(ctx, roomID)
		if err != nil {
			return "", fmt.Errorf("check room encryption: %w", err)
		}
		if encrypted {
			ciphertext, err := a.client.Crypto.Encrypt(ctx, roomID, event.EventReaction, content)
			if err != nil {
				return "", fmt.Errorf("encrypt reaction: %w", err)
			}
			resp, err := a.client.SendMessageEvent(ctx, roomID, event.EventEncrypted, ciphertext)
			if err != nil {
				return "", err
			}
			return resp.EventID, nil
		}
	}
	resp, err := a.client.SendMessageEvent(ctx, roomID, event.EventReaction, content)
	if err != nil {
		return "", err
	}
	return resp.EventID, nil
}

// ownReactions lists our live reaction events on slot's item, oldest
// first: the in-memory list when this adapter saw them, else (after a
// restart) the one the store shows, from its persisted record.
func (a *Adapter) ownReactions(ctx context.Context, slot reactionSlot) []string {
	a.mu.Lock()
	live := append([]string(nil), a.liveReactions[slot]...)
	sink := a.sink
	a.mu.Unlock()
	if len(live) > 0 || sink == nil {
		return live
	}
	shown, err := sink.Cursor(ctx, a.shownReactionKey(slot))
	if err != nil || shown == "" {
		return nil
	}
	rec, found, redacted := a.lookupReaction(ctx, sink, shown)
	if !found || redacted {
		return nil
	}
	a.mu.Lock()
	if _, ok := a.reactions[shown]; !ok {
		a.reactions[shown] = rec
		a.liveReactions[slot] = append(a.liveReactions[slot], shown)
	}
	a.mu.Unlock()
	return []string{shown}
}

// redactOwnReaction redacts one of our reaction events and forgets it the
// way handleRedaction would, so the echo of this redaction finds the
// tombstone and changes nothing.
func (a *Adapter) redactOwnReaction(ctx context.Context, roomID id.RoomID, reactionID string, rec reactionRecord) error {
	_, _, eventID, err := parseItemID(reactionID)
	if err != nil {
		return err
	}
	if _, err := a.client.RedactEvent(ctx, roomID, eventID); err != nil {
		return fmt.Errorf("redact reaction %s: %w", reactionID, err)
	}
	a.mu.Lock()
	sink := a.sink
	a.mu.Unlock()
	if sink != nil {
		a.removeReaction(ctx, sink, reactionID, rec)
		return nil
	}
	slot := reactionSlot{target: rec.Target, user: rec.User}
	a.mu.Lock()
	delete(a.reactions, reactionID)
	var kept []string
	for _, other := range a.liveReactions[slot] {
		if other != reactionID {
			kept = append(kept, other)
		}
	}
	if len(kept) > 0 {
		a.liveReactions[slot] = kept
	} else {
		delete(a.liveReactions, slot)
	}
	a.mu.Unlock()
	return nil
}

// rememberOwnReaction records a reaction we just sent exactly as
// handleReaction records one from sync, unless the echo already beat us
// to it. The store's reaction itself is written by core.Service.React.
func (a *Adapter) rememberOwnReaction(ctx context.Context, slot reactionSlot, reactionID string, rec reactionRecord) {
	a.mu.Lock()
	_, seen := a.reactions[reactionID]
	if !seen {
		a.reactions[reactionID] = rec
		a.liveReactions[slot] = append(a.liveReactions[slot], reactionID)
	}
	sink := a.sink
	a.mu.Unlock()
	if seen || sink == nil {
		return
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		core.LogSinkError(core.ChannelMatrix, a.account, "set_cursor", fmt.Errorf("matrix: encode reaction %s: %w", reactionID, err))
		return
	}
	if err := sink.SetCursor(ctx, a.reactionKey(reactionID), string(raw)); err != nil {
		core.LogSinkError(core.ChannelMatrix, a.account, "set_cursor", fmt.Errorf("matrix: remember reaction %s: %w", reactionID, err))
	}
	if err := sink.SetCursor(ctx, a.shownReactionKey(slot), reactionID); err != nil {
		core.LogSinkError(core.ChannelMatrix, a.account, "set_cursor", fmt.Errorf("matrix: remember shown reaction on %s: %w", slot.target, err))
	}
}

// itemEvent returns the room and event item.ID addresses, refusing an
// item of another account.
func (a *Adapter) itemEvent(item core.Item, op string) (id.RoomID, id.EventID, error) {
	account, roomID, eventID, err := parseItemID(item.ID)
	if err != nil {
		return "", "", fmt.Errorf("matrix: %s: %w", op, err)
	}
	if account != a.account || roomID == "" || eventID == "" {
		return "", "", fmt.Errorf("matrix: %s %s: not an event of account %q", op, item.ID, a.account)
	}
	return roomID, eventID, nil
}

// ownEvent is itemEvent for an edit or delete, which only the sender may
// make: Service already checked FromMe, and the homeserver would refuse
// a redaction of someone else's event without moderator power anyway.
func (a *Adapter) ownEvent(item core.Item, op string) (id.RoomID, id.EventID, error) {
	if !item.FromMe {
		return "", "", fmt.Errorf("matrix: %s %s: %w", op, item.ID, core.ErrNotOwnMessage)
	}
	return a.itemEvent(item, op)
}
