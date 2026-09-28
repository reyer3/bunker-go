package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
)

// threadTailLines is the mail thread view's fixed footer hint (a leading
// blank line, then the keymap — wide enough to wrap at a narrow width, so
// it is wrapLines-wrapped like every other fixed block here), always
// visible — never part of the scrollable window.
func threadTailLines(width int) []string {
	return wrapLines([]string{"", hintLine(width, threadHints...)}, width)
}

// threadHeadLines renders the K8 Subject title plus any loading/error
// status lines — always visible, never part of the scrollable window (a
// live ~40-message chat conversation showed this exact class of bug for
// the chat view: the header must never be able to scroll off screen).
func (m Model) threadHeadLines() []string {
	subject := strings.TrimSpace(safeLine(m.threadSubject))
	if subject == "" {
		subject = "(sin asunto)"
	}
	if m.width > 0 {
		subject = runewidth.Truncate(subject, m.width, "…")
	}
	lines := []string{m.styles().title.Render(subject)}
	if m.threadLoading {
		lines = append(lines, "Cargando conversación…")
	}
	if m.threadLoadErr != nil {
		lines = append(lines, "Error: "+humanError(m.threadLoadErr))
	}
	if m.threadSeenErr != nil {
		lines = append(lines, "No se pudo marcar como leído: "+humanError(m.threadSeenErr))
	}
	if m.threadBodyErr != nil {
		lines = append(lines, "No se pudo cargar el mensaje: "+humanError(m.threadBodyErr))
	}
	return wrapLines(lines, m.width)
}

// threadScrollBudget is threadViewLines' body budget alone, for the
// PgUp/PgDown/wheel handlers and resetThreadScrollToSelected in
// update.go, so the scroll math there always agrees with what actually
// renders.
func (m Model) threadScrollBudget() int {
	if m.height <= 0 {
		return chatWindowSentinel
	}
	budget := m.height - len(m.threadHeadLines()) - len(threadTailLines(m.width))
	if budget < 1 {
		budget = 1
	}
	return budget
}

// threadViewLines assembles the K6/K8 mail thread view's fitInbox-style
// layout: the Subject title and status lines, and the footer hint, are
// always visible, fixed lines; between them, a scrollable window of the
// thread's rendered lines is clipped to whatever room is left, so a long
// expanded message body scrolls WITHIN the view (PgUp/PgDown, mouse
// wheel) instead of pushing the Subject off screen. The window defaults
// to (and, on selection/expand change, resets to —
// resetThreadScrollToSelected) the selected message's own starting line.
func (m Model) threadViewLines() []string {
	head := m.threadHeadLines()
	tail := threadTailLines(m.width)
	body, _ := m.threadBodyLinesWithStarts()

	budget := chatWindowSentinel
	if m.height > 0 {
		budget = m.height - len(head) - len(tail)
		if budget < 1 {
			budget = 1
		}
	}
	total := len(body)
	scroll := clampScroll(m.threadScroll, total, budget)
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

// threadBodyLinesWithStarts renders every message's block (collapsed to
// one "sender · date · snippet" line, or expanded to its full
// From/To/Date/body/attachments block) as one flat line slice, oldest
// first; starts[i] is the line index where item i's own block begins —
// used both to window the view here and, in update.go, to reset
// threadScroll so a newly selected/expanded item's start stays visible.
func (m Model) threadBodyLinesWithStarts() (lines []string, starts []int) {
	dim := m.styles().dim
	starts = make([]int, len(m.threadItems))
	for i, item := range m.threadItems {
		var raw []string
		marker := "  "
		if i == m.threadSelected {
			marker = "▶ "
		}
		bodyText := m.threadItemBody(item)
		if m.threadExpanded[i] {
			raw = append(raw,
				fmt.Sprintf("%sDe: %s", marker, formatFromLine(item)),
				"  Para: "+safeLine(joinAddresses(item.To)),
				"  Fecha: "+safeLine(item.Timestamp.Format("02-01-2006 15:04")),
				"",
			)
			body := sanitizeTerminalText(bodyText)
			switch {
			case body != "":
				// linkifyURLs only ever runs on text sanitizeTerminalText
				// has already stripped: its escapes are bunker's own,
				// never anything message content could have injected
				// (see writeDetail's identical ordering).
				body = linkifyURLs(body)
				raw = append(raw, quotedLinesDimmed(body, dim)...)
			case m.threadBodyLoading[item.ID]:
				raw = append(raw, "Cargando…")
			default:
				raw = append(raw, "(empty)")
			}
			for _, attachment := range item.Attachments {
				raw = append(raw, fmt.Sprintf("📎 %s (%d bytes)", safeLine(attachment.Name), attachment.Size))
			}
		} else {
			snippet := threadSnippet(item, bodyText)
			line := fmt.Sprintf("%s%s · %s · %s", marker, safeLine(item.From.Name), relativeTime(item.Timestamp, m.clock()), snippet)
			if m.width > 0 {
				line = runewidth.Truncate(line, m.width, "…")
			}
			raw = append(raw, line)
		}
		// Each item's block is wrapped on its own (not the whole
		// conversation joined together) so starts[i] — the line index
		// this item's block begins at, used to scroll a newly
		// selected/expanded item into view — stays accurate even once a
		// too-wide line (a long, un-truncated body line) has been split
		// into more physical rows than it had logical ones.
		starts[i] = len(lines)
		lines = append(lines, wrapLines(raw, m.width)...)
	}
	return lines, starts
}

// quotedLinesDimmed splits body line by line, dimming any line that
// starts with the "> " quote prefix quoteOriginal's reply/forward body
// uses, so a quoted original reads visually distinct from the user's own
// new text above it. body is already sanitized/linkified before this
// runs.
func quotedLinesDimmed(body string, dim lipgloss.Style) []string {
	raw := strings.Split(body, "\n")
	out := make([]string, len(raw))
	for i, line := range raw {
		if strings.HasPrefix(line, ">") {
			out[i] = dim.Render(line)
		} else {
			out[i] = line
		}
	}
	return out
}

func joinAddresses(addrs []core.Address) string {
	names := make([]string, 0, len(addrs))
	for _, a := range addrs {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = a.ID
		}
		names = append(names, safeLine(name))
	}
	return strings.Join(names, ", ")
}

