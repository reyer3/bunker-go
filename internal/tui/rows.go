package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/style"
)

// channelOrder fixes the tab/segment order so it never jitters between
// renders, matching `bunker render`'s own channelOrder.
var channelOrder = []core.Channel{core.ChannelMail, core.ChannelWhatsApp, core.ChannelMatrix}

// selectionBackground is the selected row's full-width background: dark
// enough to read as a highlight against a dark theme (Ghostty
// "prussian-neon") without fighting the channel accent colors used for
// the glyph/badge/left bar.
const selectionBackground = "#1b2a3d"

// selectionForeground is the selected row's text color: bright enough to
// stay legible on selectionBackground.
const selectionForeground = "#f5f5f5"

// narrowWidth is the terminal width below which the inbox degrades to
// one line per conversation (no preview line, no account tag): there is
// not enough room for a legible two-line row.
const narrowWidth = 30

// rowStyles bundles the lipgloss styles View() needs, built once per
// render from the model's renderer so NO_COLOR/forced test profiles are
// honored consistently. Width math never uses these styles' own
// Width/MaxWidth: every string is truncated/padded with go-runewidth
// first, then wrapped in a style, so ANSI codes never confuse truncation.
type rowStyles struct {
	dim         lipgloss.Style
	title       lipgloss.Style
	selectedBar lipgloss.Style
	selectedRow lipgloss.Style
	glyph       map[core.Channel]lipgloss.Style
	badge       map[core.Channel]lipgloss.Style
	// sectionHeader is bold + the channel's brand color: the section
	// header line ("<glyph> Mail (3)").
	sectionHeader map[core.Channel]lipgloss.Style
}

// styles builds this model's lipgloss style set from its renderer.
func (m Model) styles() rowStyles {
	return newRowStyles(m.renderer())
}

func newRowStyles(r *lipgloss.Renderer) rowStyles {
	rs := rowStyles{
		dim:           r.NewStyle().Foreground(lipgloss.Color(style.ColorDim)),
		title:         r.NewStyle().Bold(true),
		selectedBar:   r.NewStyle().Background(lipgloss.Color(selectionBackground)).Bold(true),
		selectedRow:   r.NewStyle().Background(lipgloss.Color(selectionBackground)).Foreground(lipgloss.Color(selectionForeground)).Bold(true),
		glyph:         make(map[core.Channel]lipgloss.Style, len(style.ChannelColors)),
		badge:         make(map[core.Channel]lipgloss.Style, len(style.ChannelColors)),
		sectionHeader: make(map[core.Channel]lipgloss.Style, len(style.ChannelColors)),
	}
	for ch, hex := range style.ChannelColors {
		rs.glyph[ch] = r.NewStyle().Foreground(lipgloss.Color(hex))
		rs.badge[ch] = r.NewStyle().Foreground(lipgloss.Color(hex))
		rs.sectionHeader[ch] = r.NewStyle().Bold(true).Foreground(lipgloss.Color(hex))
	}
	return rs
}

// channelNames are the section header's channel labels: brand names, not
// translated (the rest of the inbox's static text is Spanish).
var channelNames = map[core.Channel]string{
	core.ChannelMail:     "Mail",
	core.ChannelWhatsApp: "WhatsApp",
	core.ChannelMatrix:   "Matrix",
}

// channelUnreadTotal sums counts[channel] across every account.
func channelUnreadTotal(counts map[core.Channel]map[string]int, channel core.Channel) int {
	total := 0
	for _, n := range counts[channel] {
		total += n
	}
	return total
}

// padTo right-pads plain (already truncated to at most width cells) with
// spaces so it occupies exactly width cells; used only before coloring
// (a full-width background needs the whole row's cells covered).
func padTo(plain string, width int) string {
	if width <= 0 {
		return plain
	}
	w := runewidth.StringWidth(plain)
	if w >= width {
		return plain
	}
	return plain + strings.Repeat(" ", width-w)
}

// channelHasMultipleAccounts reports whether counts lists more than one
// account for channel — the only case the row shows an account tag.
func channelHasMultipleAccounts(counts map[core.Channel]map[string]int, channel core.Channel) bool {
	return len(counts[channel]) > 1
}

