package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// maxEditHops bounds how far resolveEditTarget follows an m.replace chain
// fetched from the homeserver. Well-behaved clients always point an edit
// at the original event, so a longer chain means a malformed or hostile
// one, and following it forever would stall the sync loop.
const maxEditHops = 5

// reactionRedacted is the value a reaction's cursor record is overwritten
// with once the reaction is redacted. core.Sink has no way to delete a
// cursor, and keeping a tombstone (instead of "") lets a repeated
// redaction be recognized as "already handled" rather than mistaken for
// the redaction of a message.
const reactionRedacted = "redacted"

// reactionRecord is what a reaction event means for the store: which item
// it annotates, whose reaction it is and which emoji. A redaction only
// names the reaction's own event id, so this mapping is the only way to
// know which reaction to take away.
type reactionRecord struct {
	Target string `json:"target"` // item id of the annotated message
	User   string `json:"user"`   // MXID of the reacting user
	Key    string `json:"key"`    // the annotation key (the emoji)
}

// reactionSlot is one user's reactions to one item. core.Reaction holds a
// single emoji per sender, while Matrix lets a user add several
// annotations to the same event, so the slot keeps every live one in
// arrival order and the store shows the newest.
type reactionSlot struct {
	target string
	user   string
}

// relationHandler is the syncer handler for event types that are never
// timeline items of their own: m.reaction and m.room.redaction.
func (a *Adapter) relationHandler(sink core.Sink) func(context.Context, *event.Event) {
	return func(ctx context.Context, evt *event.Event) {
		a.applyRelation(ctx, sink, evt)
	}
}

// applyRelation applies evt to the item it relates to if evt is an edit,
// a reaction or a redaction, and reports whether it was one. Callers stop
// there when it returns true: without that check an edit (an ordinary
// m.room.message carrying "* new text") or a decrypted reaction would be
// upserted as a new, bogus timeline item.
func (a *Adapter) applyRelation(ctx context.Context, sink core.Sink, evt *event.Event) bool {
	switch evt.Type {
	case event.EventReaction:
		a.handleReaction(ctx, sink, evt)
		return true
	case event.EventRedaction:
		a.handleRedaction(ctx, sink, evt)
		return true
	case event.EventMessage, event.EventSticker:
		content := evt.Content.AsMessage()
		if content.RelatesTo.GetReplaceID() == "" {
			return false
		}
		a.handleEdit(ctx, sink, evt, content)
		return true
	default:
		return false
	}
}

// handleEdit applies an m.replace edit to the original item's body. Edits
// from anyone but the original sender are ignored, as Matrix clients do,
// since otherwise any room member could rewrite someone else's message.
func (a *Adapter) handleEdit(ctx context.Context, sink core.Sink, evt *event.Event, content *event.MessageEventContent) {
	editID := itemID(a.account, evt.RoomID, evt.ID)
	if content.NewContent == nil {
		log.Printf("matrix: ignoring edit %s: no m.new_content", editID)
		return
	}
	target, originalSender, err := a.resolveEditTarget(ctx, evt.RoomID, content.RelatesTo.GetReplaceID())
	if err != nil {
		log.Printf("matrix: ignoring edit %s: %v", editID, err)
		return
	}
	if originalSender != evt.Sender {
		log.Printf("matrix: ignoring edit %s of %s: sender %s is not the original sender %s", editID, target, evt.Sender, originalSender)
		return
	}
	body := content.NewContent.Body

	a.mu.Lock()
	a.editTargets[editID] = target
	if item, ok := a.items[target]; ok {
		item.Body = body
		item.Edited = true
		a.items[target] = item
	}
	a.mu.Unlock()

	// The edit is consumed from the sync stream and never redelivered, so
	// a failed write can only be logged.
	if err := sink.EditItem(ctx, target, body); err != nil {
		core.LogSinkError(core.ChannelMatrix, a.account, "edit_item", fmt.Errorf("matrix: edit %s of %s: %w", editID, target, err))
	}
}

