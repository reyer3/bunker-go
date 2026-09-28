package tui

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/style"
)

// K7's bubble palette (conversation-view.md's Usability pass): a dark
// neutral background for incoming bubbles, WhatsApp's own real dark-green
// bubble color for own WhatsApp messages, and Matrix's brand teal for own
// Matrix messages (Element/Matrix clients color their own messages in
// the room's accent, unlike WhatsApp's fixed green).
const (
	bubbleIncomingBg    = "#262626"
	bubbleIncomingFg    = "#e6e6e6"
	bubbleOwnWhatsAppBg = "#005c4b"
	bubbleOwnFg         = "#f5f5f5"
	dayPillBg           = "#333333"
	dayPillFg           = "#cfcfcf"
	// bubbleOptimisticBg/Fg render K10's optimistic own bubble: a dim
	// gray distinct from both the incoming and the own brand-colored
	// bubble, so a message still in flight (or that failed to send)
	// visually reads as "not confirmed yet" rather than as a normal sent
	// message.
	bubbleOptimisticBg = "#3a3a3a"
	bubbleOptimisticFg = "#a3a09e"
)

// senderPalette colors group-chat sender names stably (hashed by name, so
// the same sender always gets the same color across renders) without
// reusing the brand/bubble colors above.
var senderPalette = []string{"#e06c75", "#61afef", "#e5c07b", "#c678dd", "#56b6c2", "#98c379", "#d19a66"}

// senderColorHex picks a stable color for name via a simple string hash:
// the same sender always renders in the same color, without any shared
// state to track "which sender got which color first".
func senderColorHex(name string) string {
	if name == "" {
		return style.ColorDim
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return senderPalette[h.Sum32()%uint32(len(senderPalette))]
}

// ownBubbleBg picks the own-message bubble background for channel.
func ownBubbleBg(channel core.Channel) string {
	if channel == core.ChannelMatrix {
		return style.ColorMatrix
	}
	return bubbleOwnWhatsAppBg
}

// chatBubbleWidth caps a bubble's own text width at ~75% of the terminal
// width (conversation-view.md), so it reads as a chat bubble rather than
// plain text spanning the whole terminal; it degrades to the full width
// at narrow terminals where a 75% bubble would leave barely any room.
func chatBubbleWidth(width int) int {
	if width <= 0 {
		return 40
	}
	if width <= 20 {
		return width
	}
	bw := width * 3 / 4
	if bw < 1 {
		bw = 1
	}
	return bw
}

// chatComposerWidth is the docked composer's own content width: the
// terminal width minus the 4 cells composerBox's rounded border (2) and
// horizontal padding (2, one space on each side so typed text never
// touches the border) consume, so the bordered box's total footprint
// never exceeds the terminal.
func chatComposerWidth(width int) int {
	if width > 4 {
		return width - 4
	}
	return width
}

// messageTime formats one message's bubble timestamp ("HH:MM"), shown in
// now's location (time.Local in the running TUI; a fixed test clock in
// tests) the same way relativeTime/dayLabel bucket by now's location.
func messageTime(at, now time.Time) string {
	return at.In(now.Location()).Format("15:04")
}

// padLeft left-pads text with spaces so it ends flush with width's right
// edge — used for the bubble's bottom-right time line.
func padLeft(text string, width int) string {
	pad := width - runewidth.StringWidth(text)
	if pad <= 0 {
		return text
	}
	return strings.Repeat(" ", pad) + text
}

// chatWindowSentinel stands in for "no height limit" (an unknown terminal
// size, tea.WindowSizeMsg not received yet): every body line renders,
// exactly like rows.go's overviewLines does for the plain inbox.
const chatWindowSentinel = 1 << 20

// chatViewLines assembles the K5/K7 chat view's fitInbox-style layout:
// the header (contact/group name + presence + rule) and the docked
// composer + confirm/footer are always visible, fixed lines; between
// them, a scrollable window of the conversation's rendered lines is
// clipped to whatever room is left so the WHOLE view never exceeds
// m.height — the bug a live ~40-message conversation exposed (the header
// and day pill scrolled off the top of a real pane). The window is
// bottom-aligned to the newest message by default (m.chatScroll == 0) and
// scrolls up via PgUp/PgDown (a page) or the mouse wheel (a few lines),
// clamped so it can never show past the newest or older than the oldest
// loaded message; reaching the oldest loaded message triggers
// loadOlderChat (see updateChat/updateMouse).
func (m Model) chatViewLines() []string {
	header := m.chatHeaderLines()
	tail := m.chatTailLines()
	body := m.chatBodyLines()

	budget := chatWindowSentinel
	if m.height > 0 {
		budget = m.height - len(header) - len(tail)
		if budget < 1 {
			budget = 1
		}
	}
	window, _ := windowTail(body, m.chatScroll, budget)

	lines := make([]string, 0, len(header)+len(window)+len(tail))
	lines = append(lines, header...)
	lines = append(lines, window...)
	lines = append(lines, tail...)
	return lines
}

// chatScrollBudget is chatViewLines' body budget alone (without building
// the header/tail/body slices), for the PgUp/PgDown/wheel key handlers in
// update.go/mouse.go, so the scroll math there always agrees with what
// actually rendered.
func (m Model) chatScrollBudget() int {
	if m.height <= 0 {
		return chatWindowSentinel
	}
	budget := m.height - len(m.chatHeaderLines()) - len(m.chatTailLines())
	if budget < 1 {
		budget = 1
	}
	return budget
}

// wrapLines applies wrapView to each line independently (every line here
// is already a single, standalone terminal row with no embedded "\n"),
// flattening any line wider than width into the multiple physical rows
// it will actually occupy. Without this, a fixed line that happens to be
// wider than the terminal (the mail thread's footer keymap hint,
// unrelated to any per-item truncation) would count as one line for
// height budgeting but render as two once wrapView split it downstream —
// exactly the off-by-one that let the view grow past the pane height
// again after the per-message content was already correctly bounded.
func wrapLines(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		wrapped := wrapView(line, width)
		out = append(out, strings.Split(wrapped, "\n")...)
	}
	return out
}

