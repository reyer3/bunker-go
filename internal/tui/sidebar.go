package tui

import (
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
)

// The sidebar (issue #81) is the inbox laid out for a pane of about 28-45
// columns docked beside coding agents in herdr: the four tabs as a short
// channel list with their unread counts, then the selected tab's
// conversations, each with a one-line preview of its last message. It reuses the inbox's rows, tabs, filter
// and keys; only the rendering differs, so j/k, Tab, 0-3, /, g and ? work
// as in the full TUI, and Enter goes through openItem (in place, or to
// the external opener when one is set).

// sidebarTabLabel is tab's name in the channel list: "Todo" for the
// overview, else the channel's brand name.
func sidebarTabLabel(tab int) string {
	if tab <= 0 || tab-1 >= len(channelOrder) {
		return "Todo"
	}
	return channelNames[channelOrder[tab-1]]
}

// sidebarTabCount is tab's unread total from the daemon's counts.
func (m Model) sidebarTabCount(tab int) int {
	if tab <= 0 || tab-1 >= len(channelOrder) {
		total := 0
		for _, ch := range channelOrder {
			total += channelUnreadTotal(m.headerCounts(), ch)
		}
		return total
	}
	return channelUnreadTotal(m.headerCounts(), channelOrder[tab-1])
}

// sidebarTabLine renders one entry of the channel list: a marker on the
// active tab, the channel glyph, its name and, right-aligned, its unread
// count (blank at zero, so the eye finds what needs attention).
func (m Model) sidebarTabLine(tab int, styles rowStyles, glyphs map[core.Channel]string, width int) string {
	marker := " "
	if tab == m.activeTab {
		marker = "▌"
	}
	glyph := "∗"
	var channel core.Channel
	if tab > 0 && tab-1 < len(channelOrder) {
		channel = channelOrder[tab-1]
		glyph = glyphs[channel]
	}
	count := ""
	if n := m.sidebarTabCount(tab); n > 0 {
		count = strconv.Itoa(n)
	}
	left := marker + " " + glyph + " "
	label := sidebarTabLabel(tab)
	if width > 0 {
		budget := max(1, width-runewidth.StringWidth(left)-runewidth.StringWidth(count)-1)
		label = padTo(runewidth.Truncate(label, budget, "…"), budget) + " "
	} else {
		label += " "
	}
	if tab != m.activeTab {
		styled := glyph
		if channel != "" {
			styled = styles.glyph[channel].Render(glyph)
		}
		return marker + " " + styled + " " + label + styles.dim.Render(count)
	}
	if channel == "" {
		return styles.title.Render(marker + " " + glyph + " " + label + count)
	}
	return styles.sectionHeader[channel].Render(marker + " " + glyph + " " + label + count)
}

// sidebarRowLine renders one navigable row: marker, the channel glyph (a
// chevron on a Mail sender row), the name and an unread badge, truncated
// so the badge always stays in view. With previews on it is followed by
// a second, dim line under the name: the last message ("Ana: hola", "Tú:
// ok", or just the text in a 1:1 chat), a Mail sender's newest subject, or
// a label such as "🎤 Nota de voz 0:12". Both lines carry the selection's
// bar and background.
func sidebarRowLine(row navRow, selected, previews bool, width int, glyphs map[core.Channel]string, styles rowStyles) (line, preview string) {
	var lead, title, text string
	var dimmed bool
	var channel core.Channel
	var unread int
	if row.kind == navSender {
		lead = "▸"
		if row.expanded {
			lead = "▾"
		}
		title = row.sender.name
		channel = core.ChannelMail
		unread = row.sender.unreadCount()
		if !row.expanded {
			text = senderPreview(row.sender)
		}
	} else {
		item := row.thread.items[0]
		channel = item.Channel
		lead = glyphs[channel]
		title, dimmed = rowTitle(item)
		unread = row.thread.unreadCount()
		text = chatPreview(item, title)
	}
	title = safeLine(title)
	indent := ""
	if row.indent {
		indent = strings.Repeat(" ", indentWidth)
	}
	marker := " "
	if selected {
		marker = "▌"
	}
	badge := unreadBadge(unread)
	left := marker + " " + indent + lead + " "
	if width > 0 {
		budget := max(1, width-runewidth.StringWidth(left)-runewidth.StringWidth(badge)-1)
		title = padTo(runewidth.Truncate(title, budget, "…"), budget)
	}
	title += " "
	if previews {
		// The preview starts under the name, one cell short of the pane
		// so a wide rune never lands on its last column.
		pad := strings.Repeat(" ", runewidth.StringWidth(left)-runewidth.StringWidth(marker))
		budget := 0
		if width > 0 {
			budget = max(1, width-runewidth.StringWidth(left)-1)
		}
		preview = pad + previewFit(text, budget)
	}
	if selected {
		rest := strings.TrimPrefix(left+title+badge, marker)
		line = styles.selectedBar.Render(marker) + styles.selectedRow.Render(padTo(rest, max(0, width-1)))
		if previews {
			preview = styles.selectedBar.Render(marker) + styles.selectedRow.Render(padTo(preview, max(0, width-1)))
		}
		return line, preview
	}
	titleStyle := styles.title
	if row.kind == navThread && row.thread.conversation && row.thread.unread == 0 {
		titleStyle = styles.readTitle
	}
	if dimmed {
		titleStyle = styles.dim
	}
	leadStyle := styles.glyph[channel]
	if row.kind == navSender {
		leadStyle = styles.title
	}
	line = marker + " " + indent + leadStyle.Render(lead) + " " + titleStyle.Render(title) + styles.badge[channel].Render(badge)
	if previews {
		preview = " " + styles.dim.Render(preview)
	}
	return line, preview
}

