package core

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultAwaitingDays is how long a conversation must have gone
	// unanswered since the user's last message to count as awaiting a
	// reply, when the filter does not say.
	DefaultAwaitingDays = 3
	// AwaitingPreviewLen bounds an Awaiting row's preview in characters
	// (an ellipsis follows when it was cut).
	AwaitingPreviewLen = 80
	// maxAwaitingRows bounds one listing.
	maxAwaitingRows = 200
)

// AwaitingFilter narrows Service.AwaitingReply. Days <= 0 means
// DefaultAwaitingDays; Limit <= 0 (or above the row bound) returns at
// most maxAwaitingRows. Groups are left out unless Groups is set.
type AwaitingFilter struct {
	Days    int     `json:"days,omitempty"`
	Channel Channel `json:"channel,omitempty"`
	Account string  `json:"account,omitempty"`
	Groups  bool    `json:"groups,omitempty"`
	Limit   int     `json:"limit,omitempty"`
}

// AwaitingQuery is what Service.AwaitingReply asks the store: the
// conversations whose newest item is the user's, not deleted, and older
// than Before.
type AwaitingQuery struct {
	Before  time.Time
	Channel Channel
	Account string
	Groups  bool
	Limit   int
}

// AwaitingLister is an optional Store capability listing the
// conversations awaiting a reply. internal/store implements it.
type AwaitingLister interface {
	// AwaitingReply returns the newest item of each conversation whose
	// newest item (by timestamp, then id) the user sent, is not deleted
	// and is older than q.Before, newest first, at most q.Limit. Items
	// without a thread and the user's own chat (IsSelfChat) are never
	// conversations awaiting anyone; groups (IsGroupConversation) are
	// left out unless q.Groups. ThreadName falls back to the thread's
	// newest known name, as Conversations does.
	AwaitingReply(ctx context.Context, q AwaitingQuery) ([]Item, error)
}

// Awaiting is a conversation where the user wrote last and nobody has
// answered for Days whole days. It is derived from the store on every
// call, never stored: a reply makes it disappear by itself.
type Awaiting struct {
	ItemID  string  `json:"item_id"`
	Channel Channel `json:"channel"`
	Account string  `json:"account"`
	Thread  string  `json:"thread"`
	// Person is who the user is waiting on: the chat's name, or a mail's
	// first recipient.
	Person string `json:"person"`
	// Preview is the start of the user's last message (a mail's subject).
	Preview string    `json:"preview"`
	Sent    time.Time `json:"sent"`
	Days    int       `json:"days"`
}

// IsGroupConversation reports whether a conversation has more than one
// other person in it, which "awaiting a reply" skips by default: in a
// group, being the last to write rarely means someone owes an answer.
//
// WhatsApp says so in the chat id: groups end in @g.us, and broadcast
// lists (@broadcast) and channels (@newsletter) are not conversations
// with one person either. Matrix rooms carry no reliable direct-chat flag
// in the store, so a room counts as a group when others, the number of
// distinct people other than the user who wrote in it, is more than one;
// a room where nobody else has written yet is a direct chat nobody
// answered. Mail threads are never groups: a mail with several
// recipients still expects a reply.
func IsGroupConversation(channel Channel, thread string, others int) bool {
	switch channel {
	case ChannelWhatsApp:
		return strings.HasSuffix(thread, "@g.us") || strings.HasSuffix(thread, "@broadcast") ||
			strings.HasSuffix(thread, "@newsletter")
	case ChannelMatrix:
		return others > 1
	}
	return false
}

// IsSelfChat reports whether a WhatsApp chat is the user's own ("message
// yourself"): its chat id is the sender's own id, from any device
// (51900000009:12@s.whatsapp.net is device 12 of 51900000009). Nobody
// owes a reply there. Other channels have no such chat.
func IsSelfChat(channel Channel, thread, fromID string) bool {
	if channel != ChannelWhatsApp || fromID == "" {
		return false
	}
	return waBareJID(thread) == waBareJID(fromID)
}

// waBareJID drops a WhatsApp JID's device suffix: "user:device@server"
// becomes "user@server".
func waBareJID(jid string) string {
	user, server, _ := strings.Cut(jid, "@")
	user, _, _ = strings.Cut(user, ":")
	return user + "@" + server
}

// AwaitingReply lists the conversations awaiting a reply: those where the
// user wrote last, at least filter.Days days ago (see AwaitingLister),
// newest first. It reads the store only.
func (s *Service) AwaitingReply(ctx context.Context, filter AwaitingFilter) ([]Awaiting, error) {
	lister, ok := s.store.(AwaitingLister)
	if !ok {
		return nil, fmt.Errorf("core: store cannot list conversations awaiting a reply: %w", ErrUnsupported)
	}
	days := filter.Days
	if days <= 0 {
		days = DefaultAwaitingDays
	}
	limit := filter.Limit
	if limit <= 0 || limit > maxAwaitingRows {
		limit = maxAwaitingRows
	}
	now := s.queryClock()
	items, err := lister.AwaitingReply(ctx, AwaitingQuery{
		Before: now.Add(-time.Duration(days) * 24 * time.Hour), Channel: filter.Channel,
		Account: filter.Account, Groups: filter.Groups, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Awaiting, 0, len(items))
	for _, item := range items {
		out = append(out, Awaiting{
			ItemID: item.ID, Channel: item.Channel, Account: item.Account, Thread: item.Thread,
			Person: firstNonEmpty(otherSide(item), item.Thread), Preview: awaitingPreview(item),
			Sent: item.Timestamp, Days: int(now.Sub(item.Timestamp) / (24 * time.Hour)),
		})
	}
	return out, nil
}

// awaitingPreview is the start of a message on one line: a mail's
// subject, a chat's text, or the first attachment's name when there is
// no text.
func awaitingPreview(item Item) string {
	text := item.Body
	if item.Channel == ChannelMail && strings.TrimSpace(item.Subject) != "" {
		text = item.Subject
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" && len(item.Attachments) > 0 {
		text = item.Attachments[0].Name
	}
	if utf8.RuneCountInString(text) <= AwaitingPreviewLen {
		return text
	}
	return strings.TrimSpace(string([]rune(text)[:AwaitingPreviewLen])) + "…"
}