// resolveEditTarget returns the item id and sender of the original event
// an edit ultimately replaces. Edits of edits are resolved back to the
// original, whether the intermediate edit was seen by this adapter or has
// to be fetched (after a restart, when the in-memory caches are empty).
func (a *Adapter) resolveEditTarget(ctx context.Context, roomID id.RoomID, eventID id.EventID) (string, id.UserID, error) {
	for range maxEditHops {
		target := itemID(a.account, roomID, eventID)
		a.mu.Lock()
		if original, ok := a.editTargets[target]; ok {
			target = original
		}
		item, cached := a.items[target]
		a.mu.Unlock()
		if cached {
			return target, id.UserID(item.From.ID), nil
		}

		_, _, targetEventID, err := parseItemID(target)
		if err != nil {
			return "", "", err
		}
		evt, err := a.client.GetEvent(ctx, roomID, targetEventID)
		if err != nil {
			return "", "", fmt.Errorf("fetch edited event %s: %w", targetEventID, err)
		}
		if err := evt.Content.ParseRaw(evt.Type); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
			return "", "", fmt.Errorf("parse edited event %s: %w", targetEventID, err)
		}
		replaces := relatesTo(evt).GetReplaceID()
		if replaces == "" {
			return target, evt.Sender, nil
		}
		eventID = replaces
	}
	return "", "", fmt.Errorf("edit chain from %s is longer than %d hops", eventID, maxEditHops)
}

// relatesTo returns evt's m.relates_to whether it is a plain message or
// still encrypted: clients copy m.relates_to to the unencrypted outer
// content precisely so relations can be followed without decrypting.
func relatesTo(evt *event.Event) *event.RelatesTo {
	switch content := evt.Content.Parsed.(type) {
	case *event.MessageEventContent:
		return content.RelatesTo
	case *event.EncryptedEventContent:
		return content.RelatesTo
	case *event.ReactionEventContent:
		return &content.RelatesTo
	default:
		return nil
	}
}

// handleReaction stores an m.annotation as the reacting user's reaction to
// the target item, and remembers the reaction event so its later
// redaction can remove it.
func (a *Adapter) handleReaction(ctx context.Context, sink core.Sink, evt *event.Event) {
	reactionID := itemID(a.account, evt.RoomID, evt.ID)
	rel := evt.Content.AsReaction().RelatesTo
	targetEvent, key := rel.GetAnnotationID(), rel.GetAnnotationKey()
	if targetEvent == "" || key == "" {
		log.Printf("matrix: ignoring reaction %s: no m.annotation target or key", reactionID)
		return
	}
	rec := reactionRecord{Target: itemID(a.account, evt.RoomID, targetEvent), User: evt.Sender.String(), Key: key}
	slot := reactionSlot{target: rec.Target, user: rec.User}

	a.mu.Lock()
	_, seen := a.reactions[reactionID]
	if !seen {
		a.reactions[reactionID] = rec
		a.liveReactions[slot] = append(a.liveReactions[slot], reactionID)
	}
	a.mu.Unlock()
	if seen {
		// cryptohelper re-dispatches decrypted events to the same
		// handlers encryptedHandler already ran them through, so the
		// same reaction can arrive twice; applying it once is enough.
		return
	}

	raw, err := json.Marshal(rec)
	if err != nil {
		log.Printf("matrix: encode reaction %s: %v", reactionID, err)
		return
	}
	if err := sink.SetCursor(ctx, a.reactionKey(reactionID), string(raw)); err != nil {
		core.LogSinkError(core.ChannelMatrix, a.account, "set_cursor", fmt.Errorf("matrix: remember reaction %s: %w", reactionID, err))
	}
	a.showReaction(ctx, sink, slot, reactionID, key)
}

// showReaction makes key the reaction the store holds for slot, and
// records which reaction event it came from so a redaction of an older,
// hidden reaction does not wipe the visible one. An empty key removes the
// slot's reaction.
func (a *Adapter) showReaction(ctx context.Context, sink core.Sink, slot reactionSlot, reactionID, key string) {
	if err := sink.SetReaction(ctx, slot.target, core.Reaction{Sender: slot.user, Emoji: key}); err != nil {
		core.LogSinkError(core.ChannelMatrix, a.account, "set_reaction", fmt.Errorf("matrix: reaction %q by %s on %s: %w", key, slot.user, slot.target, err))
	}
	if err := sink.SetCursor(ctx, a.shownReactionKey(slot), reactionID); err != nil {
		core.LogSinkError(core.ChannelMatrix, a.account, "set_cursor", fmt.Errorf("matrix: remember shown reaction on %s: %w", slot.target, err))
	}
}

