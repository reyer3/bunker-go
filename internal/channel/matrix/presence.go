package matrix

import (
	"context"
	"fmt"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// typingSendTimeout is how long the homeserver keeps showing the user's
// own typing indicator after SendTyping(composing=true), before the TUI
// must re-send it (at most every 5s, per its own throttling contract) or
// it lapses. It is an implementation detail: matches human.go's own
// maxTypingDuration so a single SendTyping call behaves the same as
// Send's built-in typing-notification window.
const typingSendTimeout = maxTypingDuration

// Presence implements core.PresenceProvider: it reports the current
// m.typing state for thread (a room id), never a network round trip. A
// room with no one currently typing (including one never mentioned by
// an m.typing event at all) reports State "unknown". Matrix has no
// online/offline presence in this implementation (see
// odd/tasks/conversation-view.md's Decisions — only typing is in
// scope), so State is either "typing" or "unknown".
func (a *Adapter) Presence(_ context.Context, thread string) (core.Presence, error) {
	a.mu.Lock()
	typers := a.typers[id.RoomID(thread)]
	a.mu.Unlock()

	if len(typers) == 0 {
		return core.Presence{State: "unknown"}, nil
	}
	out := make([]string, len(typers))
	copy(out, typers)
	return core.Presence{State: "typing", Typers: out}, nil
}

// SendTyping implements core.TypingSender: composing=true shows the user
// typing in thread for typingSendTimeout; composing=false clears it
// immediately (timeout 0), exactly like Send's own typing-off call. The
// TUI throttles the actual send rate itself.
func (a *Adapter) SendTyping(ctx context.Context, thread string, composing bool) error {
	timeout := time.Duration(0)
	if composing {
		timeout = typingSendTimeout
	}
	if _, err := a.client.UserTyping(ctx, id.RoomID(thread), composing, timeout); err != nil {
		return fmt.Errorf("matrix: send typing to %s: %w", thread, err)
	}
	return nil
}

// handleTyping updates the typers cache from an m.typing ephemeral
// event: it always carries the room's full current set of typing user
// ids (never an add/remove delta), so this simply replaces the room's
// entry, excluding this account's own user id.
func (a *Adapter) handleTyping(_ context.Context, evt *event.Event) {
	content := evt.Content.AsTyping()
	ids := make([]string, 0, len(content.UserIDs))
	for _, u := range content.UserIDs {
		if u == a.client.UserID {
			continue
		}
		ids = append(ids, u.String())
	}

	a.mu.Lock()
	a.typers[evt.RoomID] = ids
	a.mu.Unlock()
}
