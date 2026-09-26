package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// openItem transitions into detail view for m.visibleGroups()[index]'s
// newest item and requests it asynchronously (the same transition
// "enter" performs on the selected row). It is a no-op — returning m
// unchanged and no command — when a detail view is already open, there
// is no client, or index is out of range.
func (m Model) openItem(index int) (Model, tea.Cmd) {
	visible := m.visibleGroups()
	if m.detail || m.client == nil || index < 0 || index >= len(visible) || len(visible[index].items) == 0 {
		return m, nil
	}
	m.detail = true
	m.reading = true
	m.readErr = nil
	m.readItem = core.Item{}
	m.readID = visible[index].items[0].ID
	m.readToken++
	return m, readItem(m.client, m.readID, m.readToken)
}

// updateMouse handles G2: wheel scrolls the selection, a left click
// selects a row (or opens it when that row was already selected) or
// focuses a section (its header, rule, or "+N más" notice). It never
// sends or marks anything — the only actions it can reach are selecting,
// opening (via openItem, identical to Enter) and switchTab (identical to
// the 1/2/3/Tab keys) — and it only acts on the plain inbox: while
// composing, previewing, marking, reading a detail, or with the help
// overlay open, every mouse event is ignored so a stray click can never
// interact with a screen it wasn't shown on.
func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.helpOpen || m.composing || m.previewing || m.marking || m.detail {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.selected > 0 {
			m.selected--
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		if m.selected < len(m.visibleGroups())-1 {
			m.selected++
		}
		return m, nil
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		hits := m.inboxHits()
		if msg.Y < 0 || msg.Y >= len(hits) {
			return m, nil
		}
		switch hit := hits[msg.Y]; hit.kind {
		case hitFocus:
			return m.switchTab(hit.tab), nil
		case hitRow:
			if hit.row == m.selected {
				return m.openItem(hit.row)
			}
			if hit.row >= 0 && hit.row < len(m.visibleGroups()) {
				m.selected = hit.row
			}
			return m, nil
		}
	}
	return m, nil
}