// lookupReaction finds what reaction event reactionID meant: from memory
// first, else from the record handleReaction persisted, so a reaction
// redacted after a daemon restart is still recognized. redacted reports a
// reaction whose redaction was already applied.
func (a *Adapter) lookupReaction(ctx context.Context, sink core.Sink, reactionID string) (rec reactionRecord, found, redacted bool) {
	a.mu.Lock()
	rec, found = a.reactions[reactionID]
	a.mu.Unlock()
	if found {
		return rec, true, false
	}
	raw, err := sink.Cursor(ctx, a.reactionKey(reactionID))
	if err != nil {
		core.LogSinkError(core.ChannelMatrix, a.account, "cursor", fmt.Errorf("matrix: look up reaction %s: %w", reactionID, err))
		return reactionRecord{}, false, false
	}
	switch raw {
	case "":
		return reactionRecord{}, false, false
	case reactionRedacted:
		return reactionRecord{}, true, true
	}
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		log.Printf("matrix: decode reaction record %s: %v", reactionID, err)
		return reactionRecord{}, false, false
	}
	return rec, true, false
}

// handleRedaction applies an m.room.redaction: a redacted reaction is
// taken off its item, a redacted message is revoked (its row kept, its
// body cleared).
func (a *Adapter) handleRedaction(ctx context.Context, sink core.Sink, evt *event.Event) {
	redacts := evt.Redacts
	if redacts == "" {
		// Room v11 moved "redacts" from the top level into content.
		redacts = evt.Content.AsRedaction().Redacts
	}
	if redacts == "" {
		log.Printf("matrix: ignoring redaction %s: no redacted event id", itemID(a.account, evt.RoomID, evt.ID))
		return
	}
	target := itemID(a.account, evt.RoomID, redacts)

	if rec, found, redacted := a.lookupReaction(ctx, sink, target); found {
		if !redacted {
			a.removeReaction(ctx, sink, target, rec)
		}
		return
	}

	a.mu.Lock()
	original, isEdit := a.editTargets[target]
	a.mu.Unlock()
	if isEdit {
		// Reverting to the previous version would need every earlier
		// edit's content, which is not kept; the item keeps the redacted
		// edit's text rather than losing its body altogether.
		log.Printf("matrix: redaction of edit %s of %s: reverting an edit is not supported, body left as is", target, original)
		return
	}

	a.mu.Lock()
	if item, ok := a.items[target]; ok {
		item.Body = ""
		item.Deleted = true
		a.items[target] = item
	}
	a.mu.Unlock()
	if err := sink.RevokeItem(ctx, target); err != nil {
		core.LogSinkError(core.ChannelMatrix, a.account, "revoke_item", fmt.Errorf("matrix: redaction of %s: %w", target, err))
	}
}

// removeReaction takes the redacted reaction reactionID off its item. If
// the same user still has another live reaction on that item, the newest
// of those becomes the one shown, since core keeps one per sender.
func (a *Adapter) removeReaction(ctx context.Context, sink core.Sink, reactionID string, rec reactionRecord) {
	slot := reactionSlot{target: rec.Target, user: rec.User}

	a.mu.Lock()
	delete(a.reactions, reactionID)
	live := a.liveReactions[slot]
	kept := live[:0]
	for _, other := range live {
		if other != reactionID {
			kept = append(kept, other)
		}
	}
	var nextID, nextKey string
	if len(kept) > 0 {
		nextID = kept[len(kept)-1]
		nextKey = a.reactions[nextID].Key
		a.liveReactions[slot] = kept
	} else {
		delete(a.liveReactions, slot)
	}
	a.mu.Unlock()

	if err := sink.SetCursor(ctx, a.reactionKey(reactionID), reactionRedacted); err != nil {
		core.LogSinkError(core.ChannelMatrix, a.account, "set_cursor", fmt.Errorf("matrix: forget reaction %s: %w", reactionID, err))
	}

	shown, err := sink.Cursor(ctx, a.shownReactionKey(slot))
	if err != nil {
		// Without knowing which reaction is visible, removing the
		// redacted one is the safer error: it is what was asked.
		core.LogSinkError(core.ChannelMatrix, a.account, "cursor", fmt.Errorf("matrix: look up shown reaction on %s: %w", rec.Target, err))
	} else if shown != "" && shown != reactionID {
		return // an older, hidden reaction was redacted: nothing visible changes
	}
	a.showReaction(ctx, sink, slot, nextID, nextKey)
}

// reactionKey is the cursor key under which a reaction event's record is
// persisted.
func (a *Adapter) reactionKey(reactionID string) string {
	return fmt.Sprintf("matrix:%s:reaction:%s", a.account, reactionID)
}

// shownReactionKey is the cursor key recording which reaction event the
// store currently shows for one user on one item.
func (a *Adapter) shownReactionKey(slot reactionSlot) string {
	return fmt.Sprintf("matrix:%s:reaction-shown:%s|%s", a.account, slot.target, slot.user)
}