// writeMailEditor renders K6's full To/Cc/Subject editor, the original's
// attachments (informational only for a forward — re-attaching them
// needs a download that is not implemented yet), and, once Ctrl+S was
// pressed, the dry-run preview/send confirm.
func (m Model) writeMailEditor(out *strings.Builder) {
	titles := map[string]string{"reply": "Responder", "replyAll": "Responder a todos", "forward": "Reenviar", "new": "Nuevo correo"}
	fmt.Fprintf(out, "%s\n\n", titles[m.mailAction])
	fmt.Fprintf(out, "Para: %s\n", m.mailTo.View())
	fmt.Fprintf(out, "Cc: %s\n", m.mailCc.View())
	fmt.Fprintf(out, "Asunto: %s\n\n", m.mailSubject.View())
	out.WriteString(m.composer.View())
	out.WriteString("\n")
	if len(m.mailAttachInfo) > 0 {
		out.WriteString("\nAdjuntos del original (todavía no se reenvían):\n")
		for _, attachment := range m.mailAttachInfo {
			fmt.Fprintf(out, "- %s (%d bytes)\n", safeLine(attachment.Name), attachment.Size)
		}
	}
	if m.mailPreviewing {
		fmt.Fprintf(out, "\nPara: %s\n", safeLine(strings.Join(m.mailPlan.Recipients, ", ")))
		if len(m.mailPlan.Cc) > 0 {
			fmt.Fprintf(out, "Cc: %s\n", safeLine(strings.Join(m.mailPlan.Cc, ", ")))
		}
		if m.mailSending {
			out.WriteString("\nEnviando…\n")
		} else if m.mailSendErr != nil {
			fmt.Fprintf(out, "\nNo se pudo enviar: %s\n", humanError(m.mailSendErr))
		}
		out.WriteString("\n" + hintLine(m.width, confirmSendHints[:2]...) + "\n")
		return
	}
	if m.mailSendErr != nil {
		fmt.Fprintf(out, "\nError: %s\n", humanError(m.mailSendErr))
	}
	out.WriteString("\n" + hintLine(m.width, mailEditorHints...) + "\n")
}