// clampScroll bounds scroll to [0, total-budget] (never negative, never
// past the point where a full budget-sized window is still shown) —
// shared by the chat view's bottom-anchored window and the mail thread
// view's selection-anchored one.
func clampScroll(scroll, total, budget int) int {
	maxScroll := total - budget
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll < 0 {
		scroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	return scroll
}

// windowTail returns the tail window of lines ending scroll lines up from
// the very end, sized to at most budget lines, plus the scroll value
// actually used after clampScroll. scroll is measured from the bottom so
// prepending older lines at the front (an older page load) never shifts
// what is already on screen — both the total length and the desired end
// index grow together.
func windowTail(lines []string, scroll, budget int) ([]string, int) {
	total := len(lines)
	scroll = clampScroll(scroll, total, budget)
	end := total - scroll
	start := end - budget
	if start < 0 {
		start = 0
	}
	if start > end {
		start = end
	}
	return lines[start:end], scroll
}

// chatHeaderLines renders the K7 header: the channel glyph + contact/
// group name in bold, an optional dim presence line, and a dim rule —
// always shown, never part of the scrollable window (the exact guarantee
// the live bug broke).
func (m Model) chatHeaderLines() []string {
	r := m.renderer()
	glyph := m.resolvedGlyphs()[m.chatChannel]
	name := strings.TrimSpace(safeLine(m.chatName))
	if name == "" {
		name = "Chat"
	}
	if m.width > 0 {
		budget := m.width - runewidth.StringWidth(glyph) - 1
		if budget < 1 {
			budget = 1
		}
		name = runewidth.Truncate(name, budget, "…")
	}
	glyphStyle := r.NewStyle().Foreground(lipgloss.Color(style.ChannelColors[m.chatChannel]))
	nameStyle := r.NewStyle().Bold(true)
	lines := []string{glyphStyle.Render(glyph) + " " + nameStyle.Render(name)}

	if presence := presenceHeaderText(m.chatPresence, m.clock()); presence != "" {
		if m.width > 0 {
			presence = runewidth.Truncate(presence, m.width, "…")
		}
		lines = append(lines, r.NewStyle().Foreground(lipgloss.Color(style.ColorDim)).Render(presence))
	}
	lines = append(lines, r.NewStyle().Foreground(lipgloss.Color(style.ColorDim)).Render(strings.Repeat("─", widthOrDefault(m.width))))
	return wrapLines(lines, m.width)
}

// chatBodyLines renders the WHOLE conversation (day pills + bubbles) as
// one flat line slice, oldest first — chatViewLines then windows it. A
// load error, if any, is its own leading line (also scrollable: it is
// rare and stays in context with the messages around it, unlike the
// always-visible header/tail).
func (m Model) chatBodyLines() []string {
	var lines []string
	if m.chatLoadErr != nil {
		lines = append(lines, "Error: "+safeLine(m.chatLoadErr.Error()))
	}
	r := m.renderer()
	// K10: the optimistic own bubble, if any, renders as one more item
	// appended after the loaded conversation — it goes through the exact
	// same day-pill/showName logic as a real item, so it never doubles up
	// on a "hoy" pill or breaks a same-sender grouping run. Its own index
	// (optIdx) is the only thing telling chatBubbleLines to render it dim
	// with a status line instead of a real "HH:MM" time.
	items := m.chatItems
	optIdx := -1
	if m.chatOptimistic != nil {
		synthetic := core.Item{
			Channel:   m.chatChannel,
			FromMe:    true,
			Body:      m.chatOptimistic.body,
			Timestamp: m.chatOptimistic.at,
		}
		items = append(append([]core.Item(nil), items...), synthetic)
		optIdx = len(items) - 1
	}
	var lastDay time.Time
	now := m.clock()
	for i, item := range items {
		if i == 0 || !sameDay(item.Timestamp, lastDay) {
			lines = append(lines, m.dayPillLine(dayLabel(item.Timestamp, now)))
			lastDay = item.Timestamp
		}
		showName := !item.FromMe && (i == 0 || items[i-1].From.Name != item.From.Name || items[i-1].FromMe)
		dim := i == optIdx
		status := ""
		if dim {
			status = m.chatOptimisticStatusText()
		}
		lines = append(lines, chatBubbleLines(r, item, m.width, showName, now, dim, status)...)
	}
	return wrapLines(lines, m.width)
}

// chatOptimisticStatusText is the K10 optimistic bubble's bottom-right
// status line, replacing the normal "HH:MM" time: "enviando…" while the
// real send is still in flight or has just succeeded but not yet been
// confirmed by the reload, "no enviado" once it has failed, matching
// conversation-view.md's K10 requirements exactly.
func (m Model) chatOptimisticStatusText() string {
	opt := m.chatOptimistic
	if opt == nil {
		return ""
	}
	if opt.failed {
		return "no enviado"
	}
	return "enviando…"
}

// chatItemsContainID reports whether items contains one with the given
// id. An empty id never matches anything (K10's optimistic bubble has no
// id at all until the real send returns a receipt), so it is never
// mistaken for "already deduped".
func chatItemsContainID(items []core.Item, id string) bool {
	if id == "" {
		return false
	}
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}

// chatTailLines renders the docked composer box plus whatever follows it
// (the inline send confirm/sending/error line, then the footer hint) —
// always visible, never part of the scrollable window.
func (m Model) chatTailLines() []string {
	lines := []string{""} // the blank line separating the body from the composer
	lines = append(lines, strings.Split(m.composerBox(), "\n")...)
	switch {
	case m.chatPreviewPending:
		lines = append(lines, "Preparando…")
	case m.chatConfirm:
		lines = append(lines, fmt.Sprintf("¿Enviar a %s? ↵ enviar · Esc cancelar", safeLine(strings.Join(m.chatPlan.Recipients, ", "))))
	case m.chatSending:
		lines = append(lines, "Enviando...")
	case m.chatSendErr != nil:
		lines = append(lines, "Error: "+safeLine(m.chatSendErr.Error()))
	}
	lines = append(lines, "Esc volver · Enter enviar · Alt+Enter salto de línea")
	return wrapLines(lines, m.width)
}

// composerBox wraps the docked composer in a rounded border with a
// one-space horizontal pad (K7), so the draft/placeholder text never
// touches the border itself.
func (m Model) composerBox() string {
	border := m.renderer().NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(style.ColorDim)).Padding(0, 1)
	return border.Render(m.composer.View())
}

