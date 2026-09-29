package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// selectedItemID returns the item id "r" (reply) and "m" (mark read) act
// on: the item open in detail view, or the newest item of the selected
// thread row. It never returns an id while a read is still loading or
// failed, matching the same item Enter/Esc show, and never resolves one
// from a Mail sender header row — only an actual thread carries an item
// to reply to or mark read.
func (m Model) selectedItemID() (string, bool) {
	if m.detail {
		if m.reading || m.readErr != nil || m.readItem.ID == "" {
			return "", false
		}
		return m.readItem.ID, true
	}
	visible := m.visibleRows()
	if m.selected < 0 || m.selected >= len(visible) {
		return "", false
	}
	row := visible[m.selected]
	if row.kind != navThread || len(row.thread.items) == 0 {
		return "", false
	}
	return row.thread.items[0].ID, true
}

// updateMark handles keys during the explicit mark-read flow: a mandatory
// dry-run preview, then an explicit confirm. It never runs from opening/
// reading an item, only from the "m" key.
func (m Model) updateMark(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.markSending {
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.marking = false
		m.markLoading = false
		m.markConfirm = false
		m.markID = ""
		m.markErr = nil
		m.markPlan = core.Plan{}
		return m, nil
	case "enter":
		if !m.markConfirm {
			return m, nil
		}
		m.markConfirm = false
		m.markSending = true
		return m, sendMarkRead(m.client, m.markID, m.markToken)
	}
	return m, nil
}