// buildRow renders one conversation's two display lines. Every piece of
// text is truncated/padded to its cell budget with go-runewidth before
// any lipgloss style wraps it, so custom glyphs (including a Supplementary
// PUA override like U+100000) and wide/emoji text measure consistently
// with the rest of the TUI.
func buildRow(group inboxGroup, selected bool, width int, glyphs map[core.Channel]string, counts map[core.Channel]map[string]int, styles rowStyles, now time.Time) (line1, line2 string) {
	item := group.items[0]
	title, dimmed := rowTitle(item)
	glyph := glyphs[item.Channel]

	accountTag := ""
	if channelHasMultipleAccounts(counts, item.Channel) {
		accountTag = "  " + item.Account
	}
	timeStr := relativeTime(item.Timestamp, now)
	badgeText := fmt.Sprintf("⬤%d", len(group.items))

	marker := " "
	if selected {
		marker = "▌" // ▌
	}

	rightPlain := accountTag + "  " + timeStr + "  " + badgeText
	leftFixed := runewidth.StringWidth(marker) + 1 + runewidth.StringWidth(glyph) + 1
	rightWidth := runewidth.StringWidth(rightPlain)

	// titlePadded is the title, truncated and right-padded to fill the
	// exact column budget so the right-aligned time/badge line up. With
	// no known terminal width there is no budget to fill: the title is
	// shown in full, unpadded (fitInbox-equivalent truncation happens
	// once the real width is known).
	var titlePadded string
	if width > 0 {
		budget := width - leftFixed - rightWidth
		if budget < 1 {
			budget = 1
		}
		titleTrunc := runewidth.Truncate(title, budget, "…")
		pad := budget - runewidth.StringWidth(titleTrunc)
		if pad < 0 {
			pad = 0
		}
		titlePadded = titleTrunc + strings.Repeat(" ", pad)
	} else {
		titlePadded = title
	}

	line1Plain := marker + " " + glyph + " " + titlePadded + rightPlain

	preview := ""
	if width <= 0 || width >= narrowWidth {
		previewBudget := width - 2
		if width > 0 && previewBudget < 1 {
			previewBudget = 1
		}
		preview = "  " + previewLine(item, previewBudget)
	}

	if selected {
		rest := strings.TrimPrefix(line1Plain, marker)
		restWidth := 0
		if width > 0 {
			restWidth = width - runewidth.StringWidth(marker)
		}
		line1 = styles.selectedBar.Render(marker) + styles.selectedRow.Render(padTo(rest, restWidth))
		if preview != "" {
			line2 = styles.selectedRow.Render(padTo(preview, width))
		}
		return line1, line2
	}

	titleStyle := styles.title
	if dimmed {
		titleStyle = styles.dim
	}
	line1 = marker + " " + styles.glyph[item.Channel].Render(glyph) + " " + titleStyle.Render(titlePadded) +
		styles.dim.Render(accountTag+"  "+timeStr) + "  " + styles.badge[item.Channel].Render(badgeText)
	if preview != "" {
		line2 = styles.dim.Render(preview)
	}
	return line1, line2
}

// indentWidth is how many cells an expanded Mail sender's thread rows
// shift right, per mail-sender-groups.md ("indented by 2 cells").
const indentWidth = 2

// indentLine prefixes line with indentWidth plain spaces; called before
// any lipgloss style wraps the rest, so the indent itself is never
// colored (matching the rest of this file's "truncate/pad first, style
// last" discipline).
func indentLine(line string) string {
	return strings.Repeat(" ", indentWidth) + line
}

// buildRowUnits renders every row in rows to its 1-or-2-line row unit,
// marking the row at localSelected (if any, and if in range) as selected.
// Each unit's hit target is hitRow at its index in rows — the same index
// space m.visibleRows() uses, so a click resolves directly to a
// selectable/openable/toggleable row with no separate coordinate math. A
// Mail sender row (navSender) renders as its own single-line chevron
// header; an expanded sender's threads (navThread with indent set)
// render with the existing two-line row design, shifted right by
// indentWidth cells.
func buildRowUnits(rows []navRow, localSelected, width int, glyphs map[core.Channel]string, counts map[core.Channel]map[string]int, styles rowStyles, now time.Time) []rowUnit {
	rowUnits := make([]rowUnit, len(rows))
	for i, row := range rows {
		selected := i == localSelected
		if row.kind == navSender {
			line := buildSenderRow(row.sender, row.expanded, selected, width, counts, styles, now)
			rowUnits[i] = rowUnit{lines: []string{line}, hit: inboxHit{kind: hitRow, row: i}}
			continue
		}
		rowWidth := width
		if row.indent && width > 0 {
			rowWidth = width - indentWidth
			if rowWidth < 1 {
				rowWidth = 1
			}
		}
		line1, line2 := buildRow(row.thread, selected, rowWidth, glyphs, counts, styles, now)
		if row.indent {
			line1 = indentLine(line1)
			if line2 != "" {
				line2 = indentLine(line2)
			}
		}
		lines := []string{line1}
		if line2 != "" {
			lines = append(lines, line2)
		}
		rowUnits[i] = rowUnit{lines: lines, hit: inboxHit{kind: hitRow, row: i}}
	}
	return rowUnits
}

