package mail

import (
	"fmt"
	"strings"
	"time"
)

// ReplySubject prefixes subject with "Re: " unless it already carries a
// reply prefix (checked case-insensitively, and left as-is so we never
// turn "RE:" into a duplicated "Re: RE:").
func ReplySubject(subject string) string {
	trimmed := strings.TrimSpace(subject)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "re:") {
		return trimmed
	}
	if trimmed == "" {
		return "Re:"
	}
	return "Re: " + trimmed
}

// ReplyReferences builds the References header value for a reply: the
// original message's own References chain with its Message-ID appended.
// A blank origMessageID (header missing) is not appended.
func ReplyReferences(origMessageID string, origReferences []string) []string {
	out := make([]string, 0, len(origReferences)+1)
	out = append(out, origReferences...)
	if origMessageID != "" {
		out = append(out, origMessageID)
	}
	return out
}

// QuoteBody renders an optional quoted-reply block: an attribution line
// followed by the original body with each line prefixed by "> ".
func QuoteBody(body, from string, at time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "On %s, %s wrote:", at.UTC().Format("2006-01-02 15:04 MST"), from)
	for _, line := range strings.Split(body, "\n") {
		b.WriteString("\n> ")
		b.WriteString(line)
	}
	return b.String()
}
