package core

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Edit, delete and react (issues #76 and #17) change a message that is
// already in the conversation rather than adding a new one. They share
// one shape with Send and Reply: a dry-run returns the Plan alone and
// never reaches the adapter, a real call goes through the adapter's
// optional capability (Editor, Deleter, Reactor) and, once the channel
// accepted it, applies the same store change the inbound path applies
// when the other side does it (Sink.EditItem, RevokeItem, SetReaction),
// so what bunker shows matches what everyone else sees.

// maxReactionRunes bounds a reaction: the longest emoji sequences (a
// family joined with zero-width joiners, a flag with its tag characters)
// stay well under it, while a sentence does not.
const maxReactionRunes = 16

// SetMessageClock overrides the clock EditMessage and DeleteMessage
// measure a message's age against the channel's MessageWindows with.
func (s *Service) SetMessageClock(now func() time.Time) { s.messageClock = now }

// EditMessage replaces the text of item id, which this account must have
// sent, with newText. dryRun returns the Plan alone. A real edit whose
// ctx carries an idempotency key is applied at most once per key.
func (s *Service) EditMessage(ctx context.Context, id, newText string, dryRun bool) (Plan, Receipt, error) {
	return s.idempotent(ctx, dryRun, []string{"edit", id, newText}, func(dryRun bool) (Plan, Receipt, error) {
		return s.editMessage(ctx, id, newText, dryRun)
	})
}

// DeleteMessage deletes item id, which this account must have sent, for
// everyone in the conversation. dryRun returns the Plan alone.
func (s *Service) DeleteMessage(ctx context.Context, id string, dryRun bool) (Plan, Receipt, error) {
	return s.idempotent(ctx, dryRun, []string{"delete", id}, func(dryRun bool) (Plan, Receipt, error) {
		return s.deleteMessage(ctx, id, dryRun)
	})
}

// React sets this account's reaction to item id (anyone's message) to
// emoji; an empty emoji removes it. dryRun returns the Plan alone.
func (s *Service) React(ctx context.Context, id, emoji string, dryRun bool) (Plan, Receipt, error) {
	return s.idempotent(ctx, dryRun, []string{"react", id, emoji}, func(dryRun bool) (Plan, Receipt, error) {
		return s.react(ctx, id, emoji, dryRun)
	})
}

// idempotent runs do through the idempotency cache when ctx carries a key
// and the call is real, exactly like Send and Reply: a retried edit,
// delete or reaction after a client timeout replays the first receipt
// instead of reaching the channel twice.
func (s *Service) idempotent(ctx context.Context, dryRun bool, parts []string, do func(dryRun bool) (Plan, Receipt, error)) (Plan, Receipt, error) {
	key := IdempotencyKey(ctx)
	if dryRun || key == "" {
		return do(dryRun)
	}
	return s.idempotency.do(ctx, key, requestFingerprint(parts...), func() (Plan, Receipt, error) {
		return do(false)
	})
}

// messageTarget loads item id and the adapter serving it, and rejects an
// item that was already deleted: there is nothing left to change.
func (s *Service) messageTarget(ctx context.Context, action, id string) (Item, Adapter, error) {
	item, err := s.store.Get(ctx, id)
	if err != nil {
		return Item{}, nil, fmt.Errorf("core: %s %s: %w", action, id, err)
	}
	if item.Deleted {
		return Item{}, nil, fmt.Errorf("core: %s %s: the message was deleted", action, id)
	}
	adapter, err := s.adapterFor(item.Channel, item.Account)
	if err != nil {
		return Item{}, nil, err
	}
	return item, adapter, nil
}

// checkOwnWithin rejects an item this account did not send, or one older
// than the adapter's window for action (see MessageWindowLimiter).
func (s *Service) checkOwnWithin(item Item, adapter Adapter, action string, window func(MessageWindows) time.Duration) error {
	if !item.FromMe {
		return fmt.Errorf("core: %s %s: %w", action, item.ID, ErrNotOwnMessage)
	}
	limiter, ok := adapter.(MessageWindowLimiter)
	if !ok {
		return nil
	}
	limit := window(limiter.MessageWindows())
	if limit <= 0 {
		return nil
	}
	if item.Timestamp.IsZero() {
		return fmt.Errorf("core: %s %s: the message has no send time to check against %s's %s window: %w", action, item.ID, item.Channel, limit, ErrWindowExpired)
	}
	if age := s.messageClock().Sub(item.Timestamp); age > limit {
		return fmt.Errorf("core: %s %s: sent %s ago, %s allows %s: %w", action, item.ID, age.Round(time.Minute), item.Channel, limit, ErrWindowExpired)
	}
	return nil
}

func (s *Service) editMessage(ctx context.Context, id, newText string, dryRun bool) (Plan, Receipt, error) {
	if strings.TrimSpace(newText) == "" {
		return Plan{}, Receipt{}, fmt.Errorf("core: edit %s: the new text is empty (delete the message instead)", id)
	}
	item, adapter, err := s.messageTarget(ctx, "edit", id)
	if err != nil {
		return Plan{}, Receipt{}, err
	}
	editor, ok := adapter.(Editor)
	if !ok {
		return Plan{}, Receipt{}, fmt.Errorf("core: adapter %s/%s cannot edit messages: %w", item.Channel, item.Account, ErrUnsupported)
	}
	if err := s.checkOwnWithin(item, adapter, "edit", func(w MessageWindows) time.Duration { return w.Edit }); err != nil {
		return Plan{}, Receipt{}, err
	}
	if len(item.Attachments) > 0 {
		// Sending the new text as a plain message would drop the media
		// on the other side; editing a caption is not implemented.
		return Plan{}, Receipt{}, fmt.Errorf("core: edit %s: editing a message with attachments: %w", id, ErrUnsupported)
	}
	if newText == item.Body {
		return Plan{}, Receipt{}, fmt.Errorf("core: edit %s: the new text is the same as the current one", id)
	}
	plan := Plan{Action: "edit", Channel: item.Channel, Account: item.Account, Target: id, Preview: newText}
	if dryRun {
		return plan, Receipt{}, nil
	}
	receipt, err := editor.EditMessage(ctx, item, newText)
	if err != nil {
		return Plan{}, Receipt{}, fmt.Errorf("core: edit failed: %w", err)
	}
	// The channel already has the edit, so a failed local write is
	// logged rather than returned: an error would invite a retry that
	// edits again.
	if err := s.store.EditItem(ctx, id, newText); err != nil {
		LogSinkError(item.Channel, item.Account, "edit_item", err)
	}
	return plan, receipt, nil
}

func (s *Service) deleteMessage(ctx context.Context, id string, dryRun bool) (Plan, Receipt, error) {
	item, adapter, err := s.messageTarget(ctx, "delete", id)
	if err != nil {
		return Plan{}, Receipt{}, err
	}
	deleter, ok := adapter.(Deleter)
	if !ok {
		return Plan{}, Receipt{}, fmt.Errorf("core: adapter %s/%s cannot delete messages: %w", item.Channel, item.Account, ErrUnsupported)
	}
	if err := s.checkOwnWithin(item, adapter, "delete", func(w MessageWindows) time.Duration { return w.Delete }); err != nil {
		return Plan{}, Receipt{}, err
	}
	// Preview is the text about to disappear, so a confirmation shows
	// what is being deleted rather than a bare id.
	plan := Plan{Action: "delete", Channel: item.Channel, Account: item.Account, Target: id, Preview: item.Body}
	if dryRun {
		return plan, Receipt{}, nil
	}
	receipt, err := deleter.DeleteMessage(ctx, item)
	if err != nil {
		return Plan{}, Receipt{}, fmt.Errorf("core: delete failed: %w", err)
	}
	if err := s.store.RevokeItem(ctx, id); err != nil {
		LogSinkError(item.Channel, item.Account, "revoke_item", err)
	}
	return plan, receipt, nil
}

func (s *Service) react(ctx context.Context, id, emoji string, dryRun bool) (Plan, Receipt, error) {
	if err := validateReaction(emoji); err != nil {
		return Plan{}, Receipt{}, fmt.Errorf("core: react %s: %w", id, err)
	}
	item, adapter, err := s.messageTarget(ctx, "react", id)
	if err != nil {
		return Plan{}, Receipt{}, err
	}
	reactor, ok := adapter.(Reactor)
	if !ok {
		return Plan{}, Receipt{}, fmt.Errorf("core: adapter %s/%s cannot react: %w", item.Channel, item.Account, ErrUnsupported)
	}
	plan := Plan{Action: "react", Channel: item.Channel, Account: item.Account, Target: id, Preview: emoji}
	if dryRun {
		return plan, Receipt{}, nil
	}
	receipt, err := reactor.React(ctx, item, emoji)
	if err != nil {
		return Plan{}, Receipt{}, fmt.Errorf("core: react failed: %w", err)
	}
	if err := s.store.SetReaction(ctx, id, Reaction{Sender: reactor.OwnReactionSender(), Emoji: emoji}); err != nil {
		LogSinkError(item.Channel, item.Account, "set_reaction", err)
	}
	return plan, receipt, nil
}

// validateReaction accepts "" (remove) or one emoji: no letters, spaces
// or control characters, since a channel would carry any string but
// every client renders a reaction as a single glyph.
func validateReaction(emoji string) error {
	if emoji == "" {
		return nil
	}
	if !utf8.ValidString(emoji) || utf8.RuneCountInString(emoji) > maxReactionRunes {
		return fmt.Errorf("reaction %q is not a single emoji", emoji)
	}
	for _, r := range emoji {
		if unicode.IsLetter(r) || unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("reaction %q is not a single emoji", emoji)
		}
	}
	return nil
}