// buildSenderRow renders one Mail sender's collapsible header line: a
// chevron (▸ collapsed, ▾ expanded), the sender's display name (see
// senderDisplayName — the newest thread's newest non-empty From.Name,
// else the address itself), a dim account tag when Mail has more than
// one account, the sender's newest time, and an unread badge summing
// every thread's loaded unread count. It mirrors buildRow's column math
// (right-aligned account/time/badge) but is always a single line: a
// sender row never shows a body preview.
func buildSenderRow(s senderGroup, expanded, selected bool, width int, counts map[core.Channel]map[string]int, styles rowStyles, now time.Time) string {
	chevron := "▸"
	if expanded {
		chevron = "▾"
	}
	newest := s.threads[0].newest()
	accountTag := ""
	if channelHasMultipleAccounts(counts, core.ChannelMail) {
		accountTag = "  " + newest.Account
	}
	timeStr := relativeTime(newest.Timestamp, now)
	badgeText := fmt.Sprintf("⬤%d", s.unreadCount())

	marker := " "
	if selected {
		marker = "▌"
	}

	rightPlain := accountTag + "  " + timeStr + "  " + badgeText
	leftFixed := runewidth.StringWidth(marker) + 1 + runewidth.StringWidth(chevron) + 1
	rightWidth := runewidth.StringWidth(rightPlain)

	var namePadded string
	if width > 0 {
		budget := width - leftFixed - rightWidth
		if budget < 1 {
			budget = 1
		}
		nameTrunc := runewidth.Truncate(s.name, budget, "…")
		pad := budget - runewidth.StringWidth(nameTrunc)
		if pad < 0 {
			pad = 0
		}
		namePadded = nameTrunc + strings.Repeat(" ", pad)
	} else {
		namePadded = s.name
	}

	linePlain := marker + " " + chevron + " " + namePadded + rightPlain
	if selected {
		rest := strings.TrimPrefix(linePlain, marker)
		restWidth := 0
		if width > 0 {
			restWidth = width - runewidth.StringWidth(marker)
		}
		return styles.selectedBar.Render(marker) + styles.selectedRow.Render(padTo(rest, restWidth))
	}
	return marker + " " + styles.title.Render(chevron) + " " + styles.title.Render(namePadded) +
		styles.dim.Render(accountTag+"  "+timeStr) + "  " + styles.badge[core.ChannelMail].Render(badgeText)
}

// sectionHeaderLine renders one channel section's header: its brand-color
// glyph, channel name and unread count.
func sectionHeaderLine(channel core.Channel, glyph string, styles rowStyles, counts map[core.Channel]map[string]int, width int) string {
	plain := fmt.Sprintf("%s %s (%d)", glyph, channelNames[channel], channelUnreadTotal(counts, channel))
	plain = truncatePlain(plain, width)
	return styles.sectionHeader[channel].Render(plain)
}

// sectionRuleLine renders the thin rule under a section header, colored
// in that channel's brand accent.
func sectionRuleLine(channel core.Channel, styles rowStyles, width int) string {
	w := width
	if w <= 0 {
		w = 60
	}
	return styles.glyph[channel].Render(strings.Repeat("─", w))
}

// separatorLine renders the plain (dim) horizontal rule above the footer.
func separatorLine(styles rowStyles, width int) string {
	return styles.dim.Render(strings.Repeat("─", widthOrDefault(width)))
}

// footerLine renders the short dim keymap hint row. "q salir" comes first
// so a narrow width's ellipsis truncation (truncatePlain) always cuts
// from the tail end, never hiding "q" — the follow-up gap bunker-tui.md
// recorded (a 40-column terminal used to lose "q to quit" entirely, since
// this line never even mentioned it).
func footerLine(styles rowStyles, width int) string {
	return styles.dim.Render(truncatePlain("q salir  ↵ leer  r responder  m leído  ? ayuda", width))
}

