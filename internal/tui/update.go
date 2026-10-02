package tui

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
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
	case callsLoadedMsg:
		return m.handleCallsLoaded(msg)
	case callTickMsg:
		return m.handleCallTick()
	case callControlDoneMsg:
		return m.handleCallControlDone(msg)
	case callNotifiedMsg:
		return m.handleCallNotified(msg)
	case chatActionPlanMsg:
		return m.handleChatActionPlan(msg)
	case chatActionDoneMsg:
		return m.handleChatActionDone(msg)
	case queryDebounceMsg:
		return m.handleQueryDebounce(msg)
	case queryPageMsg:
		return m.handleQueryPage(msg)
	case unreadDoneMsg:
		return m.handleUnreadDone(msg)
	case openItemLoadedMsg:
		return m.handleOpenItemLoaded(msg)
	case externalOpenDoneMsg:
		return m.handleExternalOpenDone(msg)
	case agentAskDoneMsg:
		return m.handleAgentAskDone(msg)
	case unreadReportedMsg:
		return m.handleUnreadReported(msg)
	case messageNotifiedMsg:
		return m.handleMessageNotified(msg)
	case contactsLoadedMsg:
		return m.handleContactsLoaded(msg)
	case paletteContactsMsg:
		return m.handlePaletteContacts(msg)
	case mediaReadyMsg:
		return m.handleMediaReady(msg)
	case videoReadyMsg:
		return m.handleVideoReady(msg)
	case videoDoneMsg:
		return m.handleVideoDone(msg)
	case voicePlayReadyMsg:
		return m.handleVoicePlayReady(msg)
	case voicePlayDoneMsg:
		return m.handleVoicePlayDone(msg)
	case voiceTickMsg:
		return m.handleVoiceTick(msg)
	case voiceExitedMsg:
		return m.handleVoiceExited(msg)
	case voiceStoppedMsg:
		return m.handleVoiceStopped(msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// The composer's width is fixed when it is built, which in a
		// "bunker open" pane happens before the first size arrives
		// (composerFallbackWidth): follow every resize instead.
		// Only a composer in use is resized: the zero textarea panics.
		switch {
		case msg.Width <= 0:
		case m.chatMode:
			m.composer.SetWidth(chatComposerWidth(msg.Width))
			m = m.resizeChatComposer()
		case m.composing || m.mailComposing:
			m.composer.SetWidth(msg.Width)
		}
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
		return m, tea.Batch(nextChatKeepalive(m.chatToken), nextChatTypingIdleTick(m.chatToken), nextChatRefresh(m.chatToken))
	case chatRefreshTickMsg:
		return m.handleChatRefreshTick(msg)
	case chatRefreshedMsg:
		return m.handleChatRefreshed(msg)
	case chatRefreshReadMsg:
		return m.handleChatRefreshRead(msg)
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
		autoSend := m.chatAutoSend
		m.chatAutoSend = false
		if msg.err != nil {
			m.chatSendErr = msg.err
			// A recorded note that cannot be sent is not worth keeping.
			m = m.dropChatVoice()
			return m, nil
		}
		if autoSend {
			// A clean dry-run on a plain text reply: send it now instead
			// of waiting for a second Enter.
			m.chatPlan = msg.plan
			m.chatSendErr = nil
			return m.confirmChatSendNow()
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
		m.chats = mergeChatLists(m.chats, msg.chats)
		m.counts = msg.counts
		if visible := len(m.visibleRows()); m.selected >= visible {
			m.selected = max(0, visible-1)
		}
		var notifyCmd tea.Cmd
		m, notifyCmd = m.maybeNotify(wasLoaded, oldGroups, msg.items)
		var updateCmd tea.Cmd
		m, updateCmd = m.noteUpdate(msg.update)
		var callsCmd tea.Cmd
		m, callsCmd = m.startCalls()
		return m, tea.Batch(nextPoll(m.pollToken), notifyCmd, updateCmd, m.reportUnread(), callsCmd)
	case pollTickMsg:
		if msg.token != m.pollToken || m.polling || m.client == nil {
			return m, nil
		}
		return m.startPoll()
	case copyDoneMsg:
		return m.handleCopyDone(msg)
	case openAttachMsg:
		return m.handleOpenAttach(msg)
	case tea.KeyMsg:
		if next, cmd, ok := m.selectModeToggle(msg.String()); ok {
			return next, cmd
		}
		if m.helpOpen {
			return m.updateHelp(msg.String()), nil
		}
		if msg.String() == "f1" {
			return m.openHelp(), nil
		}
		if next, cmd, ok := m.callKey(msg.String()); ok {
			return next, cmd
		}
		if m.palette != nil {
			return m.updatePalette(msg)
		}
		if m.canOpenPalette(msg.String()) {
			return m.openPalette(), nil
		}
		if m.openPending() {
			return m.updateOpenPending(msg)
		}
		if m.picker != nil {
			return m.updatePicker(msg)
		}
		if m.filtering {
			return m.updateFilter(msg)
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
		case copyKey:
			if m.detail {
				return m.copySelected()
			}
		case "q":
			return m, tea.Quit
		case "?":
			m = m.openHelp()
		case "/":
			if !m.detail {
				return m.startFilter(), nil
			}
		case "esc":
			if !m.detail && m.filterQuery != "" {
				return m.clearFilter(), nil
			}
			if m.detail && m.openID != "" {
				return m, tea.Quit
			}
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
				return m.maybeLoadMore()
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
		case "@":
			if !m.detail {
				return m.openSearchPicker()
			}
		case "c":
			if !m.detail {
				return m.callSelectedRow()
			}
		case "u":
			if !m.detail {
				return m.undoRead()
			}
		case "a":
			if next, cmd, ok := m.plainCallKey("a"); ok {
				return next, cmd
			}
			return m.askAgent()
		case "x", "h":
			return m.plainCallKeyOrNothing(msg.String())
		case "m":
			// A read chat-list row has nothing to mark.
			if id, ok := m.selectedItemID(); ok && m.client != nil && (m.detail || m.selectedRowUnread()) {
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

func (m Model) startPoll() (tea.Model, tea.Cmd) {
	m.pollToken++
	m.polling = true
	return m, loadInbox(m.client, m.pollToken)
}
