package tui

import (
	"strings"

	"github.com/reyer3/bunker-go/internal/core"
)

// footerReservedLines is the separator + hint line every inbox render
// (overview or focused) reserves at the bottom.
const footerReservedLines = 2

// sectionFixedLines is a section's header + brand-color rule line: always
// present, even for an empty section.
const sectionFixedLines = 2

// inboxView renders the sectioned inbox: an overview with all three
// channel sections sharing the pane fairly, or (once focused) a single
// section at full height with scrolling. It keeps fitInbox's guarantees:
// every line truncated to width, no trailing newline, every section
// header always visible in the overview, and the selection always
// visible.
func (m Model) inboxView() string {
	lines, _ := m.inboxLinesAndHits()
	return strings.Join(lines, "\n")
}

// inboxHits returns inboxView()'s per-physical-line click targets, in the
// same order: hits[i] is what a mouse click on rendered line i does (see
// inboxHit). Recomputed from the same model state inboxView() uses, so
// the two never drift out of sync with each other.
func (m Model) inboxHits() []inboxHit {
	_, hits := m.inboxLinesAndHits()
	return hits
}

func (m Model) inboxLinesAndHits() (lines []string, hits []inboxHit) {
	if m.sidebar {
		return m.sidebarLinesAndHits()
	}
	styles := m.styles()
	glyphs := m.resolvedGlyphs()
	width := m.width

	if !m.loaded {
		lines := []string{
			truncatePlain("Cargando bandeja de entrada…", width),
			separatorLine(styles, width),
			footerLine(styles, width),
		}
		return lines, repeatHit(inboxHit{kind: hitNone}, len(lines))
	}

	if status, ok := m.statusLine(); ok {
		lines = append(lines, truncatePlain(status, width))
		hits = append(hits, inboxHit{kind: hitNone})
	}
	if filter, ok := m.filterLine(); ok {
		lines = append(lines, truncatePlain(filter, width))
		hits = append(hits, inboxHit{kind: hitNone})
	}
	bodyHeight := m.height
	if bodyHeight > 0 {
		bodyHeight = max(0, bodyHeight-len(lines))
	}

	channel, focused := m.currentChannelFilter()
	renderBody := func(height int) ([]string, []inboxHit) {
		if focused {
			return m.focusedSectionLines(channel, styles, glyphs, width, height)
		}
		return m.overviewLines(styles, glyphs, width, height)
	}
	bodyLines, bodyHits := renderBody(bodyHeight)

	// The sections under the list (Reuniones, then Pendientes) take the
	// most room the list can spare: at most a third of the pane, shared
	// between them (splitLowerRoom), and only if the whole view still fits
	// the height, so they never push a section or the footer off screen.
	// An unknown height (0) shows them whole.
	var meetingLines []string
	var meetingHits []inboxHit
	if bodyHeight > 0 {
		for avail := min(bodyHeight/3, m.lowerSectionsWant()); avail >= 2; avail-- {
			ml, mh := m.lowerSections(styles, width, avail)
			if len(ml) == 0 {
				break
			}
			bl, bh := renderBody(bodyHeight - len(ml))
			if len(lines)+len(bl)+len(ml)+footerReservedLines <= m.height {
				bodyLines, bodyHits, meetingLines, meetingHits = bl, bh, ml, mh
				break
			}
		}
	} else {
		meetingLines, meetingHits = m.lowerSections(styles, width, -1)
	}
	lines = append(lines, bodyLines...)
	hits = append(hits, bodyHits...)
	lines = append(lines, meetingLines...)
	hits = append(hits, meetingHits...)

	lines = append(lines, separatorLine(styles, width), m.footerLine(styles, width))
	hits = append(hits, repeatHit(inboxHit{kind: hitNone}, 2)...)
	return lines, hits
}

// overviewLines renders every channel's section stacked, each getting a
// fair share of height's remaining room (after every section's fixed
// header+rule and the caller's separator+footer). No section can push
// another's header off screen: fairShares guarantees at least 1 row line
// per section once totalBudget covers it. A click anywhere on a header,
// its rule, or a "+N más" notice focuses that section (hitFocus); a
// click on a row selects/opens it (hitRow, indexed within that section).
func (m Model) overviewLines(styles rowStyles, glyphs map[core.Channel]string, width, height int) (lines []string, hits []inboxHit) {
	reserved := sectionFixedLines*len(channelOrder) + footerReservedLines
	rowBudget := -1
	if height > 0 {
		rowBudget = height - reserved
	}
	shares := fairShares(rowBudget, len(channelOrder))
	if rowBudget < 0 {
		// Unknown terminal size: show every row, no truncation notice.
		for i := range shares {
			shares[i] = 1 << 20
		}
	}

	offset := 0
	for i, channel := range channelOrder {
		focusTab := i + 1
		groups := m.sectionRows(channel)
		lines = append(lines,
			sectionHeaderLine(channel, glyphs[channel], styles, m.headerCounts(), width),
			sectionRuleLine(channel, styles, width),
		)
		hits = append(hits, repeatHit(inboxHit{kind: hitFocus, tab: focusTab}, 2)...)
		if len(groups) == 0 {
			lines = append(lines, m.emptySectionLine(styles, width))
			hits = append(hits, inboxHit{kind: hitNone})
			continue
		}
		localSelected := -1
		if m.selected >= offset && m.selected < offset+len(groups) {
			localSelected = m.selected - offset
		}
		offset += len(groups)

		rowUnits := buildRowUnits(groups, localSelected, width, glyphs, m.counts, styles, m.clock(), m.folderTag)
		shown := layoutSectionRows(rowUnits, shares[i], localSelected, focusTab, styles, width)
		unitLines, unitHits := flattenRowUnits(shown)
		lines = append(lines, unitLines...)
		hits = append(hits, unitHits...)
	}
	return lines, hits
}

// focusedSectionLines renders a single channel's section at full height,
// scrolled to keep the selection visible. Its header/rule are clickable
// (hitFocus is a no-op re-focus of the same tab); rows use hitRow indexed
// directly into m.visibleRows() (== this channel's sectionRows()).
func (m Model) focusedSectionLines(channel core.Channel, styles rowStyles, glyphs map[core.Channel]string, width, height int) (lines []string, hits []inboxHit) {
	focusTab := m.activeTab
	groups := m.sectionRows(channel)
	lines = []string{
		sectionHeaderLine(channel, glyphs[channel], styles, m.headerCounts(), width),
		sectionRuleLine(channel, styles, width),
	}
	hits = repeatHit(inboxHit{kind: hitFocus, tab: focusTab}, 2)
	if len(groups) == 0 {
		return append(lines, m.emptySectionLine(styles, width)), append(hits, inboxHit{kind: hitNone})
	}

	rowUnits := buildRowUnits(groups, m.selected, width, glyphs, m.counts, styles, m.clock(), m.folderTag)
	share := 1 << 20
	if height > 0 {
		share = height - sectionFixedLines
		if share < 1 {
			share = 1
		}
	}
	shown := layoutSectionRows(rowUnits, share, m.selected, focusTab, styles, width)
	unitLines, unitHits := flattenRowUnits(shown)
	return append(lines, unitLines...), append(hits, unitHits...)
}
