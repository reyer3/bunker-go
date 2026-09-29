package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// openItem is Enter/click's transition for m.visibleRows()[index]: on a
// Mail sender row (mail-sender-groups.md) it toggles that sender's expand
// state and returns no command — the same "Enter ... on a sender row
// toggles it" Enter, →, and a click all share; on a thread row it enters
// detail view for its newest item and requests it asynchronously, same
// as before sender-groups.md. It is a no-op — returning m unchanged and
// no command — when a detail view is already open, there is no client,
// or index is out of range.
func (m Model) openItem(index int) (Model, tea.Cmd) {
	if m.detail {
		return m, nil
	}
	rows := m.visibleRows()
	if index < 0 || index >= len(rows) {
		return m, nil
	}
	row := rows[index]
	if row.kind == navSender {
		return m.setSenderExpanded(row.sender.key, !row.expanded), nil
	}
	if m.client == nil || len(row.thread.items) == 0 {
		return m, nil
	}
	item := row.thread.items[0]
	if m.externalOpen != nil {
		// The sidebar hands the conversation to its own pane (issue
		// #81) and stays on the list.
		return m, openExternally(m.externalOpen, item.ID)
	}
	return m.openConversation(item)
}

// openConversation opens item's conversation in the view its channel
// uses: Enter on an inbox row and "bunker open" (launch.go) both land
// here, so a conversation opened either way behaves the same.
func (m Model) openConversation(item core.Item) (Model, tea.Cmd) {
	// K5 (conversation-view.md): WhatsApp/Matrix open into the chat view,
	// which marks the conversation read with a receipt and loads it via
	// Thread instead of the old plain single-item detail. Mail keeps the
	// plain detail view here until K6 gives it its own thread view.
	if item.Channel == core.ChannelWhatsApp || item.Channel == core.ChannelMatrix {
		return m.openChat(item)
	}
	if item.Channel == core.ChannelMail {
		return m.openThread(item)
	}
	m.detail = true
	m.reading = true
	m.readErr = nil
	m.readItem = core.Item{}
	m.readID = item.ID
	m.readToken++
	m.detailScroll = 0
	return m, readItem(m.client, m.readID, m.readToken)
}

// openThread enters the K6 mail thread view for item's conversation: it
// loads the thread and marks the whole conversation \Seen immediately via
// ReadThread (no confirm — opening is the explicit action).
func (m Model) openThread(item core.Item) (Model, tea.Cmd) {
	m.unreadOnOpen = ""
	if item.Unread {
		m.unreadOnOpen = item.ID
	}
	m.detail = true
	m.threadMode = true
	m.threadChannel = item.Channel
	m.threadAccount = item.Account
	m.threadKey = item.Thread
	if m.threadKey == "" {
		m.threadKey = item.ID
	}
	m.threadSubject = item.Subject
	m.threadFolder = m.folderTag(item)
	m.threadItems = nil
	m.threadExpanded = nil
	m.threadSelected = 0
	m.threadLoading = true
	m.threadLoadErr = nil
	m.threadSeenErr = nil
	m.threadBodies = map[string]string{}
	m.threadBodyLoading = map[string]bool{}
	m.threadBodyErr = nil
	m.threadToken++
	return m, openThreadCmd(m.client, m.threadChannel, m.threadAccount, m.threadKey, m.threadToken)
}

// openChat enters the K5 chat view for item's conversation: it loads the
// newest page via Thread, marks it read with a receipt, fetches presence,
// and reports focused=true — all in one command (openChatCmd) — while
// resetting the shared composer for a fresh chat draft.
func (m Model) openChat(item core.Item) (Model, tea.Cmd) {
	m.detail = true
	m.chatMode = true
	m.chatChannel = item.Channel
	m.chatAccount = item.Account
	m.chatThread = item.Thread
	if m.chatThread == "" {
		m.chatThread = item.ID
	}
	m.chatDraftID = item.ID
	m.chatNewTo = ""
	m.unreadOnOpen = ""
	if item.Unread {
		m.unreadOnOpen = item.ID
	}
	m.chatName, _ = rowTitle(item)
	m.chatScroll = 0
	m.chatItems = nil
	m.chatLoading = true
	m.chatLoadErr = nil
	m.chatPresence = core.Presence{}
	m.chatPresenceErr = nil
	m.chatConfirm = false
	m.chatPlan = core.Plan{}
	m.chatSending = false
	m.chatSendErr = nil
	m.chatTypingOn = false
	m.chatPreviewPending = false
	m.chatOptimistic = nil
	m.chatToken++
	m.composer = newChatComposer(chatComposerWidth(m.width), m.renderer())
	if draft, ok := m.drafts[chatDraftKey(m.chatChannel, m.chatAccount, m.chatThread)]; ok {
		m.composer.SetValue(draft)
		m = m.resizeChatComposer()
	}
	return m, openChatCmd(m.client, m.chatChannel, m.chatAccount, m.chatThread, m.chatToken)
}

// leaveChat closes the chat view, best-effort reporting focused=false and
// composing=false (the daemon's own 60s lease timeout is the safety net
// if this call never lands, e.g. on a hard quit).
func (m Model) leaveChat() (Model, tea.Cmd) {
	channel, account, thread := m.chatChannel, m.chatAccount, m.chatThread
	if next, kept := m.keepDraft(chatDraftKey(channel, account, thread), m.composer.Value()); kept {
		m = next.withFlash("borrador guardado")
	} else {
		m = next
	}
	m.detail = false
	m.chatMode = false
	m.chatConfirm = false
	m.chatSending = false
	m.chatTypingOn = false
	m.chatPreviewPending = false
	m.chatOptimistic = nil
	m = m.clearChatAttachments()
	return m, leaveChatCmd(m.client, channel, account, thread)
}