// dayPillLine renders the day separator as a centered dim pill: a
// background only around the label itself, not the whole line width, so
// it reads as a pill rather than a full-width bar.
func (m Model) dayPillLine(label string) string {
	pillStyle := m.renderer().NewStyle().Background(lipgloss.Color(dayPillBg)).Foreground(lipgloss.Color(dayPillFg))
	return centerPill(label, m.width, pillStyle)
}

// centerPill centers " label " within width, applying pillStyle only to
// that padded label text; the remaining space on either side stays plain
// (unstyled), which is what makes it read as a pill instead of a bar.
func centerPill(label string, width int, pillStyle lipgloss.Style) string {
	text := " " + label + " "
	if width <= 0 {
		return pillStyle.Render(text)
	}
	text = runewidth.Truncate(text, width, "")
	total := runewidth.StringWidth(text)
	pad := width - total
	if pad < 0 {
		pad = 0
	}
	left := pad / 2
	right := pad - left
	return strings.Repeat(" ", left) + pillStyle.Render(text) + strings.Repeat(" ", right)
}

// presenceHeaderText shows the conversation's live presence the way
// WhatsApp does: "en línea"/"escribiendo…"/"grabando audio…"/"últ. vez
// HH:MM", or nothing for a channel/state without presence data
// ("unknown").
func presenceHeaderText(p core.Presence, now time.Time) string {
	switch p.State {
	case "online":
		return "en línea"
	case "typing":
		return "escribiendo…"
	case "recording":
		return "grabando audio…"
	case "offline":
		if p.LastSeen.IsZero() {
			return ""
		}
		return "últ. vez " + p.LastSeen.In(now.Location()).Format("15:04")
	default:
		return ""
	}
}

