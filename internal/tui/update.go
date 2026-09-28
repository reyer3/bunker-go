package tui

import (
	"os"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/kittygfx"
)

// Update is the tea.Model entry point. After every step it also starts
// fetching any image thumbnails the chat view now shows but the media
// cache does not hold yet (a no-op without kitty graphics).
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if _, resized := msg.(tea.WindowSizeMsg); resized {
		// A resize can leave the old frame on screen: a terminal that
		// re-wraps or keeps the previous cells confuses the renderer's
		// line diff. A full clear makes the next frame start clean.
		cmd = tea.Batch(cmd, tea.ClearScreen)
	}
	nm, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	if media := nm.requestChatMedia(); media != nil {
		return nm, tea.Batch(cmd, media)
	}
	return nm, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case clipboardImageMsg:
		if msg.err != nil {
			m.chatAttachErr = msg.err
			return m, nil
		}
		if !m.chatMode {
			os.Remove(msg.path)
			return m, nil
		}
		m.chatTempFiles = append(m.chatTempFiles, msg.path)
		return m.addChatAttachments(msg.path), nil
	case unreadDoneMsg:
		return m.handleUnreadDone(msg)
	case contactsLoadedMsg:
		return m.handleContactsLoaded(msg)
	case mediaReadyMsg:
		return m.handleMediaReady(msg)
	case videoReadyMsg:
		return m.handleVideoReady(msg)
	case videoDoneMsg:
		return m.handleVideoDone(msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case tea.FocusMsg:
		m.blurred = false
	case tea.BlurMsg:
		m.blurred = true
		if m.chatMode {
			return m, leaveChatCmd(m.client, m.chatChannel, m.chatAccount, m.chatThread)
		}
	case chatThreadLoadedMsg:
		if msg.token != m.chatToken || !m.chatMode {
			return m, nil
		}
		m.chatLoading = false
		m.chatLoadErr = msg.itemsErr
		if msg.older {
			if msg.itemsErr == nil {
				m.chatItems = append(append([]core.Item(nil), msg.items...), m.chatItems...)
			}
			return m, nil
		}
		if msg.itemsErr == nil {
			m.chatItems = msg.items
		}
		m.chatPresence = msg.presence
		m.chatPresenceErr = msg.presenceErr
		if msg.readErr == nil && m.unreadOnOpen != "" {
			m = m.rememberRead(m.unreadOnOpen)
			m.unreadOnOpen = ""
		}
		return m, tea.Batch(nextChatKeepalive(m.chatToken), nextChatTypingIdleTick(m.chatToken))
	case chatKeepaliveTickMsg:
		if msg.token != m.chatToken || !m.chatMode {
			return m, nil
		}
		return m, tea.Batch(sendChatKeepalive(m.client, m.chatChannel, m.chatAccount, m.chatThread, !m.blurred), nextChatKeepalive(m.chatToken))
	case chatTypingIdleTickMsg:
		if msg.token != m.chatToken || !m.chatMode {
			return m, nil
		}
		if m.chatTypingOn && m.clock().Sub(m.chatTypingAt) >= typingIdleTimeout {
			m.chatTypingOn = false
			return m, tea.Batch(sendChatTyping(m.client, m.chatChannel, m.chatAccount, m.chatThread, false), nextChatTypingIdleTick(m.chatToken))
		}
		return m, nextChatTypingIdleTick(m.chatToken)
	case downloadResultMsg:
		if msg.token != m.downloadToken || !m.downloadSending {
			return m, nil
		}
		m.downloadSending = false
		if msg.err != nil {
			m.downloadErr = msg.err
			return m, nil
		}
		m.downloadErr = nil
		m.downloadResult = msg.result
		return m, nil
	case chatReplyPreviewMsg:
		if msg.token != m.chatReplyToken || m.chatConfirm {
			return m, nil
		}
		m.chatPreviewPending = false
		if msg.err != nil {
			m.chatSendErr = msg.err
			return m, nil
		}
		m.chatConfirm = true
		m.chatPlan = msg.plan
		m.chatSendErr = nil
		return m, nil
	case threadLoadedMsg:
		if msg.token != m.threadToken || !m.threadMode {
			return m, nil
		}
		m.threadLoading = false
		m.threadLoadErr = msg.itemsErr
		m.threadSeenErr = msg.seenErr
		if msg.seenErr == nil && m.unreadOnOpen != "" {
			m = m.rememberRead(m.unreadOnOpen)
			m.unreadOnOpen = ""
		}
		if msg.itemsErr == nil {
			m.threadItems = msg.items
			m.threadSelected = max(0, len(m.threadItems)-1)
			m.threadExpanded = map[int]bool{m.threadSelected: true}
			m = m.resetThreadScrollToSelected()
			if len(m.threadItems) > 0 {
				return m.fetchThreadBodyIfNeeded(m.threadItems[m.threadSelected].ID)
			}
		}
		return m, nil
	case threadBodyLoadedMsg:
		if msg.token != m.threadToken || !m.threadMode {
			return m, nil
		}
		if m.threadBodyLoading != nil {
			delete(m.threadBodyLoading, msg.id)
		}
		if msg.err != nil {
			m.threadBodyErr = msg.err
			return m, nil
		}
		if m.threadBodies == nil {
			m.threadBodies = map[string]string{}
		}
		m.threadBodies[msg.id] = msg.body
		m.threadBodyErr = nil
		return m, nil
	case mailPreviewMsg:
		if msg.token != m.mailToken || !m.mailComposing || m.mailPreviewing {
			return m, nil
		}
		if msg.err != nil {
			m.mailSendErr = msg.err
			return m, nil
		}
		m.mailPreviewing = true
		m.mailPlan = msg.plan
		m.mailSendErr = nil
		return m, nil
	case mailSentMsg:
		if msg.token != m.mailToken || !m.mailSending {
			return m, nil
		}
		m.mailSending = false
		if msg.err != nil {
			m.mailSendErr = msg.err
			m.mailPreviewing = false
			return m, nil
		}
		delete(m.mailDrafts, mailDraftKey(m.mailAction, m.mailTargetID))
		m.mailComposing = false
		m.mailPreviewing = false
		m.mailPlan = core.Plan{}
		m.mailSendErr = nil
		return m, nil
	case chatReplySentMsg:
		if msg.token != m.chatReplyToken || !m.chatSending {
			return m, nil
		}
		m.chatSending = false
		if msg.err != nil {
			m.chatSendErr = msg.err
			m.chatConfirm = false
			if m.chatOptimistic != nil {
				// K10: keep the bubble visible, marked "no enviado", and
				// restore the draft the optimistic send already cleared
				// from the composer — never auto-retry.
				m.chatOptimistic.failed = true
				m.composer.SetValue(m.chatOptimistic.body)
				m = m.resizeChatComposer()
			}
			return m, nil
		}
		m.chatConfirm = false
		m.chatPlan = core.Plan{}
		m.chatSendErr = nil
		m = m.clearChatAttachments()
		if m.chatOptimistic != nil {
			m.chatOptimistic.id = msg.receipt.ID
		}
		return m, reloadChatAfterSend(m.client, m.chatChannel, m.chatAccount, m.chatThread, msg.receipt.ID, m.chatReplyToken)
	case chatSendReloadMsg:
		if msg.token != m.chatReplyToken {
			return m, nil
		}
		if msg.err == nil {
			m.chatItems = msg.items
			m.chatScroll = 0
		}
		// K10 dedupe: the optimistic bubble is dropped only once the
		// reloaded thread actually contains the stored FromMe item
		// (matched by the send's own receipt ID) — never on the mere
		// arrival of the reload, so a stale/short reload never hides the
		// only visible copy of the message just sent.
		if m.chatOptimistic != nil && chatItemsContainID(m.chatItems, m.chatOptimistic.id) {
			m.chatOptimistic = nil
		}
		return m, nil
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
		delete(m.drafts, replyDraftKey(m.draftID))
		m.composing = false
		m.previewing = false
		m.draftID = ""
		m.composer.Reset()
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
		m = m.rememberRead(m.markID)
		m.marking = false
		m.markID = ""
		m.markPlan = core.Plan{}
		m.markErr = nil
		return m.withFlash("marcado como leído · u deshacer"), nil
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
		m.loadedAt = m.clock()
		m.adapterHealth = msg.health
		if len(msg.items) > inboxLimit {
			msg.items = msg.items[:inboxLimit]
		}
		oldGroups := m.groups
		m.groups = groupUnread(msg.items)
		m.counts = msg.counts
		if visible := len(m.visibleRows()); m.selected >= visible {
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
			return m.updateHelp(msg.String()), nil
		}
		if msg.String() == "f1" {
			return m.openHelp(), nil
		}
		if m.picker != nil {
			return m.updatePicker(msg)
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
		if m.downloadActive {
			return m.updateDownload(msg)
		}
		if m.viewer != nil {
			return m.updateViewer(msg)
		}
		if m.chatMode {
			return m.updateChat(msg)
		}
		if m.mailComposing {
			return m.updateMailEditor(msg)
		}
		if m.threadMode {
			return m.updateThread(msg)
		}
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "?":
			m = m.openHelp()
		case "esc":
			if m.detail {
				m.detail = false
				m.reading = false
				m.readErr = nil
				m.readItem = core.Item{}
				m.readToken++
				m.detailScroll = 0
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
			if !m.detail {
				if m.selected < len(m.visibleRows())-1 {
					m.selected++
				}
				break
			}
			m.detailScroll = clampScroll(m.detailScroll+1, len(m.detailBodyLines(m.readItem)), m.detailScrollBudget())
		case "k", "up":
			if !m.detail {
				if m.selected > 0 {
					m.selected--
				}
				break
			}
			m.detailScroll = clampScroll(m.detailScroll-1, len(m.detailBodyLines(m.readItem)), m.detailScrollBudget())
		case "pgdown":
			if m.detail {
				budget := m.detailScrollBudget()
				m.detailScroll = clampScroll(m.detailScroll+budget, len(m.detailBodyLines(m.readItem)), budget)
			}
		case "pgup":
			if m.detail {
				budget := m.detailScrollBudget()
				m.detailScroll = clampScroll(m.detailScroll-budget, len(m.detailBodyLines(m.readItem)), budget)
			}
		case "G":
			// Lowercase "g" already means "refresh the inbox" everywhere,
			// including while a detail view is open (the case above), so it
			// keeps that meaning here instead of being repurposed as
			// "scroll to top" — only the otherwise-unbound "G" scrolls to
			// the bottom, vim-style.
			if m.detail {
				budget := m.detailScrollBudget()
				total := len(m.detailBodyLines(m.readItem))
				m.detailScroll = clampScroll(total, total, budget)
			}
		case "right":
			if !m.detail {
				m = m.expandRight()
			}
		case "left":
			if !m.detail {
				m = m.collapseLeft()
			}
		case "r":
			if id, ok := m.selectedItemID(); ok && m.client != nil {
				m.composing = true
				m.draftID = id
				m.composer = newComposer(m.width, m.renderer())
				if draft, ok := m.drafts[replyDraftKey(id)]; ok {
					m.composer.SetValue(draft)
				}
				m.attachments = nil
				m.attaching = false
				m.attachInput = ""
				m.replyErr = nil
				m.previewPlan = core.Plan{}
			}
		case "n":
			if !m.detail {
				return m.openPicker()
			}
		case "u":
			if !m.detail {
				return m.undoRead()
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

// updateCompose handles keys while drafting a reply. Every key is literal
// text except the handful of compose control keys; this is what lets "q"
// and "r" be typed into the draft instead of triggering quit/reply again.
func (m Model) updateCompose(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if next, kept := m.keepDraft(replyDraftKey(m.draftID), m.composer.Value()); kept {
			m = next.withFlash("borrador guardado")
		} else {
			m = next
		}
		m.composing = false
		m.draftID = ""
		m.composer.Reset()
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
		return m, previewReply(m.client, m.draftID, m.composer.Value(), attachments, m.replyToken)
	case "pgdown", "pgup":
		// Text-input keys win in compose (j/k and the plain arrows already
		// reach the composer below as cursor movement, which auto-scrolls
		// it): PgUp/PgDown are the only scroll keys that need explicit
		// handling here, since bubbles/textarea binds neither by default.
		// There is no public API to move its internal viewport without
		// moving the cursor, so a "page" is composerHeight CursorUp/
		// CursorDown steps — the same movement the up/down arrows already
		// do, just composerHeight of them at once.
		for i := 0; i < composerHeight; i++ {
			if msg.String() == "pgdown" {
				m.composer.CursorDown()
			} else {
				m.composer.CursorUp()
			}
		}
		return m, nil
	}
	// Every other key (including "enter" for a newline, arrows/Home/End
	// for cursor movement, backspace/delete, and paste) is handled by the
	// shared bubbles/textarea composer itself (see composer.go), which is
	// what lets "q"/"r" stay literal draft text while still supporting
	// real cursor positioning instead of only ever appending at the end.
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	return m, cmd
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
		return m, sendReply(m.client, m.draftID, m.composer.Value(), attachments, m.replyToken)
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

// updateChat handles keys while the K5 chat view is open. The composer is
// always active there (there is no separate "not composing" sub-state
// like the mail flow's "r" key): every key is literal draft text except
// Esc (leave), plain Enter (preview→inline confirm, never a newline —
// unlike the mail composer), Alt+Enter (insert a newline instead), and Up
// at the top of the draft (scroll-up pagination, K5's "Scrolling up
// paginates through thread(before=oldest)").
func (m Model) updateChat(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.chatSending || m.chatPreviewPending {
		return m, nil
	}
	if m.chatConfirm {
		switch msg.String() {
		case "esc":
			m.chatConfirm = false
			return m, nil
		case "enter":
			draft := m.composer.Value()
			// K10: clear the confirm state as the send starts (it was
			// previously left true for the whole in-flight send, which
			// left chatTailLines' "Enviando..." case dead code, since its
			// switch checks chatConfirm first) so the tail line actually
			// shows the send-in-progress state.
			m.chatConfirm = false
			m.chatSending = true
			m.chatReplyToken++
			// Show the optimistic own bubble and clear the composer right
			// away, before the real send even returns — the draft is
			// restored only if chatReplySentMsg comes back with an error
			// (see its handler above).
			m.chatOptimistic = &chatOptimisticMsg{body: draft, at: m.clock()}
			m.composer.Reset()
			m = m.resizeChatComposer()
			if err := validateAttachments(m.chatAttachments); err != nil {
				m.chatSending = false
				m.chatOptimistic = nil
				m.composer.SetValue(draft)
				m.chatSendErr = err
				return m.resizeChatComposer(), nil
			}
			m.chatOptimistic.attachments = attachmentNames(m.chatAttachments)
			return m, m.chatSendCmd(draft, false)
		}
		return m, nil
	}
	if next, ok := m.updateEmojiCompletion(msg); ok {
		return next, nil
	}
	if msg.Paste {
		// Issue #5: files dropped on the terminal arrive as a pasted
		// list of their paths; attach them instead of typing the paths.
		if paths, ok := parseDroppedPaths(string(msg.Runes)); ok {
			return m.addChatAttachments(paths...), nil
		}
	}
	switch msg.String() {
	case "esc":
		return m.leaveChat()
	case "alt+enter":
		m.composer.InsertRune('\n')
		return m.resizeChatComposer(), nil
	case "enter":
		body := strings.TrimSpace(m.composer.Value())
		if body == "" && len(m.chatAttachments) == 0 {
			return m, nil
		}
		if err := validateAttachments(m.chatAttachments); err != nil {
			m.chatSendErr = err
			return m, nil
		}
		m.chatSendErr = nil
		m.chatReplyToken++
		m.chatPreviewPending = true
		return m, m.chatSendCmd(m.composer.Value(), true)
	case "ctrl+v":
		// Issue #5: paste an image (e.g. a screenshot) from the clipboard
		// as an attachment. Text is pasted by the terminal itself
		// (Ctrl+Shift+V), which arrives as a bracketed paste instead.
		if m.clipboard == nil {
			return m, nil
		}
		return m, pasteClipboardImageCmd(m.clipboard)
	case "backspace":
		if m.composer.Value() == "" && len(m.chatAttachments) > 0 {
			return m.removeLastChatAttachment(), nil
		}
	case "up":
		if m.composer.Line() == 0 && !m.chatLoading && len(m.chatItems) > 0 {
			return m.loadOlderChat()
		}
	case "pgup":
		return m.scrollChatUp(m.chatScrollBudget())
	case "pgdown":
		m.chatScroll = clampScroll(m.chatScroll-m.chatScrollBudget(), len(m.chatBodyLines()), m.chatScrollBudget())
		return m, nil
	case "ctrl+o":
		// Open the newest image or video full size (issues #4/#6): like
		// Ctrl+D, a control key, since the composer has focus. Without
		// kitty graphics there is no viewer, but the newest video still
		// plays (in mpv's own window).
		if m.gfx != kittygfx.Kitty {
			if key, ok := m.newestVideoKey(); ok {
				return m, m.playVideo(key)
			}
			return m, nil
		}
		return m.openViewer("")
	case "ctrl+d":
		// The composer always has focus in the chat view, so a plain "d"
		// is text ("de acuerdo"); download the newest attachment on
		// Ctrl+D instead.
		if item, ok := m.chatDownloadCandidate(); ok {
			return m.openDownload(item.ID, item.Attachments)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	m = m.resizeChatComposer()
	// The typing notification takes priority over any (rare, currently
	// always nil in this static-cursor configuration) command the
	// textarea itself returns: see typeRunes's doc comment for why only
	// one command per keystroke is forwarded in tests.
	m, typingCmd := m.noteChatTyping()
	if typingCmd != nil {
		return m, typingCmd
	}
	return m, cmd
}

// resizeChatComposer grows the K7 docked composer up to
// chatComposerMaxHeight lines as the draft gains lines, and shrinks it
// back down (e.g. after Reset() clears a sent draft) — never below 1.
// bubbles/textarea does not do this on its own: Height is a fixed
// viewport that only ever scrolls internally past it (see
// newChatComposer's doc comment).
func (m Model) resizeChatComposer() Model {
	n := m.composer.LineCount()
	if n < 1 {
		n = 1
	}
	if n > chatComposerMaxHeight {
		n = chatComposerMaxHeight
	}
	m.composer.SetHeight(n)
	return m
}

// noteChatTyping applies the ≤once-per-5s typing throttle: it only
// returns a non-nil command (and only then updates chatTypingAt) when
// enough time passed since the last composing=true it sent.
func (m Model) noteChatTyping() (Model, tea.Cmd) {
	now := m.clock()
	if m.chatTypingOn && now.Sub(m.chatTypingAt) < typingThrottle {
		return m, nil
	}
	m.chatTypingOn = true
	m.chatTypingAt = now
	return m, sendChatTyping(m.client, m.chatChannel, m.chatAccount, m.chatThread, true)
}

// loadOlderChat requests one older page, keyed off the oldest currently
// loaded item's timestamp.
func (m Model) loadOlderChat() (tea.Model, tea.Cmd) {
	if len(m.chatItems) == 0 {
		return m, nil
	}
	m.chatLoading = true
	before := m.chatItems[0].Timestamp
	return m, loadOlderChatThread(m.client, m.chatChannel, m.chatAccount, m.chatThread, before, m.chatToken)
}

// chatWheelScroll is how many lines a single mouse wheel tick scrolls the
// chat body (PgUp/PgDown instead scroll by a full viewport page — see
// scrollChatUp/chatScrollBudget). The mail thread and plain detail views
// reuse it too (mouse.go): all three windows share the same line-index
// clampScroll contract, so one shared "how many lines per tick" constant
// keeps their feel consistent.
const chatWheelScroll = 3

// composeWheelScroll is how many bubbles/textarea CursorUp/CursorDown
// steps a single mouse wheel tick moves in the K4 reply composer — the
// same unit PgUp/PgDown use there (composerHeight steps instead of 3),
// since the composer has no line-index scroll offset of its own to share
// chatWheelScroll's contract with (see updateCompose/updateComposeMouse).
const composeWheelScroll = 3

// scrollChatUp scrolls the chat body up by amount lines (clamped to the
// oldest currently loaded line). If it is already scrolled as far up as
// the loaded content allows, it requests an older page instead — the
// same "reaching the top loads more" contract the plain "Up" key already
// had, now shared with PgUp and the mouse wheel.
func (m Model) scrollChatUp(amount int) (tea.Model, tea.Cmd) {
	budget := m.chatScrollBudget()
	total := len(m.chatBodyLines())
	maxScroll := total - budget
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.chatScroll >= maxScroll && !m.chatLoading && len(m.chatItems) > 0 {
		return m.loadOlderChat()
	}
	m.chatScroll = clampScroll(m.chatScroll+amount, total, budget)
	return m, nil
}

// updateThread handles keys in the K6 mail thread view: j/k/up/down move
// the selection, Enter toggles the selected message's collapsed/expanded
// state, r/R/f open the full editor on the selected message, and Esc
// leaves the thread view.
func (m Model) updateThread(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "?":
		return m.openHelp(), nil
	case "esc":
		m.detail = false
		m.threadMode = false
		return m, nil
	case "enter":
		if len(m.threadItems) == 0 {
			return m, nil
		}
		if m.threadExpanded == nil {
			m.threadExpanded = map[int]bool{}
		}
		nowExpanded := !m.threadExpanded[m.threadSelected]
		m.threadExpanded[m.threadSelected] = nowExpanded
		m = m.resetThreadScrollToSelected()
		if nowExpanded {
			return m.fetchThreadBodyIfNeeded(m.threadItems[m.threadSelected].ID)
		}
		return m, nil
	case "j", "down":
		if m.threadSelected < len(m.threadItems)-1 {
			m.threadSelected++
			m = m.resetThreadScrollToSelected()
		}
	case "k", "up":
		if m.threadSelected > 0 {
			m.threadSelected--
			m = m.resetThreadScrollToSelected()
		}
	case "pgup":
		budget := m.threadScrollBudget()
		m.threadScroll = clampScroll(m.threadScroll-budget, m.threadBodyLen(), budget)
	case "pgdown":
		budget := m.threadScrollBudget()
		m.threadScroll = clampScroll(m.threadScroll+budget, m.threadBodyLen(), budget)
	case "r":
		return m.openMailEditor("reply")
	case "R":
		return m.openMailEditor("replyAll")
	case "f":
		return m.openMailEditor("forward")
	case "d":
		if m.threadSelected >= 0 && m.threadSelected < len(m.threadItems) {
			item := m.threadItems[m.threadSelected]
			if len(item.Attachments) > 0 {
				return m.openDownload(item.ID, item.Attachments)
			}
		}
	}
	return m, nil
}

// openMailEditor opens K6's full To/Cc/Subject editor on the currently
// selected thread message, prefilled per action: "reply" (the sender),
// "replyAll" (every participant excluding our own address, see
// mailSelfAddress) or "forward" (empty To, the original's attachments
// listed informationally — re-attaching them needs a download first,
// which is not implemented here; see the K6 commit's disclosure).
func (m Model) openMailEditor(action string) (tea.Model, tea.Cmd) {
	if m.threadSelected < 0 || m.threadSelected >= len(m.threadItems) {
		return m, nil
	}
	item := m.threadItems[m.threadSelected]
	m.mailComposing = true
	m.mailAction = action
	m.mailTargetID = item.ID
	m.mailChannel = item.Channel
	m.mailAccount = item.Account
	m.mailThread = item.Thread
	m.mailTo = newLineEditor()
	m.mailCc = newLineEditor()
	m.mailSubject = newLineEditor()
	m.mailAttachInfo = nil
	m.mailPreviewing = false
	m.mailSending = false
	m.mailSendErr = nil
	m.mailPlan = core.Plan{}
	m.composer = newComposer(m.width, m.renderer())
	m.mailFocus = 3

	switch action {
	case "reply":
		m.mailTo.SetValue(item.From.ID)
		m.mailSubject.SetValue(subjectWithPrefix(item.Subject, "Re: "))
	case "replyAll":
		self := mailSelfAddress(m.threadItems)
		m.mailTo.SetValue(strings.Join(replyAllRecipients(item, self), ", "))
		m.mailSubject.SetValue(subjectWithPrefix(item.Subject, "Re: "))
	case "forward":
		m.mailSubject.SetValue(subjectWithPrefix(item.Subject, "Fwd: "))
		m.mailAttachInfo = item.Attachments
		m.mailFocus = 0
	}
	m.composer.SetValue("\n\n" + quoteOriginal(item))
	m.composer.CursorStart()
	m = m.restoreMailDraft()
	m = m.withMailFocusApplied()
	return m, nil
}

// withMailFocusApplied blurs every editor field and focuses only the one
// mailFocus names (0=To, 1=Cc, 2=Subject, else the composer/body).
func (m Model) withMailFocusApplied() Model {
	m.mailTo.Blur()
	m.mailCc.Blur()
	m.mailSubject.Blur()
	m.composer.Blur()
	switch m.mailFocus {
	case 0:
		m.mailTo.Focus()
	case 1:
		m.mailCc.Focus()
	case 2:
		m.mailSubject.Focus()
	default:
		m.composer.Focus()
	}
	return m
}

// buildMailOutgoing assembles the editor's fields into the core.Outgoing
// Send needs.
func (m Model) buildMailOutgoing() core.Outgoing {
	return core.Outgoing{
		Channel: m.mailChannel,
		Account: m.mailAccount,
		To:      splitRecipients(m.mailTo.Value()),
		Cc:      splitRecipients(m.mailCc.Value()),
		Thread:  m.mailThread,
		ReplyTo: m.mailTargetID,
		Subject: m.mailSubject.Value(),
		Body:    m.composer.Value(),
	}
}

// updateMailEditor handles keys in K6's full editor: Tab/Shift+Tab cycle
// To/Cc/Subject/body focus, Ctrl+S requests a dry-run preview, Esc
// discards the draft and closes the editor, and every other key goes to
// whichever field is focused.
func (m Model) updateMailEditor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mailPreviewing {
		return m.updateMailPreview(msg)
	}
	switch msg.String() {
	case "esc":
		if m.mailDrafts == nil {
			m.mailDrafts = map[string]mailDraft{}
		}
		m.mailDrafts[mailDraftKey(m.mailAction, m.mailTargetID)] = mailDraft{to: m.mailTo.Value(), cc: m.mailCc.Value(), subject: m.mailSubject.Value(), body: m.composer.Value()}
		m.mailComposing = false
		return m.withFlash("borrador guardado"), nil
	case "tab":
		m.mailFocus = (m.mailFocus + 1) % 4
		m = m.withMailFocusApplied()
		return m, nil
	case "shift+tab":
		m.mailFocus = (m.mailFocus - 1 + 4) % 4
		m = m.withMailFocusApplied()
		return m, nil
	case "ctrl+s":
		out := m.buildMailOutgoing()
		m.mailSendErr = nil
		m.mailToken++
		return m, previewMailSend(m.client, out, m.mailToken)
	}
	var cmd tea.Cmd
	switch m.mailFocus {
	case 0:
		m.mailTo, cmd = m.mailTo.Update(msg)
	case 1:
		m.mailCc, cmd = m.mailCc.Update(msg)
	case 2:
		m.mailSubject, cmd = m.mailSubject.Update(msg)
	default:
		m.composer, cmd = m.composer.Update(msg)
	}
	return m, cmd
}

// updateMailPreview handles keys once the editor's dry-run preview is
// showing: Enter is the only way to send for real, guarded against a
// second send while one is in flight; Esc returns to editing without
// ever sending.
func (m Model) updateMailPreview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mailSending {
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.mailPreviewing = false
		return m, nil
	case "enter":
		m.mailSending = true
		out := m.buildMailOutgoing()
		return m, sendMailSend(m.client, out, m.mailToken)
	}
	return m, nil
}

func (m Model) startPoll() (tea.Model, tea.Cmd) {
	m.pollToken++
	m.polling = true
	return m, loadInbox(m.client, m.pollToken)
}
