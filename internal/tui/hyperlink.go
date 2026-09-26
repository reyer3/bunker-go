package tui

import (
	"regexp"
	"strings"

	"github.com/reyer3/bunker-go/internal/core"
)

// urlPattern matches an http(s) URL. It excludes whitespace and the
// quoting/bracket characters a URL is often wrapped in, so a trailing
// ")" or "." from surrounding prose is not swept into the link.
var urlPattern = regexp.MustCompile(`https?://[^\s<>"')\]}]+`)

// urlTrailingPunctuation is punctuation commonly typed right after a URL
// that is not part of it (end of sentence, closing quote already
// excluded by urlPattern itself).
const urlTrailingPunctuation = ".,;:!?"

// osc8Link wraps text in an OSC 8 hyperlink escape sequence pointing at
// uri. Only ever called with text bunker has already sanitized (a
// detected URL, or a sender address run through safeLine first) — never
// directly on raw message content — so this is the one place in the
// package that is allowed to emit an escape sequence into the rendered
// output; sanitizeTerminalText still strips anything the daemon/message
// content itself contains.
func osc8Link(uri, text string) string {
	return "\x1b]8;;" + uri + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// linkifyURLs wraps every http(s) URL in already-sanitized text with
// bunker's own OSC 8 hyperlink, leaving the visible text (the URL itself)
// unchanged so the rendered line looks identical to a terminal that
// doesn't support OSC 8 — only cell-width-invisible escapes are added.
// Must only ever be called on text that has already been through
// sanitizeTerminalText.
func linkifyURLs(text string) string {
	return urlPattern.ReplaceAllStringFunc(text, func(url string) string {
		trimmed := strings.TrimRight(url, urlTrailingPunctuation)
		trailing := url[len(trimmed):]
		return osc8Link(trimmed, trimmed) + trailing
	})
}

// senderURI returns a clickable URI for a sender address on channel, or
// "" when none is known: mail addresses become "mailto:", a Matrix user
// id becomes a matrix.to link (openable without a Matrix client), and a
// WhatsApp contact (a bare numeric JID) becomes a wa.me "click to chat"
// link — a WhatsApp group JID (letters/digits before "@g.us") has no such
// convention and is left unlinked.
func senderURI(channel core.Channel, addr string) string {
	if addr == "" {
		return ""
	}
	switch channel {
	case core.ChannelMail:
		return "mailto:" + addr
	case core.ChannelMatrix:
		return "https://matrix.to/#/" + addr
	case core.ChannelWhatsApp:
		// Only an individual contact JID ("...@s.whatsapp.net") has a
		// "click to chat" convention; a group JID ("...@g.us") has no
		// such link, so it is left unlinked.
		if local, ok := strings.CutSuffix(addr, "@s.whatsapp.net"); ok && local != "" {
			return "https://wa.me/" + local
		}
	}
	return ""
}

// formatFromLine renders the detail view's "From:" line: the sender's
// name, plus its address in angle brackets when known, linked with OSC 8
// via senderURI when a clickable scheme exists for that channel. Name
// and address are sanitized before anything else touches them, so a
// malicious display name/address can never inject its own escape
// sequence — only the OSC 8 this function itself emits ever reaches the
// terminal.
func formatFromLine(item core.Item) string {
	name := safeLine(item.From.Name)
	addr := safeLine(item.From.ID)
	if addr == "" {
		return name
	}
	display := addr
	if uri := senderURI(item.Channel, addr); uri != "" {
		display = osc8Link(uri, addr)
	}
	if name == "" {
		return display
	}
	return name + " <" + display + ">"
}
