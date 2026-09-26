package tui

import "github.com/reyer3/bunker-go/internal/core"

// currentChannelFilter returns the channel the active focus narrows to,
// and false for focus 0 (the overview, every channel's section visible).
func (m Model) currentChannelFilter() (core.Channel, bool) {
	if m.activeTab <= 0 || m.activeTab-1 >= len(channelOrder) {
		return "", false
	}
	return channelOrder[m.activeTab-1], true
}

// sectionGroups returns m.groups belonging to channel, keeping their
// existing newest-first order.
func (m Model) sectionGroups(channel core.Channel) []inboxGroup {
	out := make([]inboxGroup, 0, len(m.groups))
	for _, g := range m.groups {
		if len(g.items) > 0 && g.items[0].Channel == channel {
			out = append(out, g)
		}
	}
	return out
}

// overviewOrder flattens every channel's section, in fixed channel order,
// each section internally newest-first. This is the navigable order j/k
// walks in the overview (focus 0): it crosses section boundaries instead
// of mixing channels by timestamp.
func (m Model) overviewOrder() []inboxGroup {
	out := make([]inboxGroup, 0, len(m.groups))
	for _, ch := range channelOrder {
		out = append(out, m.sectionGroups(ch)...)
	}
	return out
}

// visibleGroups returns the groups navigable by j/k and addressable by
// Enter/r/m in the current focus: overviewOrder() for the overview, or
// just one channel's section when focused.
func (m Model) visibleGroups() []inboxGroup {
	channel, ok := m.currentChannelFilter()
	if !ok {
		return m.overviewOrder()
	}
	return m.sectionGroups(channel)
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
	visible := len(m.visibleGroups())
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
