package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/reyer3/bunker-go/internal/core"
)

// Links in the chat view: bubbles show their http(s) URLs as OSC 8
// hyperlinks (a click opens them in terminals that support it), and
// Alt+L opens the selected message's link (else the newest message's,
// like Alt+Y) with the desktop opener — straight away when it has one,
// through a small numbered picker when it has several.

const (
	// chatLinkKey opens a message's link; Alt+L is free (see the list of
	// taken Alt keys in chat_ux.go).
	chatLinkKey = "alt+l"
	// chatLinkPickerMax bounds the picker to what the digits 1-9 reach.
	chatLinkPickerMax = 9
)

// linkOpenedMsg is the opener's answer to an Alt+L.
type linkOpenedMsg struct{ err error }

// urlSpan is one URL found in a text: its byte range there (trailing
// punctuation excluded, as linkifyURLs does) and the URL itself.
type urlSpan struct {
	start, end int
	url        string
}

// urlSpans finds the http(s) URLs in text, already sanitized.
func urlSpans(text string) []urlSpan {
	var spans []urlSpan
	for _, loc := range urlPattern.FindAllStringIndex(text, -1) {
		url := strings.TrimRight(text[loc[0]:loc[1]], urlTrailingPunctuation)
		if url == "" {
			continue
		}
		spans = append(spans, urlSpan{start: loc[0], end: loc[0] + len(url), url: url})
	}
	return spans
}

// messageURLs lists item's links in order, each once; a deleted message
// has none (its old body is not shown).
func messageURLs(item core.Item) []string {
	if item.Deleted {
		return nil
	}
	var urls []string
	seen := map[string]bool{}
	for _, s := range urlSpans(sanitizeTerminalText(item.Body)) {
		if !seen[s.url] {
			seen[s.url] = true
			urls = append(urls, s.url)
		}
	}
	return urls
}

// wrappedLinks maps text's URLs onto the lines wrapWords made of it: for
// each line, the parts of it that belong to a URL, as byte ranges in that
// line, each pointing at the WHOLE URL — so a long URL broken across
// lines is one link on every line it spans, never a truncated one.
// wrapWords only drops spaces at its break points, which is how a line is
// found in text; a line that cannot be found gets no links (plain text
// is the safe fallback).
func wrappedLinks(text string, wrapped []string) [][]urlSpan {
	out := make([][]urlSpan, len(wrapped))
	spans := urlSpans(text)
	if len(spans) == 0 {
		return out
	}
	pos := 0
	for i, line := range wrapped {
		for pos < len(text) && text[pos] == ' ' && !strings.HasPrefix(text[pos:], line) {
			pos++
		}
		if !strings.HasPrefix(text[pos:], line) {
			return out
		}
		start, end := pos, pos+len(line)
		for _, s := range spans {
			lo, hi := max(s.start, start), min(s.end, end)
			if lo < hi {
				out[i] = append(out[i], urlSpan{start: lo - start, end: hi - start, url: s.url})
			}
		}
		pos = end
	}
	return out
}

// renderLinked styles text like st.Render does, wrapping each link range
// in bunker's own OSC 8 escape. The pieces are styled one by one so the
// escape never passes through lipgloss, and the visible text (and its
// width) is exactly what st.Render(text) shows.
func renderLinked(st lipgloss.Style, text string, links []urlSpan) string {
	if len(links) == 0 {
		return st.Render(text)
	}
	var b strings.Builder
	pos := 0
	for _, l := range links {
		if l.start > pos {
			b.WriteString(st.Render(text[pos:l.start]))
		}
		b.WriteString(osc8Link(l.url, st.Render(text[l.start:l.end])))
		pos = l.end
	}
	if pos < len(text) {
		b.WriteString(st.Render(text[pos:]))
	}
	return b.String()
}

// openChatLink is Alt+L.
func (m Model) openChatLink() (Model, tea.Cmd) {
	item, ok := m.chatCopyTarget()
	if !ok {
		return m.noteUIError(errors.New("no hay mensajes")), nil
	}
	urls := messageURLs(item)
	switch len(urls) {
	case 0:
		return m.withFlash("El mensaje no tiene enlaces"), nil
	case 1:
		return m.openLink(urls[0])
	}
	if len(urls) > chatLinkPickerMax {
		urls = urls[:chatLinkPickerMax]
	}
	m.chatLinks, m.chatLinkSel = urls, 0
	return m, nil
}

// openLink runs the opener (the injected one in tests) off the UI
// goroutine; its failure comes back as a linkOpenedMsg.
func (m Model) openLink(url string) (Model, tea.Cmd) {
	m.chatLinks, m.chatLinkSel = nil, 0
	m = m.withFlash("Abriendo enlace…")
	open := m.openMeetingURL
	return m, func() tea.Msg { return linkOpenedMsg{err: open(url)} }
}

func (m Model) handleLinkOpened(msg linkOpenedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.withFlash("No se pudo abrir el enlace: " + humanError(msg.err)), nil
	}
	return m, nil
}

// updateChatLinks handles keys while the link picker is on screen: a
// digit or ↵ opens a link, ↑/↓ move, Esc closes it. Any other key is
// ignored so it cannot land in the draft behind the picker.
func (m Model) updateChatLinks(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc":
		m.chatLinks, m.chatLinkSel = nil, 0
		return m, nil
	case "up":
		if m.chatLinkSel > 0 {
			m.chatLinkSel--
		}
		return m, nil
	case "down":
		if m.chatLinkSel < len(m.chatLinks)-1 {
			m.chatLinkSel++
		}
		return m, nil
	case "enter":
		return m.openLink(m.chatLinks[m.chatLinkSel])
	}
	if len(key) == 1 && key[0] >= '1' && int(key[0]-'0') <= len(m.chatLinks) {
		return m.openLink(m.chatLinks[key[0]-'1'])
	}
	return m, nil
}

// chatLinkPickerLines are the picker's tail lines: a prompt, then one
// numbered line per link, the highlighted one marked.
func (m Model) chatLinkPickerLines() []string {
	if m.chatLinks == nil {
		return nil
	}
	lines := []string{fmt.Sprintf("Abrir enlace: 1-%d o ↑/↓ y ↵ · Esc cancelar", len(m.chatLinks))}
	for i, url := range m.chatLinks {
		mark := "  "
		if i == m.chatLinkSel {
			mark = "› "
		}
		line := fmt.Sprintf("%s%d %s", mark, i+1, url)
		if m.width > 0 {
			line = runewidth.Truncate(line, m.width, "…")
		}
		lines = append(lines, line)
	}
	return lines
}