// emptySectionLine renders a section's single dim placeholder line when
// it has no unread conversations.
func emptySectionLine(styles rowStyles, width int) string {
	return styles.dim.Render(truncatePlain("sin pendientes", width))
}

// moreLine renders the dim "+N más" truncation notice a section shows
// when it has more rows than its fair share (overview) or visible window
// (focused) can display.
func moreLine(more int, styles rowStyles, width int) string {
	return styles.dim.Render(truncatePlain(fmt.Sprintf("+%d más", more), width))
}

// truncatePlain truncates plain (uncolored) text to width cells with an
// ellipsis; width<=0 means "unknown/unbounded", so the text is returned
// unchanged. Always call this before wrapping text in a lipgloss style,
// never after: go-runewidth does not understand ANSI escape sequences.
func truncatePlain(plain string, width int) string {
	if width <= 0 {
		return plain
	}
	return runewidth.Truncate(plain, width, "…")
}

// widthOrDefault returns width, or 60 (the doc's documented mockup width)
// when the terminal size is not yet known.
func widthOrDefault(width int) int {
	if width <= 0 {
		return 60
	}
	return width
}

// fairShares splits totalBudget physical lines across n sections as
// evenly as possible, earlier sections (fixed channel order: Mail,
// WhatsApp, Matrix) getting any remainder line first. A non-positive
// totalBudget still yields at least 1 line per section, so no section is
// ever left with zero room.
func fairShares(totalBudget, n int) []int {
	if n <= 0 {
		return nil
	}
	if totalBudget < n {
		totalBudget = n
	}
	base := totalBudget / n
	rem := totalBudget % n
	shares := make([]int, n)
	for i := range shares {
		shares[i] = base
		if i < rem {
			shares[i]++
		}
	}
	return shares
}

// unitLines returns u's physical line count (at least 1, defensively: a
// unit with no lines still occupies a row).
func unitLines(u rowUnit) int {
	if n := len(u.lines); n > 0 {
		return n
	}
	return 1
}

// layoutSectionRows fits rowUnits into share physical lines by their
// actual, possibly mixed, per-unit heights: a Mail section can combine
// 1-line collapsed sender rows with 2-line thread rows (an expanded
// sender's own threads) in the very same render — unlike before
// mail-sender-groups.md, when every row in one render shared one uniform
// height. When they all fit, every unit is returned as-is. Otherwise it
// builds a contiguous window around localSelected (localSelected < 0
// means no selection in this section), growing forward then backward
// while it still fits share (minus 1 reserved line for the "+N más"
// notice — except the selected unit itself is never dropped even if its
// own height alone exceeds that reduced budget: keeping the selection
// visible outranks always having room to spare for the notice in an
// extremely short pane). The notice is a synthetic unit whose hit
// focuses focusTab (this section), matching G2's "a click on '+N más'
// also focuses that section".
func layoutSectionRows(rowUnits []rowUnit, share, localSelected, focusTab int, styles rowStyles, width int) []rowUnit {
	if len(rowUnits) == 0 || share <= 0 {
		return nil
	}
	total := 0
	for _, u := range rowUnits {
		total += unitLines(u)
	}
	if total <= share {
		return rowUnits
	}

	sel := localSelected
	if sel < 0 {
		sel = 0
	}
	if sel >= len(rowUnits) {
		sel = len(rowUnits) - 1
	}

	budget := share - 1
	selLines := unitLines(rowUnits[sel])
	if budget < selLines {
		budget = selLines
	}

	// Grow a window starting at the selection, forward first (so what
	// follows it is preferred when there is a tie), then backward with
	// any budget left over.
	start, end := sel, sel+1
	used := selLines
	for end < len(rowUnits) {
		need := unitLines(rowUnits[end])
		if used+need > budget {
			break
		}
		used += need
		end++
	}
	for start > 0 {
		need := unitLines(rowUnits[start-1])
		if used+need > budget {
			break
		}
		used += need
		start--
	}

	out := append([]rowUnit{}, rowUnits[start:end]...)
	more := len(rowUnits) - (end - start)
	if more > 0 && used < share {
		out = append(out, rowUnit{
			lines: []string{moreLine(more, styles, width)},
			hit:   inboxHit{kind: hitFocus, tab: focusTab},
		})
	}
	return out
}