// updateMouse handles G2: wheel scrolls the selection, a left click
// selects a row (or opens it when that row was already selected) or
// focuses a section (its header, rule, or "+N más" notice). It never
// sends or marks anything — the only actions it can reach are selecting,
// opening (via openItem, identical to Enter) and switchTab (identical to
// the 1/2/3/Tab keys) — and it only acts on the plain inbox: while
// previewing, marking, or with the help overlay open, every mouse event
// is ignored so a stray click can never interact with a screen it wasn't
// shown on. Composing is handled separately (updateComposeMouse): the
// wheel scrolls the draft there, but every other mouse action stays a
// no-op, same as these other overlays.
func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.picker != nil {
		return m.updatePickerMouse(msg)
	}
	if m.composing {
		return m.updateComposeMouse(msg)
	}
	if m.helpOpen {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			return m.updateHelp("k"), nil
		case tea.MouseButtonWheelDown:
			return m.updateHelp("j"), nil
		}
		return m, nil
	}
	if m.previewing || m.marking {
		return m, nil
	}
	if m.viewer != nil {
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			return m.closeViewer()
		}
		return m, nil
	}
	if m.detail {
		if m.chatMode {
			return m.updateChatMouse(msg)
		}
		if m.threadMode {
			return m.updateThreadMouse(msg)
		}
		return m.updateDetailMouse(msg)
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.selected > 0 {
			m.selected--
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		if m.selected < len(m.visibleRows())-1 {
			m.selected++
		}
		return m.maybeLoadMore()
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
			// A Mail sender row toggles on a single click, whether or not
			// it was already selected — unlike a thread row, which keeps
			// the "first click selects, a click on the already-selected
			// row opens it" pattern (mail-sender-groups.md's "a click on
			// the sender row toggles it").
			rows := m.visibleRows()
			if hit.row >= 0 && hit.row < len(rows) && rows[hit.row].kind == navSender {
				m.selected = hit.row
				return m.openItem(hit.row)
			}
			if hit.row == m.selected {
				return m.openItem(hit.row)
			}
			if hit.row >= 0 && hit.row < len(rows) {
				m.selected = hit.row
			}
			return m, nil
		}
	}
	return m, nil
}

// updateChatMouse handles the wheel while the K7 chat view is open: it
// scrolls the message window by chatWheelScroll lines, loading an older
// page once scrolled as far up as the loaded content allows (the same
// contract PgUp/the plain "Up" key already have). A click on an image
// thumbnail opens it full size; any other click is a no-op.
func (m Model) updateChatMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress || m.downloadActive {
			return m, nil
		}
		lines := strings.Split(m.View(), "\n")
		if msg.Y >= 0 && msg.Y < len(lines) {
			if key, ok := m.imageKeyAt(lines[msg.Y]); ok {
				if _, _, a, found := m.chatAttachment(key); found && isVideoAttachment(a) {
					return m, m.playVideo(key)
				}
				return m.openViewer(key)
			}
		}
		return m, nil
	case tea.MouseButtonWheelUp:
		return m.scrollChatUp(chatWheelScroll)
	case tea.MouseButtonWheelDown:
		m.chatScroll = clampScroll(m.chatScroll-chatWheelScroll, len(m.chatBodyLines()), m.chatScrollBudget())
		return m, nil
	}
	return m, nil
}

// updateThreadMouse handles the wheel while the K8 mail thread view is
// open: it scrolls the thread's rendered body by chatWheelScroll lines,
// the same "a long expanded body scrolls within the view" guarantee
// PgUp/PgDown give from the keyboard.
func (m Model) updateThreadMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	budget := m.threadScrollBudget()
	total := m.threadBodyLen()
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.threadScroll = clampScroll(m.threadScroll-chatWheelScroll, total, budget)
	case tea.MouseButtonWheelDown:
		m.threadScroll = clampScroll(m.threadScroll+chatWheelScroll, total, budget)
	}
	return m, nil
}

// updateDetailMouse handles the wheel while the plain single-item detail
// view is open (m.detail without chatMode/threadMode): it scrolls the
// body by chatWheelScroll lines, the same "a long body scrolls within the
// view" guarantee j/k/PgUp/PgDown/"G" give from the keyboard. A click is
// a no-op here, same as the chat/thread views.
func (m Model) updateDetailMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.reading || m.readErr != nil {
		return m, nil
	}
	budget := m.detailScrollBudget()
	total := len(m.detailBodyLines(m.readItem))
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.detailScroll = clampScroll(m.detailScroll-chatWheelScroll, total, budget)
	case tea.MouseButtonWheelDown:
		m.detailScroll = clampScroll(m.detailScroll+chatWheelScroll, total, budget)
	}
	return m, nil
}

// updateComposeMouse handles the wheel while the K4 reply composer
// (m.composing) is open: it scrolls the draft the same way PgUp/PgDown
// do (see updateCompose), composeWheelScroll CursorUp/CursorDown steps
// instead of a full composerHeight page. Every other mouse action
// (clicks) stays a no-op here, matching every other view.
func (m Model) updateComposeMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		for i := 0; i < composeWheelScroll; i++ {
			m.composer.CursorUp()
		}
	case tea.MouseButtonWheelDown:
		for i := 0; i < composeWheelScroll; i++ {
			m.composer.CursorDown()
		}
	}
	return m, nil
}
