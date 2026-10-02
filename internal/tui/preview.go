package tui

import (
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
)

// One shared source of "what the last message says" for every list (the
// inbox rows, the chat list, the sidebar and Mail's sender rows), so they
// never disagree: the message's text when it has any, else a short label
// for what it carries (a voice note, a photo, a file...), like a
// messaging app's chat list.

// previewSpace squeezes s to one sanitized line: terminal escapes and
// control characters stripped, every run of whitespace (newlines
// included) a single space.
func previewSpace(s string) string {
	return strings.Join(strings.Fields(safeLine(s)), " ")
}

// attachmentLabel is a short label for a message that carries a without
// any text of its own.
func attachmentLabel(a core.Attachment) string {
	mime := strings.ToLower(a.MIME)
	switch {
	case a.Voice:
		label := "🎤 Nota de voz"
		if a.Duration > 0 {
			label += " " + formatVoiceDuration(a.Duration)
		}
		return label
	case a.Name == "sticker":
		return "Sticker"
	case strings.HasPrefix(mime, "image/"):
		return "📷 Foto"
	case strings.HasPrefix(mime, "video/"):
		return "🎥 Video"
	case strings.HasPrefix(mime, "audio/"):
		return "🎵 Audio"
	}
	if name := previewSpace(a.Name); name != "" {
		return "📎 " + name
	}
	return "📎 Archivo"
}

// previewText is what item's last message says: a deleted message and a
// call get their own label, a message with text shows the text, and one
// with only attachments shows the first one's label.
func previewText(item core.Item) string {
	if item.Deleted {
		return "🚫 Mensaje eliminado"
	}
	if item.Meta["wa_call"] != "" {
		return "📞 Llamada"
	}
	if text := previewSpace(item.Body); text != "" {
		return text
	}
	if len(item.Attachments) > 0 {
		return attachmentLabel(item.Attachments[0])
	}
	return ""
}

// previewSender is who said item's last message: "Tú" for our own, else
// the sender's name, "" when no human-facing name is known (a raw id is
// never shown in its place).
func previewSender(item core.Item) string {
	if item.FromMe {
		return "Tú"
	}
	sender := previewSpace(item.From.Name)
	if looksLikeRawIdentifier(sender) {
		return ""
	}
	return sender
}

// joinPreview is "sender: text", or whichever of the two exists.
func joinPreview(sender, text string) string {
	switch {
	case sender == "":
		return text
	case text == "":
		return sender
	}
	return sender + ": " + text
}

// chatPreview is the sidebar's line for a conversation titled title: the
// sender is spelled out ("Ana: ...", "Tú: ...") only when it is not
// already the title, as in a 1:1 chat where the title is the contact.
func chatPreview(item core.Item, title string) string {
	sender := previewSender(item)
	if !item.FromMe && sender == previewSpace(title) {
		sender = ""
	}
	return joinPreview(sender, previewText(item))
}

// senderPreview is a collapsed Mail sender's line: the newest thread's
// subject and, when the body has been fetched, its first words.
func senderPreview(s senderGroup) string {
	if len(s.threads) == 0 || len(s.threads[0].items) == 0 {
		return ""
	}
	item := s.threads[0].newest()
	subject := previewSpace(item.Subject)
	body := previewSpace(item.Body)
	switch {
	case subject == "":
		return body
	case body == "":
		return subject
	}
	return subject + " — " + body
}

// previewFit truncates plain to width cells with an ellipsis; width<=0
// means unbounded.
func previewFit(plain string, width int) string {
	if width <= 0 {
		return plain
	}
	return runewidth.Truncate(plain, width, "…")
}

// previewWrap wraps plain onto at most maxLines lines of width cells,
// breaking at the last space that fits (a word wider than a whole line
// is hard-broken where it starts) and ellipsizing only the last line. A wide rune is never
// split. width<=0 means unbounded: one line, unchanged.
func previewWrap(plain string, width, maxLines int) []string {
	if width <= 0 || maxLines <= 1 {
		return []string{previewFit(plain, width)}
	}
	var lines []string
	rest := plain
	for len(lines) < maxLines-1 && runewidth.StringWidth(rest) > width {
		fit := runewidth.Truncate(rest, width, "")
		line := fit
		// Break at the last space that fits, unless the word it would
		// push down is too long for a line of its own anyway: then it is
		// hard-broken here rather than leaving this line short.
		if !strings.HasPrefix(rest[len(fit):], " ") {
			if i := strings.LastIndexByte(fit, ' '); i > 0 {
				word, _, _ := strings.Cut(rest[i+1:], " ")
				if runewidth.StringWidth(word) <= width {
					line = fit[:i]
				}
			}
		}
		lines = append(lines, strings.TrimRight(line, " "))
		rest = strings.TrimLeft(rest[len(line):], " ")
	}
	return append(lines, previewFit(rest, width))
}
