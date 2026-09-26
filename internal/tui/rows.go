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

// buildRowUnits renders every group in groups to its 1-or-2-line row
// unit, marking the group at localSelected (if any, and if in range) as
// selected. Each unit's hit target is hitRow at its index in groups — the
// same index space m.visibleGroups() uses, so a click resolves directly
// to a selectable/openable row with no separate coordinate math.
func buildRowUnits(groups []inboxGroup, localSelected, width int, glyphs map[core.Channel]string, counts map[core.Channel]map[string]int, styles rowStyles, now time.Time) []rowUnit {
	rowUnits := make([]rowUnit, len(groups))
	for i, g := range groups {
		line1, line2 := buildRow(g, i == localSelected, width, glyphs, counts, styles, now)
		lines := []string{line1}
		if line2 != "" {
			lines = append(lines, line2)
		}
		rowUnits[i] = rowUnit{lines: lines, hit: inboxHit{kind: hitRow, row: i}}
	}
	return rowUnits
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

// footerLine renders the short dim keymap hint row.
func footerLine(styles rowStyles, width int) string {
	return styles.dim.Render(truncatePlain("↵ leer  r responder  m leído  ? ayuda", width))
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

// layoutSectionRows fits rowUnits (each 1 or 2 physical lines, uniform
// within one render) into share physical lines. When they all fit, every
// unit is returned as-is. Otherwise it scrolls to keep localSelected
// visible (localSelected < 0 means no selection in this section) and
// ends with a dim "+N más" notice for the rows that did not fit — a
// synthetic unit whose hit focuses focusTab (this section), matching G2's
// "a click on '+N más' also focuses that section".
func layoutSectionRows(rowUnits []rowUnit, share, localSelected, focusTab int, styles rowStyles, width int) []rowUnit {
	if len(rowUnits) == 0 || share <= 0 {
		return nil
	}
	perRow := len(rowUnits[0].lines)
	if perRow < 1 {
		perRow = 1
	}
	maxUnits := share / perRow
	if maxUnits < 1 {
		maxUnits = 1
	}
	if len(rowUnits) <= maxUnits {
		return rowUnits
	}

	// Reserve 1 line for the "+N más" notice by showing one fewer row
	// unit, but never drop below 1 visible row: keeping the selection
	// (and at least one conversation) visible outranks always having
	// room to spare for the notice in an extremely short pane.
	unitsShown := maxUnits
	noticeBudget := share - unitsShown*perRow
	if noticeBudget < 1 && unitsShown > 1 {
		unitsShown--
		noticeBudget = share - unitsShown*perRow
	}

	start := 0
	if localSelected >= 0 {
		if localSelected >= start+unitsShown {
			start = localSelected - unitsShown + 1
		}
		if localSelected < start {
			start = localSelected
		}
	}
	if start+unitsShown > len(rowUnits) {
		start = len(rowUnits) - unitsShown
	}
	if start < 0 {
		start = 0
	}

	out := append([]rowUnit{}, rowUnits[start:start+unitsShown]...)
	more := len(rowUnits) - unitsShown
	if noticeBudget > 0 {
		out = append(out, rowUnit{
			lines: []string{moreLine(more, styles, width)},
			hit:   inboxHit{kind: hitFocus, tab: focusTab},
		})
	}
	return out
}
