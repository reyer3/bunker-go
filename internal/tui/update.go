package tui

import (
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.FocusMsg:
		m.blurred = false
	case tea.BlurMsg:
		m.blurred = true
	case replyPreviewMsg:
		if msg.token != m.replyToken || !m.composing {
			return m, nil
		}
		if msg.err != nil {
			m.replyErr = msg.err
			return m, nil
		}
		m.composing = false
		m.previewing = true
		m.previewPlan = msg.plan
		m.replyErr = nil
		return m, nil
	case replySentMsg:
		if msg.token != m.replyToken || !m.sending {
			return m, nil
		}
		m.sending = false
		if msg.err != nil {
			m.replyErr = msg.err
			m.previewing = false
			m.composing = true
			return m, nil
		}
		m.composing = false
		m.previewing = false
		m.draftID = ""
		m.draftBody = ""
		m.attachments = nil
		m.previewPlan = core.Plan{}
		m.replyErr = nil
		return m, nil
	case markPreviewMsg:
		if msg.token != m.markToken || !m.marking || !m.markLoading {
			return m, nil
		}
		m.markLoading = false
		if msg.err != nil {
			m.markErr = msg.err
			return m, nil
		}
		m.markConfirm = true
		m.markPlan = msg.plan
		m.markErr = nil
		return m, nil
	case markSentMsg:
		if msg.token != m.markToken || !m.markSending {
			return m, nil
		}
		m.markSending = false
		if msg.err != nil {
			m.markErr = msg.err
			m.markConfirm = false
			return m, nil
		}
		m.marking = false
		m.markID = ""
		m.markPlan = core.Plan{}
		m.markErr = nil
		return m, nil
	case itemReadMsg:
		if !m.detail || msg.token != m.readToken || msg.id != m.readID {
			return m, nil
		}
		m.reading = false
		m.readItem = msg.item
		m.readErr = msg.err
	case inboxLoadedMsg:
		if !m.polling || msg.token != m.pollToken {
			return m, nil
		}
		m.polling = false
		if m.refreshPending {
			m.refreshPending = false
			return m.startPoll()
		}
		wasLoaded := m.loaded
		m.loaded = true
		m.loadErr = msg.listErr
		if msg.listErr != nil {
			return m, nextPoll(m.pollToken)
		}
		m.loadErr = msg.countsErr
		if msg.countsErr != nil {
			return m, nextPoll(m.pollToken)
		}
		if len(msg.items) > inboxLimit {
			msg.items = msg.items[:inboxLimit]
		}
		oldGroups := m.groups
		m.groups = groupUnread(msg.items)
		m.counts = msg.counts
		if visible := len(m.visibleGroups()); m.selected >= visible {
			m.selected = max(0, visible-1)
		}
		var notifyCmd tea.Cmd
		m, notifyCmd = m.maybeNotify(wasLoaded, oldGroups, msg.items)
		return m, tea.Batch(nextPoll(m.pollToken), notifyCmd)
	case pollTickMsg:
		if msg.token != m.pollToken || m.polling || m.client == nil {
			return m, nil
		}
		return m.startPoll()
	case tea.KeyMsg:
		if m.helpOpen {
			switch msg.String() {
			case "esc", "?":
				m.helpOpen = false
			}
			return m, nil
		}
		if m.composing {
			if m.attaching {
				return m.updateAttach(msg)
			}
			return m.updateCompose(msg)
		}
		if m.previewing {
			return m.updatePreview(msg)
		}
		if m.marking {
			return m.updateMark(msg)
		}
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "?":
			m.helpOpen = true
		case "esc":
			if m.detail {
				m.detail = false
				m.reading = false
				m.readErr = nil
				m.readItem = core.Item{}
				m.readToken++
			}
		case "g":
			if m.client == nil {
				return m, nil
			}
			if m.polling {
				m.refreshPending = true
				return m, nil
			}
			return m.startPoll()
		case "0":
			if !m.detail {
				m = m.switchTab(0)
			}
		case "1":
			if !m.detail {
				m = m.switchTab(1)
			}
		case "2":
			if !m.detail {
				m = m.switchTab(2)
			}
		case "3":
			if !m.detail {
				m = m.switchTab(3)
			}
		case "tab":
			if !m.detail {
				m = m.switchTab((m.activeTab + 1) % numTabs)
			}
		case "shift+tab":
			if !m.detail {
				m = m.switchTab((m.activeTab - 1 + numTabs) % numTabs)
			}
		case "enter":
			if !m.detail {
				return m.openItem(m.selected)
			}
		case "j", "down":
			if !m.detail && m.selected < len(m.visibleGroups())-1 {
				m.selected++
			}
		case "k", "up":
			if !m.detail && m.selected > 0 {
				m.selected--
			}
		case "r":
			if id, ok := m.selectedItemID(); ok && m.client != nil {
				m.composing = true
				m.draftID = id
				m.draftBody = ""
				m.attachments = nil
				m.attaching = false
				m.attachInput = ""
				m.replyErr = nil
				m.previewPlan = core.Plan{}
			}
		case "m":
			if id, ok := m.selectedItemID(); ok && m.client != nil {
				m.marking = true
				m.markLoading = true
				m.markConfirm = false
				m.markSending = false
				m.markID = id
				m.markErr = nil
				m.markPlan = core.Plan{}
				m.markToken++
				return m, previewMarkRead(m.client, id, m.markToken)
			}
		}
	}
	return m, nil
}

