package whatsapp

import (
	"context"
	"sync"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// spySink is a core.Sink recording every call, for adapter tests. It
// never touches a real store.
type spySink struct {
	mu sync.Mutex

	upserted   []core.Item
	upsertErr  error
	markedRead []struct {
		id   string
		read bool
	}
	markReadErr          error
	cursors              map[string]string
	markedThreadReadUpTo []struct {
		channel core.Channel
		account string
		thread  string
		upTo    time.Time
	}
	markThreadReadUpToErr error
	// editErr, revokeErr and reactionErr, when set, fail the matching
	// write so tests can prove the adapter logs it instead of dropping it.
	editErr     error
	revokeErr   error
	reactionErr error
}

func newSpySink() *spySink {
	return &spySink{cursors: make(map[string]string)}
}

func (s *spySink) Upsert(_ context.Context, item core.Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.upserted = append(s.upserted, item)
	return nil
}

// MarkThreadReadUpTo records the call and, to keep items() a faithful
// mirror of what a real store would show, flips Unread=false on every
// matching upserted item (same channel/account/thread, not FromMe,
// Timestamp <= upTo).
func (s *spySink) MarkThreadReadUpTo(_ context.Context, channel core.Channel, account, thread string, upTo time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markThreadReadUpToErr != nil {
		return s.markThreadReadUpToErr
	}
	s.markedThreadReadUpTo = append(s.markedThreadReadUpTo, struct {
		channel core.Channel
		account string
		thread  string
		upTo    time.Time
	}{channel, account, thread, upTo})
	for i, item := range s.upserted {
		if item.Channel != channel || item.Account != account || item.Thread != thread {
			continue
		}
		if item.FromMe || !item.Unread || item.Timestamp.After(upTo) {
			continue
		}
		s.upserted[i].Unread = false
	}
	return nil
}

func (s *spySink) MarkRead(_ context.Context, id string, read bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markReadErr != nil {
		return s.markReadErr
	}
	s.markedRead = append(s.markedRead, struct {
		id   string
		read bool
	}{id, read})
	return nil
}

func (s *spySink) Cursor(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[key], nil
}

func (s *spySink) SetCursor(_ context.Context, key, val string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[key] = val
	return nil
}

func (s *spySink) EditItem(_ context.Context, id, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.editErr != nil {
		return s.editErr
	}
	for i, item := range s.upserted {
		if item.ID == id {
			s.upserted[i].Body = body
			s.upserted[i].Edited = true
			return nil
		}
	}
	return core.ErrNotFound
}

func (s *spySink) RevokeItem(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revokeErr != nil {
		return s.revokeErr
	}
	for i, item := range s.upserted {
		if item.ID == id {
			s.upserted[i].Body = ""
			s.upserted[i].Deleted = true
			return nil
		}
	}
	return core.ErrNotFound
}

func (s *spySink) SetReaction(_ context.Context, id string, reaction core.Reaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reactionErr != nil {
		return s.reactionErr
	}
	for i, item := range s.upserted {
		if item.ID == id {
			var kept []core.Reaction
			for _, r := range item.Reactions {
				if r.Sender != reaction.Sender {
					kept = append(kept, r)
				}
			}
			if reaction.Emoji != "" {
				kept = append(kept, reaction)
			}
			s.upserted[i].Reactions = kept
			return nil
		}
	}
	return core.ErrNotFound
}

func (s *spySink) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.upserted {
		if item.ID == id {
			s.upserted = append(s.upserted[:i], s.upserted[i+1:]...)
			return nil
		}
	}
	return core.ErrNotFound
}

// threadReadUpToCalls returns every recorded MarkThreadReadUpTo call.
func (s *spySink) threadReadUpToCalls() []struct {
	channel core.Channel
	account string
	thread  string
	upTo    time.Time
} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]struct {
		channel core.Channel
		account string
		thread  string
		upTo    time.Time
	}, len(s.markedThreadReadUpTo))
	copy(out, s.markedThreadReadUpTo)
	return out
}

func (s *spySink) items() []core.Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.Item, len(s.upserted))
	copy(out, s.upserted)
	return out
}
