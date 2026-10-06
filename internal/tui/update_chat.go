package tui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/kittygfx"
)

// updateChat handles keys while the K5 chat view is open. The composer is
// always active there (there is no separate "not composing" sub-state
// like the mail flow's "r" key): every key is literal draft text except
// Esc (leave), plain Enter (preview→inline confirm, never a newline —
// unlike the mail composer), Alt+Enter (insert a newline instead), and Up
// at the top of the draft (scroll-up pagination, K5's "Scrolling up
// paginates through thread(before=oldest)").
//
// Sends already confirmed never block the composer: they wait in the send
// queue (chat_queue.go) while the user keeps typing and sending. Only the
// short dry-run preview still ignores keys, so the draft cannot change
// between its preview and its send.
func (m Model) updateChat(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.chatPreviewPending {
		return m, nil
	}
	if m.voiceRec != nil {
		return m.updateVoiceRecording(msg)
	}
	if m.chatAction != nil {
		return m.updateChatAction(msg)
	}
	if m.chatLinks != nil {
		return m.updateChatLinks(msg)
	}
	if m.chatConfirm {
		switch msg.String() {
		case "esc":
			m.chatConfirm = false
			// A forwarded voice note is the forward itself: only a
			// second Esc drops it (cancelChatForward).
			if m.chatForward == nil {
				m = m.dropChatVoice()
			}
			return m, nil
		case "enter", "ctrl+s":
			return m.confirmChatSendNow()
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
	case copyKey:
		return m.copySelected()
	case openFileKey:
		return m.openNewestAttachment()
	case voicePlayKey:
		key, ok := m.newestVoiceKey()
		if !ok && m.voicePlay == nil {
			m.mediaErr = errors.New("no hay notas de voz en este chat")
			return m, nil
		}
		return m.startVoicePlay(key)
	case voiceRecordKey:
		return m.startVoiceRecording()
	case chatAskKey:
		return m.askAgent()
	case chatEditKey:
		return m.startChatEdit()
	case callPlaceKey:
		return m.startChatCall()
	case chatDeleteKey:
		return m.startChatDelete()
	case chatReactKey, chatReactKeyAlt:
		return m.startChatReact()
	case chatLinkKey:
		return m.openChatLink()
	case chatForwardKey:
		return m.startChatForward()
	case chatSelectPrevKey:
		return m.moveChatFocus(-1), nil
	case chatSelectNextKey:
		return m.moveChatFocus(+1), nil
	case "esc":
		if m.voicePlay != nil {
			return m.stopVoicePlay(), nil
		}
		if m.chatEditID != "" {
			return m.cancelChatEdit(), nil
		}
		// A forward being composed is dropped before anything else, and
		// Esc stays in the chat it was going to.
		if m.chatForward != nil {
			return m.cancelChatForward(), nil
		}
		// A selection is dropped first, so Esc never leaves the chat
		// while a message is still highlighted.
		if m.chatFocus != "" {
			m.chatFocus = ""
			return m, nil
		}
		// Leaving with sends still queued would hide their outcome and,
		// on a failure, the attachments riding on them: stay until the
		// queue drains (bounded by the send timeout).
		if n := m.chatPendingSends(m.chatConvKey()); n > 0 {
			return m.withFlash(pendingSendsNotice(n)), nil
		}
		if m.openID != "" {
			return m, m.quitOpenChat()
		}
		return m.leaveChat()
	case "alt+enter":
		m.composer.InsertRune('\n')
		return m.resizeChatComposer(), nil
	case "enter", "ctrl+s":
		if m.chatEditID != "" {
			return m.previewChatEdit()
		}
		if m.chatForward != nil && m.chatForward.loading {
			// Sending now would drop the attachment still downloading.
			return m.withFlash("Esperando el adjunto a reenviar…"), nil
		}
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
		// The dry-run always runs first. When one Enter is enough the
		// preview's clean reply sends the draft itself (update.go).
		m.chatAutoSend = m.chatSendsOnOneEnter()
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

// confirmChatSendNow sends the previewed chat draft for real: the second
// Enter of the explicit flow, or the preview's own reply when one Enter is
// enough (chatSendsOnOneEnter). The draft joins the send queue: its
// optimistic bubble shows and the composer clears right away, before the
// real send even starts — the draft is restored only if its send fails
// (failChatSend).
func (m Model) confirmChatSendNow() (Model, tea.Cmd) {
	// K10: clear the confirm state as the send starts, so the tail line
	// shows the send in progress instead of the confirm.
	m.chatConfirm = false
	m.chatAutoSend = false
	if err := validateAttachments(m.chatAttachments); err != nil {
		m.chatSendErr = err
		return m, nil
	}
	return m.enqueueChatSend(m.composer.Value())
}
