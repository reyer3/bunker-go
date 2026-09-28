package tui

import (
	"fmt"
	"strings"

	"time"

	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
)

// spanishMonths are the "dd-mmm" abbreviations relativeTime uses for
// items older than yesterday; the user reads bunker in Spanish.
var spanishMonths = [...]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"}

// relativeTime formats at relative to now the way the inbox row's
// right-aligned timestamp does: "15:04" for today, "ayer" for yesterday,
// else "dd-mmm".
func relativeTime(at, now time.Time) string {
	// Stored timestamps are UTC; show and bucket them in now's location
	// (time.Local in the running TUI).
	at = at.In(now.Location())
	if sameDay(at, now) {
		return at.Format("15:04")
	}
	if sameDay(at, now.AddDate(0, 0, -1)) {
		return "ayer"
	}
	return fmt.Sprintf("%02d-%s", at.Day(), spanishMonths[at.Month()-1])
}

// dayLabel formats the chat view's day separator ("hoy"/"ayer"/"dd-mmm"),
// the same day-bucketing relativeTime uses but with "hoy" instead of a
// clock time for today (a separator marks a whole day, not one moment).
func dayLabel(at, now time.Time) string {
	at = at.In(now.Location())
	if sameDay(at, now) {
		return "hoy"
	}
	if sameDay(at, now.AddDate(0, 0, -1)) {
		return "ayer"
	}
	return fmt.Sprintf("%02d-%s", at.Day(), spanishMonths[at.Month()-1])
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// looksLikeRawIdentifier reports whether s is a raw protocol identifier
// that must never stand in as a human-facing title: a Matrix room id
// ("!abc:server.org") or a bare numeric JID (WhatsApp's
// "120363...@g.us" group id or "34600112233@s.whatsapp.net" contact,
// shown before a display name has resolved).
func looksLikeRawIdentifier(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "!") && strings.Contains(s, ":") {
		return true
	}
	local := s
	if i := strings.IndexByte(s, '@'); i >= 0 {
		local = s[:i]
	}
	if local == "" {
		return false
	}
	for _, r := range local {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// shortenIdentifier trims a raw identifier's protocol noise (the "!"
// sigil, ":server" suffix, "@domain" suffix) and caps its width, for
// display only when every human-facing candidate was rejected. The
// caller is responsible for dimming it.
func shortenIdentifier(s string) string {
	s = strings.TrimPrefix(s, "!")
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "(sin nombre)"
	}
	return runewidth.Truncate(s, 12, "…")
}

// rowTitle resolves a conversation's line-1 title: ThreadName, then
// Subject, then the sender's name — skipping any candidate that is a raw
// protocol identifier (a Matrix room with no name, a WhatsApp group
// whose subject hasn't synced) — falling back to a shortened, dimmed
// identifier when nothing human-facing is available at all.
func rowTitle(item core.Item) (text string, dimmed bool) {
	for _, candidate := range []string{item.ThreadName, item.Subject, item.From.Name} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || looksLikeRawIdentifier(candidate) {
			continue
		}
		return candidate, false
	}
	for _, candidate := range []string{item.ThreadName, item.Thread, item.From.ID, item.From.Name, item.ID} {
		if candidate != "" {
			return shortenIdentifier(candidate), true
		}
	}
	return "(sin título)", true
}

// previewLine is line 2: a single-line, sanitized, truncated "Sender:
// body" preview of a group's newest item. The sender prefix is omitted
// (not replaced by a raw id) when no human-facing sender name is known.
func previewLine(item core.Item, width int) string {
	body := safeLine(item.Body)
	sender := strings.TrimSpace(item.From.Name)
	text := body
	if sender != "" && !looksLikeRawIdentifier(sender) {
		text = safeLine(sender)
		if body != "" {
			text += ": " + body
		}
	}
	if width > 0 {
		text = runewidth.Truncate(text, width, "…")
	}
	return text
}