// selectedItemID returns the item id "r" (reply) and "m" (mark read) act
// on: the item open in detail view, or the newest item of the selected
// inbox group. It never returns an id while a read is still loading or
// failed, matching the same item Enter/Esc show.
func (m Model) selectedItemID() (string, bool) {
	if m.detail {
		if m.reading || m.readErr != nil || m.readItem.ID == "" {
			return "", false
		}
		return m.readItem.ID, true
	}
	visible := m.visibleGroups()
	if m.selected >= 0 && m.selected < len(visible) && len(visible[m.selected].items) > 0 {
		return visible[m.selected].items[0].ID, true
	}
	return "", false
}

// updateCompose handles keys while drafting a reply. Every key is literal
// text except the handful of compose control keys; this is what lets "q"
// and "r" be typed into the draft instead of triggering quit/reply again.
func (m Model) updateCompose(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.composing = false
		m.draftID = ""
		m.draftBody = ""
		m.attachments = nil
		m.attaching = false
		m.attachInput = ""
		m.replyErr = nil
		return m, nil
	case "ctrl+a":
		m.attaching = true
		m.attachInput = ""
		m.replyErr = nil
		return m, nil
	case "ctrl+x":
		if len(m.attachments) > 0 {
			m.attachments = m.attachments[:len(m.attachments)-1]
		}
		return m, nil
	case "ctrl+s":
		if err := validateAttachments(m.attachments); err != nil {
			m.replyErr = err
			return m, nil
		}
		m.replyErr = nil
		m.replyToken++
		attachments := append([]string(nil), m.attachments...)
		return m, previewReply(m.client, m.draftID, m.draftBody, attachments, m.replyToken)
	case "enter":
		m.draftBody += "\n"
	case "backspace":
		if m.draftBody != "" {
			_, size := utf8.DecodeLastRuneInString(m.draftBody)
			m.draftBody = m.draftBody[:len(m.draftBody)-size]
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.draftBody += string(msg.Runes)
		} else if msg.Type == tea.KeySpace {
			m.draftBody += " "
		}
	}
	return m, nil
}

// updateAttach handles keys while typing a local attachment path. Every key
// is literal path text (spaces allowed, no shell involved) except Esc and
// Enter; Enter validates the path immediately so a bad path is visible
// before it ever reaches a preview or send.
func (m Model) updateAttach(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.attaching = false
		m.attachInput = ""
		return m, nil
	case "enter":
		path := strings.TrimSpace(m.attachInput)
		m.attaching = false
		m.attachInput = ""
		if path == "" {
			return m, nil
		}
		if _, _, err := statAttachment(path); err != nil {
			m.replyErr = err
			return m, nil
		}
		m.replyErr = nil
		m.attachments = append(m.attachments, path)
		return m, nil
	case "backspace":
		if m.attachInput != "" {
			_, size := utf8.DecodeLastRuneInString(m.attachInput)
			m.attachInput = m.attachInput[:len(m.attachInput)-size]
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.attachInput += string(msg.Runes)
		} else if msg.Type == tea.KeySpace {
			m.attachInput += " "
		}
	}
	return m, nil
}

// updatePreview handles keys once a dry-run preview is showing. It is the
// only place Enter sends for real, and it is guarded so a send in flight
// swallows every key instead of launching a second one.
func (m Model) updatePreview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sending {
		return m, nil
	}
	switch msg.String() {
	case "q":
		if m.quitConfirm {
			return m, tea.Quit
		}
		m.quitConfirm = true
		return m, nil
	case "esc":
		m.quitConfirm = false
		m.previewing = false
		m.composing = true
		return m, nil
	case "enter":
		if err := validateAttachments(m.attachments); err != nil {
			m.replyErr = err
			return m, nil
		}
		m.quitConfirm = false
		m.sending = true
		attachments := append([]string(nil), m.attachments...)
		return m, sendReply(m.client, m.draftID, m.draftBody, attachments, m.replyToken)
	default:
		m.quitConfirm = false
	}
	return m, nil
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

func (m Model) startPoll() (tea.Model, tea.Cmd) {
	m.pollToken++
	m.polling = true
	return m, loadInbox(m.client, m.pollToken)
}
