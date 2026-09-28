package tui

import (
	"fmt"
	"strings"

	"github.com/reyer3/bunker-go/internal/core"
)

// detailTailLines is the plain single-item detail view's fixed footer
// hint (a leading blank line, then the keymap), always visible — never
// part of the scrollable window. wrapLines-wrapped like every other
// fixed block in the chat/thread views, so a narrow terminal wrapping it
// onto two physical rows is already accounted for in the height budget.
func detailTailLines(width int) []string {
	return wrapLines([]string{"", hintLine(width, detailHints...)}, width)
}

// detailHeadLines renders the always-visible Subject/From/Channel block
// above the scrollable body — never part of the scrollable window, the
// same guarantee the chat/thread views give their own header.
func (m Model) detailHeadLines(item core.Item) []string {
	lines := []string{
		fmt.Sprintf("Asunto: %s", safeLine(item.Subject)),
		fmt.Sprintf("De: %s", formatFromLine(item)),
		fmt.Sprintf("Cuenta: %s/%s", safeLine(string(item.Channel)), safeLine(item.Account)),
	}
	return wrapLines(lines, m.width)
}

// detailBodyLines renders the scrollable part of the plain detail view:
// the body text (sanitized and linkified, same ordering as before this
// task) followed by the attachments list.
func (m Model) detailBodyLines(item core.Item) []string {
	raw := []string{"", "Mensaje:"}
	body := sanitizeTerminalText(item.Body)
	if body == "" {
		raw = append(raw, "(vacío)")
	} else {
		// linkifyURLs only ever runs on text sanitizeTerminalText has
		// already stripped: the escapes it adds are bunker's own, never
		// anything message content could have injected.
		raw = append(raw, strings.Split(linkifyURLs(body), "\n")...)
	}
	raw = append(raw, "", "Attachments:")
	if len(item.Attachments) == 0 {
		raw = append(raw, "(none)")
	}
	for _, attachment := range item.Attachments {
		raw = append(raw, fmt.Sprintf("- %s (%s, %d bytes)", safeLine(attachment.Name), safeLine(attachment.MIME), attachment.Size))
	}
	return wrapLines(raw, m.width)
}

// detailScrollBudget is detailViewLines' body budget alone, for the
// j/k/PgUp/PgDown/"G"/wheel handlers in update.go/mouse.go, so the scroll
// math there always agrees with what actually renders.
func (m Model) detailScrollBudget() int {
	if m.height <= 0 {
		return chatWindowSentinel
	}
	budget := m.height - len(m.detailHeadLines(m.readItem)) - len(detailTailLines(m.width))
	if budget < 1 {
		budget = 1
	}
	return budget
}

// detailViewLines assembles the plain single-item detail view's
// fitInbox-style layout: the Subject/From/Channel header and the footer
// hint are always visible, fixed lines; between them, a scrollable
// window of the body's rendered lines is clipped to whatever room is
// left, so a long body scrolls WITHIN the view (j/k, arrows, PgUp/
// PgDown, "G", mouse wheel) instead of pushing the header or footer off
// screen — the same fix the chat/thread views already have.
func (m Model) detailViewLines() []string {
	if m.reading {
		return append([]string{"Cargando…"}, detailTailLines(m.width)...)
	}
	if m.readErr != nil {
		return append([]string{fmt.Sprintf("No se pudo abrir: %s", humanError(m.readErr))}, detailTailLines(m.width)...)
	}

	item := m.readItem
	head := m.detailHeadLines(item)
	tail := detailTailLines(m.width)
	body := m.detailBodyLines(item)

	budget := chatWindowSentinel
	if m.height > 0 {
		budget = m.height - len(head) - len(tail)
		if budget < 1 {
			budget = 1
		}
	}
	total := len(body)
	scroll := clampScroll(m.detailScroll, total, budget)
	end := scroll + budget
	if end > total {
		end = total
	}
	window := body[scroll:end]

	lines := make([]string, 0, len(head)+len(window)+len(tail))
	lines = append(lines, head...)
	lines = append(lines, window...)
	lines = append(lines, tail...)
	return lines
}
