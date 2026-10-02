package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// "c" in the inbox and the sidebar calls the selected conversation. It
// does not carry its own call logic: it opens the conversation's chat and
// starts the chat's Alt+C action, so the dry-run preview, the confirm and
// every error are the chat's. A row that cannot be called says why in a
// flash and leaves the list as it is.

// selectedRowItem is the newest item of the selected conversation row.
func (m Model) selectedRowItem() (core.Item, bool) {
	rows := m.visibleRows()
	if m.selected < 0 || m.selected >= len(rows) {
		return core.Item{}, false
	}
	row := rows[m.selected]
	if row.kind != navThread || len(row.thread.items) == 0 {
		return core.Item{}, false
	}
	return row.thread.items[0], true
}

// rowCallTarget is the address openChat would call for item: its thread,
// else its ID.
func rowCallTarget(item core.Item) string {
	if item.Thread != "" {
		return item.Thread
	}
	return item.ID
}

// rowCallReason says why the selected row cannot be called, or "".
func (m Model) rowCallReason() string {
	if m.client == nil {
		return "sin conexión con el daemon"
	}
	item, ok := m.selectedRowItem()
	if !ok {
		return errCallOnlyWA.Error()
	}
	if err := m.callBlockedFor(item.Channel, rowCallTarget(item)); err != nil {
		return err.Error()
	}
	if m.externalOpen != nil {
		return "abre la conversación y pulsa Alt+C allí"
	}
	return ""
}

func (m Model) callSelectedRow() (tea.Model, tea.Cmd) {
	if m.client == nil {
		return m.withFlash("no se puede llamar: sin conexión con el daemon"), nil
	}
	item, ok := m.selectedRowItem()
	if !ok {
		return m.withFlash("no se puede llamar: " + errCallOnlyWA.Error()), nil
	}
	if err := m.callBlockedFor(item.Channel, rowCallTarget(item)); err != nil {
		return m.withFlash("no se puede llamar: " + humanError(err)), nil
	}
	if m.externalOpen != nil {
		// Opening here would go to another pane, so the call cannot be
		// started from this one.
		return m.withFlash("aquí la conversación se abre en otro panel: pulsa Alt+C allí para llamar"), nil
	}
	m, open := m.openChat(item)
	next, call := m.startChatCall()
	return next, tea.Batch(open, call)
}