// chatBubbleLines renders one item as its bubble's styled, aligned lines:
// an optional sender-name line (group threads/others' messages, colored
// stably per sender, shown once per run of consecutive messages from the
// same sender), one line per line of body text, one "📎 name (size)" line
// per attachment, and a trailing dim time line — every line padded to
// exactly the bubble's own width *before* it is wrapped in a lipgloss
// style (so go-runewidth, not the ANSI-unaware alignment step after it,
// is the single source of truth for width; see alignBubbleLine), then
// aligned to the right (own messages) or left (others') edge of the full
// terminal width. dim/status back K10's optimistic own bubble: dim swaps
// the bubble's colors for the dim optimistic palette, and a non-empty
// status replaces the trailing "HH:MM" time line with that text (e.g.
// "enviando…"/"no enviado") instead.
func chatBubbleLines(r *lipgloss.Renderer, item core.Item, width int, showName bool, now time.Time, dim bool, status string) []string {
	bubbleWidth := chatBubbleWidth(width)
	bgHex := bubbleIncomingBg
	fgHex := bubbleIncomingFg
	if item.FromMe {
		bgHex = ownBubbleBg(item.Channel)
		fgHex = bubbleOwnFg
	}
	if dim {
		bgHex = bubbleOptimisticBg
		fgHex = bubbleOptimisticFg
	}
	bodyStyle := r.NewStyle().Background(lipgloss.Color(bgHex)).Foreground(lipgloss.Color(fgHex))
	timeStyle := r.NewStyle().Background(lipgloss.Color(bgHex)).Foreground(lipgloss.Color(style.ColorDim))

	var lines []string
	if showName {
		name := strings.TrimSpace(safeLine(item.From.Name))
		if name != "" {
			nameStyle := r.NewStyle().Background(lipgloss.Color(bgHex)).Foreground(lipgloss.Color(senderColorHex(name))).Bold(true)
			text := runewidth.Truncate(name, bubbleWidth, "…")
			padded := padTo(text, bubbleWidth)
			lines = append(lines, alignBubbleLine(nameStyle.Render(padded), bubbleWidth, width, item.FromMe))
		}
	}
	if item.Deleted {
		// S2: a revoked message keeps its row (and place in the
		// conversation) but shows neither its old body nor its
		// attachments — only the deletion marker, dim/italic like a
		// real chat client.
		delStyle := r.NewStyle().Background(lipgloss.Color(bgHex)).Foreground(lipgloss.Color(style.ColorDim)).Italic(true)
		text := runewidth.Truncate("mensaje eliminado", bubbleWidth, "…")
		padded := padTo(text, bubbleWidth)
		lines = append(lines, alignBubbleLine(delStyle.Render(padded), bubbleWidth, width, item.FromMe))
	} else {
		body := sanitizeTerminalText(item.Body)
		for _, raw := range strings.Split(body, "\n") {
			text := strings.ReplaceAll(raw, "\r", "")
			text = runewidth.Truncate(text, bubbleWidth, "…")
			padded := padTo(text, bubbleWidth)
			lines = append(lines, alignBubbleLine(bodyStyle.Render(padded), bubbleWidth, width, item.FromMe))
		}
		for _, attachment := range item.Attachments {
			text := fmt.Sprintf("📎 %s (%d bytes)", safeLine(attachment.Name), attachment.Size)
			text = runewidth.Truncate(text, bubbleWidth, "…")
			padded := padTo(text, bubbleWidth)
			lines = append(lines, alignBubbleLine(bodyStyle.Render(padded), bubbleWidth, width, item.FromMe))
		}
		if len(item.Reactions) > 0 {
			// S2: one emoji per reaction, sender order (reactionsFor's
			// own deterministic order) — repeats read as a count (two
			// 👍 means two people reacted with it), without needing to
			// show who.
			emojis := make([]string, len(item.Reactions))
			for i, reaction := range item.Reactions {
				emojis[i] = reaction.Emoji
			}
			text := runewidth.Truncate(strings.Join(emojis, " "), bubbleWidth, "…")
			padded := padTo(text, bubbleWidth)
			lines = append(lines, alignBubbleLine(bodyStyle.Render(padded), bubbleWidth, width, item.FromMe))
		}
		if item.Edited {
			editedStyle := r.NewStyle().Background(lipgloss.Color(bgHex)).Foreground(lipgloss.Color(style.ColorDim)).Italic(true)
			text := runewidth.Truncate("(editado)", bubbleWidth, "…")
			padded := padTo(text, bubbleWidth)
			lines = append(lines, alignBubbleLine(editedStyle.Render(padded), bubbleWidth, width, item.FromMe))
		}
	}
	if len(lines) == 0 {
		padded := padTo("", bubbleWidth)
		lines = append(lines, alignBubbleLine(bodyStyle.Render(padded), bubbleWidth, width, item.FromMe))
	}
	statusText := status
	if statusText == "" {
		statusText = messageTime(item.Timestamp, now)
	}
	statusText = runewidth.Truncate(statusText, bubbleWidth, "")
	timeLine := padLeft(statusText, bubbleWidth)
	lines = append(lines, alignBubbleLine(timeStyle.Render(timeLine), bubbleWidth, width, item.FromMe))
	return lines
}

// alignBubbleLine right-pads (own messages) or leaves as-is (others') so
// a bubble hugs the right/left edge like a real chat UI. contentWidth is
// the bubble's own (unstyled) width in terminal cells, passed explicitly
// rather than measured from text: text already carries a lipgloss
// style's ANSI escapes by the time this runs, which go-runewidth does
// not account for.
func alignBubbleLine(text string, contentWidth, totalWidth int, right bool) string {
	if totalWidth <= 0 || !right {
		return text
	}
	pad := totalWidth - contentWidth
	if pad <= 0 {
		return text
	}
	return strings.Repeat(" ", pad) + text
}