// sidebarPreviewMinBudget is the fewest row lines the list needs before
// it spends every second one on previews: below it (a pane a few lines
// tall) rows are single lines, so previews give way before rows do.
const sidebarPreviewMinBudget = 4

// sidebarMeetingListMin is how many lines of the sidebar's row list the
// meetings section leaves alone.
const sidebarMeetingListMin = 3

// sidebarLinesAndHits renders the sidebar and, line for line, what a
// click on each does (the same contract as the full inbox's hits).
func (m Model) sidebarLinesAndHits() (lines []string, hits []inboxHit) {
	styles := m.styles()
	glyphs := m.resolvedGlyphs()
	width := m.width
	add := func(line string, hit inboxHit) {
		lines = append(lines, line)
		hits = append(hits, hit)
	}

	if status, ok := m.statusLine(); ok {
		add(truncatePlain(status, width), inboxHit{kind: hitNone})
	}
	if filter, ok := m.filterLine(); ok {
		add(truncatePlain(filter, width), inboxHit{kind: hitNone})
	}
	for tab := 0; tab < numTabs; tab++ {
		add(m.sidebarTabLine(tab, styles, glyphs, width), inboxHit{kind: hitFocus, tab: tab})
	}
	add(separatorLine(styles, width), inboxHit{kind: hitNone})

	// The rows get whatever the fixed lines above and the footer below
	// leave, and scroll so the selection stays visible.
	budget := -1
	if m.height > 0 {
		budget = max(1, m.height-len(lines)-footerReservedLines)
	}
	// The meetings section sits under the list and only takes what the
	// list can spare: it keeps at least sidebarMeetingListMin lines.
	meetingAvail := 1 + meetingMaxRows
	if budget > 0 {
		meetingAvail = budget - sidebarMeetingListMin
	}
	meetingLines, meetingHits := m.meetingSection(styles, width, meetingAvail)
	if budget > 0 {
		budget = max(1, budget-len(meetingLines))
	}
	rows := m.visibleRows()
	switch {
	case !m.loaded:
		add(styles.dim.Render(truncatePlain("Cargando…", width)), inboxHit{kind: hitNone})
	case len(rows) == 0:
		add(m.emptySectionLine(styles, width), inboxHit{kind: hitNone})
	default:
		// Rows are two lines with previews and one without; the window
		// is counted in rows so it never splits one and never overflows.
		previews := budget < 0 || budget >= sidebarPreviewMinBudget
		per := 1
		if previews {
			per = 2
		}
		start, end := 0, len(rows)
		if budget > 0 {
			if visible := max(1, budget/per); len(rows) > visible {
				start = max(0, min(m.selected-visible+1, len(rows)-visible))
				end = start + visible
			}
		}
		for i := start; i < end; i++ {
			line, preview := sidebarRowLine(rows[i], i == m.selected, previews, width, glyphs, styles)
			add(line, inboxHit{kind: hitRow, row: i})
			if previews {
				add(preview, inboxHit{kind: hitRow, row: i})
			}
		}
	}

	for i, line := range meetingLines {
		add(line, meetingHits[i])
	}
	add(separatorLine(styles, width), inboxHit{kind: hitNone})
	add(styles.dim.Render(hintLine(width, m.withMeetingHint(m.withAskHint(sidebarHints, "a"))...)), inboxHit{kind: hitNone})
	return lines, hits
}
