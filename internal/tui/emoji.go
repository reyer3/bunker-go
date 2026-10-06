package tui

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

// Emoji shortcode completion (issue #7): typing ":" plus at least
// emojiMinQuery letters at the end of the chat draft lists matching
// emoji above the composer; Tab/Shift+Tab pick one, Enter inserts it and
// Esc dismisses the list. It only ever edits the draft, never sends.

const (
	emojiMinQuery   = 2
	emojiMaxMatches = 6
)

// emojiQuery returns the shortcode being typed at the end of text: the
// letters after a ":" that starts the text or follows whitespace. A colon
// inside a word or number ("12:30", "http://") never starts one.
func emojiQuery(text string) (string, bool) {
	at := strings.LastIndexByte(text, ':')
	if at < 0 {
		return "", false
	}
	if at > 0 {
		prev := []rune(text[:at])
		if !unicode.IsSpace(prev[len(prev)-1]) {
			return "", false
		}
	}
	query := text[at+1:]
	if len([]rune(query)) < emojiMinQuery {
		return "", false
	}
	for _, r := range query {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return "", false
		}
	}
	return query, true
}

var accentFold = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n")

func normalizeEmojiQuery(q string) string {
	return accentFold.Replace(strings.ToLower(q))
}

// matchEmoji ranks emojiTable against query: shortcode or keyword
// prefixes first, then substrings, each in table order.
func matchEmoji(query string) []emojiEntry {
	q := normalizeEmojiQuery(query)
	var prefix, contains []emojiEntry
	for _, e := range emojiTable {
		words := append([]string{e.code}, strings.Fields(accentFold.Replace(e.keywords))...)
		matched := false
		for _, w := range words {
			if strings.HasPrefix(w, q) {
				prefix = append(prefix, e)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		for _, w := range words {
			if strings.Contains(w, q) {
				contains = append(contains, e)
				break
			}
		}
	}
	out := append(prefix, contains...)
	if len(out) > emojiMaxMatches {
		out = out[:emojiMaxMatches]
	}
	return out
}

// composerCursorAtEnd reports whether the draft's cursor sits after its
// last character: completion only looks at the draft's tail, so it stays
// off while the user edits earlier text.
func (m Model) composerCursorAtEnd() bool {
	if m.composer.Line() != m.composer.LineCount()-1 {
		return false
	}
	end := m.composer
	end.CursorEnd()
	return end.LineInfo() == m.composer.LineInfo()
}

// emojiMatches returns the completion list for the current chat draft,
// or nil when no completion is active.
func (m Model) emojiMatches() []emojiEntry {
	if !m.chatMode || m.chatConfirm || m.chatPreviewPending {
		return nil
	}
	value := m.composer.Value()
	if value == m.emojiDismissed || !m.composerCursorAtEnd() {
		return nil
	}
	query, ok := emojiQuery(value)
	if !ok {
		return nil
	}
	return matchEmoji(query)
}

// updateEmojiCompletion handles the completion keys while a list is
// shown, reporting whether it consumed msg.
func (m Model) updateEmojiCompletion(msg tea.KeyMsg) (Model, bool) {
	matches := m.emojiMatches()
	if len(matches) == 0 {
		return m, false
	}
	sel := m.emojiSel
	if sel >= len(matches) {
		sel = 0
	}
	switch msg.String() {
	case "tab":
		m.emojiSel = (sel + 1) % len(matches)
		return m, true
	case "shift+tab":
		m.emojiSel = (sel + len(matches) - 1) % len(matches)
		return m, true
	case "esc":
		m.emojiDismissed = m.composer.Value()
		m.emojiSel = 0
		return m, true
	case "enter":
		value := m.composer.Value()
		query, _ := emojiQuery(value)
		value = strings.TrimSuffix(value, ":"+query) + matches[sel].emoji + " "
		m.composer.SetValue(value)
		m.composer.CursorEnd()
		m.emojiSel = 0
		return m.resizeChatComposer(), true
	}
	return m, false
}

// emojiCompletionLine renders the completion list as one line: each
// match as "emoji :code", the selected one highlighted.
func (m Model) emojiCompletionLine() (string, bool) {
	matches := m.emojiMatches()
	if len(matches) == 0 {
		return "", false
	}
	sel := m.emojiSel
	if sel >= len(matches) {
		sel = 0
	}
	selStyle := m.renderer().NewStyle().Reverse(true)
	parts := make([]string, len(matches))
	width := 0
	for i, e := range matches {
		text := e.emoji + " :" + e.code
		if width+runewidth.StringWidth(text)+2 > m.width && i > 0 {
			parts = parts[:i]
			break
		}
		width += runewidth.StringWidth(text) + 2
		if i == sel {
			text = selStyle.Render(text)
		}
		parts[i] = text
	}
	return strings.Join(parts, "  "), true
}
