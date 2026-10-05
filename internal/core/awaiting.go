package core

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
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
// most maxAwaitingRows. Groups are left out unless Groups is set. Mail
// threads count only when the user's last mail asks a question
// (AsksQuestion) unless Mail is set: most mail the user sends informs
// (an invoice, "te envié el archivo") and expects no answer.
type AwaitingFilter struct {
	Days    int     `json:"days,omitempty"`
	Channel Channel `json:"channel,omitempty"`
	Account string  `json:"account,omitempty"`
	Groups  bool    `json:"groups,omitempty"`
	Mail    bool    `json:"mail,omitempty"`
	Limit   int     `json:"limit,omitempty"`
}

// AwaitingQuery is what Service.AwaitingReply asks the store: the
// conversations whose newest item is the user's, not deleted, and older
// than Before. Mail keeps every mail thread, not only those whose last
// mail asks a question.
type AwaitingQuery struct {
	Before  time.Time
	Channel Channel
	Account string
	Groups  bool
	Mail    bool
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
	// left out unless q.Groups, and mail threads whose last mail asks
	// nothing (AsksQuestion) unless q.Mail. Those rules run before the
	// limit, so it counts listed rows only. A ThreadName that is not
	// usable (UsableThreadName) falls back to the thread's newest usable
	// one, when there is one.
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
		Account: filter.Account, Groups: filter.Groups, Mail: filter.Mail, Limit: limit,
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

// UsableThreadName reports whether name says who a chat is with: not
// empty, not the thread id itself, and not just a phone number (digits
// with optional +, spaces and dashes). WhatsApp stores the bare number
// as the chat name when it knows no contact name, as a message sent from
// the phone often does, while older messages of the same chat carry the
// person's name.
func UsableThreadName(thread, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == thread {
		return false
	}
	return strings.ContainsFunc(name, func(r rune) bool {
		return !unicode.IsDigit(r) && r != '+' && r != ' ' && r != '-'
	})
}

// AsksQuestion reports whether a mail body asks something in the
// sender's own text: a '?' or '¿' outside quoted history and links. It
// skips lines quoted with '>' and stops at the signature ("-- ") and at
// the start of the quoted original: an attribution line ("El ...
// escribió:", "On ... wrote:", also wrapped over two lines), an
// "-----Original Message-----" or "-----Mensaje original-----"
// separator, an Outlook rule of underscores, or a "De:"/"From:" header
// block (followed by Enviado:, Para:, Sent:, To: and the like).
func AsksQuestion(body string) bool {
	for _, line := range ownLines(body) {
		for _, word := range strings.Fields(line) {
			if strings.Contains(word, "://") || strings.HasPrefix(strings.ToLower(word), "www.") {
				continue
			}
			if strings.ContainsAny(word, "?¿") {
				return true
			}
		}
	}
	return false
}

// replyHeaderKeys start the lines of a quoted mail's header block, after
// its "De:"/"From:" line.
var replyHeaderKeys = []string{"enviado:", "fecha:", "para:", "asunto:", "cc:", "sent:", "date:", "to:", "subject:"}

// ownLines is a mail body's lines written by its sender: those before the
// quoted history or the signature, without '>' quoted lines.
func ownLines(body string) []string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var out []string
	for i, raw := range lines {
		line := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case strings.HasPrefix(line, ">"):
			continue
		case strings.TrimRight(raw, "\r") == "-- " || line == "--",
			strings.HasPrefix(line, "-----") && (strings.Contains(line, "original message") || strings.Contains(line, "mensaje original")),
			strings.HasPrefix(line, "_____"),
			(strings.HasPrefix(line, "de:") || strings.HasPrefix(line, "from:")) && nextIsReplyHeader(lines[i+1:]):
			return out
		case strings.HasSuffix(line, "wrote:") || strings.HasSuffix(line, "escribió:") || strings.HasSuffix(line, "escribio:"):
			// A wrapped attribution starts on the line before.
			if !isAttributionStart(line) && len(out) > 0 && isAttributionStart(strings.ToLower(strings.TrimSpace(out[len(out)-1]))) {
				out = out[:len(out)-1]
			}
			return out
		}
		out = append(out, raw)
	}
	return out
}

func isAttributionStart(line string) bool {
	return strings.HasPrefix(line, "el ") || strings.HasPrefix(line, "on ")
}

// nextIsReplyHeader reports whether the first non-blank of lines is a
// quoted mail's header (Enviado:, Para:, Sent:, To:...).
func nextIsReplyHeader(lines []string) bool {
	for _, raw := range lines {
		line := strings.ToLower(strings.TrimSpace(raw))
		if line == "" {
			continue
		}
		for _, key := range replyHeaderKeys {
			if strings.HasPrefix(line, key) {
				return true
			}
		}
		return false
	}
	return false
}
