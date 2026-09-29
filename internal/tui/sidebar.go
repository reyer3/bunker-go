package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
)

// The sidebar (issue #81) is the inbox laid out for a pane of about 28-45
// columns docked beside coding agents in herdr: the four tabs as a short
// channel list with their unread counts, then the selected tab's
// conversations one line each. It reuses the inbox's rows, tabs, filter
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
			total += channelUnreadTotal(m.counts, ch)
		}
		return total
	}
	return channelUnreadTotal(m.counts, channelOrder[tab-1])
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

// sidebarRowLine renders one navigable row on a single line: marker,
// the channel glyph (a chevron on a Mail sender row), the name and an
// unread badge, truncated so the badge always stays in view.
func sidebarRowLine(row navRow, selected bool, width int, glyphs map[core.Channel]string, styles rowStyles) string {
	var lead, title string
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
	} else {
		item := row.thread.items[0]
		channel = item.Channel
		lead = glyphs[channel]
		title, dimmed = rowTitle(item)
		unread = len(row.thread.items)
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
	badge := fmt.Sprintf("⬤%d", unread)
	left := marker + " " + indent + lead + " "
	if width > 0 {
		budget := max(1, width-runewidth.StringWidth(left)-runewidth.StringWidth(badge)-1)
		title = padTo(runewidth.Truncate(title, budget, "…"), budget)
	}
	title += " "
	if selected {
		rest := strings.TrimPrefix(left+title+badge, marker)
		return styles.selectedBar.Render(marker) + styles.selectedRow.Render(padTo(rest, max(0, width-1)))
	}
	titleStyle := styles.title
	if dimmed {
		titleStyle = styles.dim
	}
	leadStyle := styles.glyph[channel]
	if row.kind == navSender {
		leadStyle = styles.title
	}
	return marker + " " + indent + leadStyle.Render(lead) + " " + titleStyle.Render(title) + styles.badge[channel].Render(badge)
}

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
	rows := m.visibleRows()
	switch {
	case !m.loaded:
		add(styles.dim.Render(truncatePlain("Cargando…", width)), inboxHit{kind: hitNone})
	case len(rows) == 0:
		add(m.emptySectionLine(styles, width), inboxHit{kind: hitNone})
	default:
		start, end := 0, len(rows)
		if budget > 0 && len(rows) > budget {
			start = max(0, min(m.selected-budget+1, len(rows)-budget))
			end = start + budget
		}
		for i := start; i < end; i++ {
			add(sidebarRowLine(rows[i], i == m.selected, width, glyphs, styles), inboxHit{kind: hitRow, row: i})
		}
	}

	add(separatorLine(styles, width), inboxHit{kind: hitNone})
	add(styles.dim.Render(hintLine(width, m.withAskHint(sidebarHints, "a")...)), inboxHit{kind: hitNone})
	return lines, hits
}
