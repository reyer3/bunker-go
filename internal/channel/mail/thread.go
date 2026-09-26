// Package mail is the mail channel adapter for bunker-go: IMAP (go-imap
// v2) for receive/organize and SMTP (go-smtp) for send, built against the
// core.Adapter/Fetcher/Sender/Organizer ports.
package mail

import "strings"

// ThreadID picks the stable id a conversation is grouped under. RFC 5322
// References carries the whole ancestor chain, oldest first, so its first
// entry is the thread root; falling back to In-Reply-To then to the
// message's own id keeps every message addressable even when the other
// headers are missing.
func ThreadID(messageID, inReplyTo string, references []string) string {
	if len(references) > 0 && references[0] != "" {
		return references[0]
	}
	if inReplyTo != "" {
		return inReplyTo
	}
	return messageID
}

// replyPrefixes are the subject prefixes ThreadName strips, repeatedly,
// so "RE: Fwd: Re: X" reduces to "X". Compared case-insensitively.
var replyPrefixes = []string{"re:", "fwd:", "fw:"}

// ThreadName derives a human display name for a thread from a message
// subject, stripping repeated reply/forward prefixes and surrounding
// whitespace.
func ThreadName(subject string) string {
	s := strings.TrimSpace(subject)
	for {
		trimmed := false
		for _, prefix := range replyPrefixes {
			if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
				s = strings.TrimSpace(s[len(prefix):])
				trimmed = true
			}
		}
		if !trimmed {
			break
		}
	}
	return s
}
