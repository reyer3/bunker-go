package core

import (
	"context"
	"fmt"
)

// Conversation is one chat as a messaging app lists it: the newest item
// of its (channel, account, thread) plus how many of its items are still
// unread. Unlike an unread-only List, a fully read conversation is
// still a Conversation.
type Conversation struct {
	Last   Item `json:"last"`
	Unread int  `json:"unread"`
}

// ConversationFilter narrows Service.Conversations. A zero Channel or
// Account matches everything; Limit <= 0 uses DefaultConversationLimit.
type ConversationFilter struct {
	Channel Channel `json:"channel,omitempty"`
	Account string  `json:"account,omitempty"`
	Limit   int     `json:"limit,omitempty"`
}

const (
	// DefaultConversationLimit is how many conversations Conversations
	// returns when the filter names no limit.
	DefaultConversationLimit = 50
	// MaxConversationLimit caps a request so a client cannot ask the
	// store to materialise every conversation it has ever seen.
	MaxConversationLimit = 500
)

// ConversationLister is an optional Store capability: the newest item and
// unread count per conversation, newest conversation first. It is
// separate from Store so the narrow in-memory stores tests use keep
// compiling; internal/store implements it.
type ConversationLister interface {
	Conversations(ctx context.Context, filter ConversationFilter) ([]Conversation, error)
}

// Conversations lists conversations ordered by their newest item, newest
// first, each with its unread count. It reads the store only: nothing
// reaches a channel.
func (s *Service) Conversations(ctx context.Context, filter ConversationFilter) ([]Conversation, error) {
	if filter.Limit <= 0 {
		filter.Limit = DefaultConversationLimit
	}
	if filter.Limit > MaxConversationLimit {
		filter.Limit = MaxConversationLimit
	}
	lister, ok := s.store.(ConversationLister)
	if !ok {
		return nil, fmt.Errorf("core: store cannot list conversations: %w", ErrUnsupported)
	}
	out, err := lister.Conversations(ctx, filter)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Conversation{}
	}
	return out, nil
}
