package whatsapp

import (
	"context"
	"sync"

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
	markReadErr error
	cursors     map[string]string
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

func (s *spySink) items() []core.Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.Item, len(s.upserted))
	copy(out, s.upserted)
	return out
}
