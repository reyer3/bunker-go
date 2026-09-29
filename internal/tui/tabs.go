package tui

import "github.com/reyer3/bunker-go/internal/core"

// navRowKind distinguishes the inbox's two navigable row shapes.
type navRowKind int

const (
	// navThread is a single conversation row: the pre-existing per-
	// thread two-line row design. Every WhatsApp/Matrix row is always
	// this kind, never wrapped by a sender — only Mail groups by sender
	// (mail-sender-groups.md's Decisions).
	navThread navRowKind = iota
	// navSender is a Mail-only collapsible sender header row (chevron,
	// display name, account tag, newest time, unread badge).
	navSender
)

// navRow is one navigable, selectable, and (for Mail) collapsible row of
// the inbox. senderKey/indent are only set on a navThread row nested
// under an expanded sender; every other row leaves them zero. expanded
// is only meaningful on a navSender row (whether its threads are the
// following rows).
type navRow struct {
	kind      navRowKind
	thread    inboxGroup
	sender    senderGroup
	expanded  bool
	indent    bool
	senderKey string
}

// currentChannelFilter returns the channel the active focus narrows to,
// and false for focus 0 (the overview, every channel's section visible).
func (m Model) currentChannelFilter() (core.Channel, bool) {
	if m.activeTab <= 0 || m.activeTab-1 >= len(channelOrder) {
		return "", false
	}
	return channelOrder[m.activeTab-1], true
}

// threadGroups returns m.groups belonging to channel, keeping their
// existing newest-first order: the raw per-conversation groups, before
// any Mail sender wrapping.
func (m Model) threadGroups(channel core.Channel) []inboxGroup {
	if m.queryActive {
		// The daemon already applied the query: its results are shown
		// as they are, never filtered again as text.
		out := make([]inboxGroup, 0, len(m.queryGroups))
		for _, g := range m.queryGroups {
			if len(g.items) > 0 && g.items[0].Channel == channel {
				out = append(out, g)
			}
		}
		return out
	}
	out := make([]inboxGroup, 0, len(m.groups))
	query := m.foldedFilter()
	for _, g := range m.groups {
		if len(g.items) > 0 && g.items[0].Channel == channel && groupMatches(g, query) {
			out = append(out, g)
		}
	}
	return out
}

// sectionRows returns channel's navigable rows. Mail wraps its threads
// under collapsible sender headers — one navSender row per sender,
// followed by its threads (navThread, indented) only when
// m.mailExpanded[key] is true; every other channel is unchanged: a bare
// navThread row per conversation, same order as before sender-groups.md.
func (m Model) sectionRows(channel core.Channel) []navRow {
	threads := m.threadGroups(channel)
	// Query results list every matching conversation directly: folding
	// them under sender headers would hide the very matches asked for.
	if channel != core.ChannelMail || m.queryActive {
		rows := make([]navRow, len(threads))
		for i, t := range threads {
			rows[i] = navRow{kind: navThread, thread: t}
		}
		return rows
	}
	var rows []navRow
	for _, s := range groupBySender(threads) {
		expanded := m.mailExpanded[s.key]
		rows = append(rows, navRow{kind: navSender, sender: s, expanded: expanded})
		if expanded {
			for _, t := range s.threads {
				rows = append(rows, navRow{kind: navThread, thread: t, indent: true, senderKey: s.key})
			}
		}
	}
	return rows
}

// overviewOrder flattens every channel's section, in fixed channel order,
// each section internally newest-first. This is the navigable order j/k
// walks in the overview (focus 0): it crosses section boundaries instead
// of mixing channels by timestamp.
func (m Model) overviewOrder() []navRow {
	out := make([]navRow, 0, len(m.groups))
	for _, ch := range channelOrder {
		out = append(out, m.sectionRows(ch)...)
	}
	return out
}

// visibleRows returns the rows navigable by j/k and addressable by
// Enter/←/→/click/r/m in the current focus: overviewOrder() for the
// overview, or just one channel's section when focused.
func (m Model) visibleRows() []navRow {
	channel, ok := m.currentChannelFilter()
	if !ok {
		return m.overviewOrder()
	}
	return m.sectionRows(channel)
}

// setSenderExpanded sets a Mail sender's expand state (allocating
// mailExpanded lazily on first use). The state persists across polls —
// see Model.mailExpanded's doc comment.
func (m Model) setSenderExpanded(key string, expanded bool) Model {
	if m.mailExpanded == nil {
		m.mailExpanded = map[string]bool{}
	}
	m.mailExpanded[key] = expanded
	return m
}

// expandRight handles →: only a Mail sender row responds, toggling its
// expand state; any other row (a thread, or no selection) is a no-op —
// unlike Enter, → never opens a thread.
func (m Model) expandRight() Model {
	rows := m.visibleRows()
	if m.selected < 0 || m.selected >= len(rows) {
		return m
	}
	row := rows[m.selected]
	if row.kind != navSender {
		return m
	}
	return m.setSenderExpanded(row.sender.key, !row.expanded)
}

// collapseLeft handles ←: a Mail sender row collapses (never expands); a
// thread row nested under an expanded sender jumps the selection up to
// its own sender row and collapses it, so the selection is never left
// pointing at a row that is about to disappear. Any other row (a
// non-nested thread, or no selection) is a no-op.
func (m Model) collapseLeft() Model {
	rows := m.visibleRows()
	if m.selected < 0 || m.selected >= len(rows) {
		return m
	}
	row := rows[m.selected]
	switch {
	case row.kind == navSender:
		return m.setSenderExpanded(row.sender.key, false)
	case row.kind == navThread && row.indent:
		m = m.setSenderExpanded(row.senderKey, false)
		for i, r := range m.visibleRows() {
			if r.kind == navSender && r.sender.key == row.senderKey {
				m.selected = i
				break
			}
		}
		return m
	default:
		return m
	}
}

// switchTab moves focus to tab (clamped to [0, numTabs)): 0 is the
// overview, 1..len(channelOrder) focuses that channel's section alone.
// It remembers the current focus's selection and restores the
// destination's last selection (clamped to what is now visible there).
func (m Model) switchTab(tab int) Model {
	if tab < 0 {
		tab = 0
	}
	if tab >= numTabs {
		tab = numTabs - 1
	}
	if tab == m.activeTab {
		return m
	}
	m.tabSelected[m.activeTab] = m.selected
	m.activeTab = tab
	visible := len(m.visibleRows())
	sel := m.tabSelected[tab]
	if sel >= visible {
		sel = visible - 1
	}
	if sel < 0 {
		sel = 0
	}
	m.selected = sel
	return m
}
