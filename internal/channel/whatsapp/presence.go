package whatsapp

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/reyer3/bunker-go/internal/core"
)

var (
	_ core.PresenceProvider               = (*Adapter)(nil)
	_ core.PresenceAvailabilityController = (*Adapter)(nil)
	_ core.TypingSender                   = (*Adapter)(nil)
)

// SetPresenceAvailable implements core.PresenceAvailabilityController:
// K3's privacy-critical availability lease. WhatsApp only delivers a
// contact's presence/typing updates to a client that is itself
// "available" (which also shows the user online to contacts), so
// core.Service keeps every account unavailable by default and only
// calls this while a chat view is open AND focused. Becoming available
// broadcasts that first, then subscribes to thread — never the reverse
// order, which would ask for updates the server would not yet deliver.
// Becoming unavailable only broadcasts that: whatsmeow exposes no
// explicit "unsubscribe", and going unavailable is itself what stops the
// server sending further updates (WhatsApp's presence delivery is
// reciprocal).
func (a *Adapter) SetPresenceAvailable(ctx context.Context, available bool, thread string) error {
	state := types.PresenceUnavailable
	if available {
		state = types.PresenceAvailable
	}
	if err := a.cli.SendPresence(ctx, state); err != nil {
		return fmt.Errorf("whatsapp: send presence %s: %w", state, err)
	}
	if !available || thread == "" {
		return nil
	}

	jid, err := types.ParseJID(thread)
	if err != nil {
		return fmt.Errorf("whatsapp: subscribe presence %q: %w", thread, err)
	}
	if err := a.cli.SubscribePresence(ctx, jid); err != nil {
		return fmt.Errorf("whatsapp: subscribe presence %s: %w", thread, err)
	}
	return nil
}

// Presence implements core.PresenceProvider: it answers from the
// in-memory cache events.Presence/events.ChatPresence populate, never a
// network round trip. A thread never subscribed to (or not yet
// answered) reports State "unknown".
func (a *Adapter) Presence(_ context.Context, thread string) (core.Presence, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.presence[thread]
	if !ok {
		return core.Presence{State: "unknown"}, nil
	}
	return p, nil
}

// SendTyping implements core.TypingSender: composing maps to
// types.ChatPresenceComposing, otherwise types.ChatPresencePaused. The
// TUI throttles the actual send rate; this is a thin forward.
func (a *Adapter) SendTyping(ctx context.Context, thread string, composing bool) error {
	jid, err := types.ParseJID(thread)
	if err != nil {
		return fmt.Errorf("whatsapp: typing %q: %w", thread, err)
	}
	state := types.ChatPresencePaused
	if composing {
		state = types.ChatPresenceComposing
	}
	if err := a.cli.SendChatPresence(ctx, jid, state, ""); err != nil {
		return fmt.Errorf("whatsapp: send chat presence: %w", err)
	}
	return nil
}

// handlePresence updates the presence cache from an events.Presence:
// online/offline plus LastSeen for evt.From.
func (a *Adapter) handlePresence(evt *events.Presence) {
	state := "online"
	if evt.Unavailable {
		state = "offline"
	}
	key := evt.From.String()

	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.presence[key]
	p.State = state
	p.LastSeen = evt.LastSeen
	a.presence[key] = p
}

// handleChatPresence updates the presence cache from an
// events.ChatPresence: composing adds evt.Sender to Typers and sets
// State "typing"; paused removes it, falling back to State "online"
// when no one is left typing (an explicit online/offline events.Presence
// for this thread, if one arrives later, takes precedence again).
func (a *Adapter) handleChatPresence(evt *events.ChatPresence) {
	key := evt.Chat.String()
	sender := evt.Sender.String()

	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.presence[key]
	switch evt.State {
	case types.ChatPresenceComposing:
		p.State = "typing"
		p.Typers = addTyper(p.Typers, sender)
	case types.ChatPresencePaused:
		p.Typers = removeTyper(p.Typers, sender)
		if len(p.Typers) == 0 && p.State == "typing" {
			p.State = "online"
		}
	}
	a.presence[key] = p
}

// addTyper appends sender to typers if it is not already present.
func addTyper(typers []string, sender string) []string {
	for _, t := range typers {
		if t == sender {
			return typers
		}
	}
	return append(typers, sender)
}

// removeTyper returns typers with sender removed, preserving order.
func removeTyper(typers []string, sender string) []string {
	out := make([]string, 0, len(typers))
	for _, t := range typers {
		if t != sender {
			out = append(out, t)
		}
	}
	return out
}
