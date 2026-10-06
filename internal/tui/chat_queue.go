package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// The chat send queue lets the composer keep working while earlier
// messages are still being delivered. A WhatsApp send holds the RPC for
// the channel's human-emulation pause (several seconds); instead of
// freezing the chat for that long, every confirmed message becomes an
// entry here, with its own optimistic bubble, and the entries are
// delivered strictly one at a time, in order: the next RPC starts only
// once the previous one has returned. The channel's own pacing is left
// untouched; the queue only stops the TUI from blocking on it.

// chatSendState is where one queued message is in its delivery.
type chatSendState int

const (
	// chatSendQueued waits for the sends ahead of it.
	chatSendQueued chatSendState = iota
	// chatSendInFlight is the one send whose RPC is running.
	chatSendInFlight
	// chatSendDone has its receipt and waits for a reload to show the
	// stored item in its place (K10).
	chatSendDone
	// chatSendFailed was not sent: its own send failed, or one ahead of
	// it in the same conversation did and the queue stopped there.
	chatSendFailed
)

// chatOptimisticMsg is one queued send and its K10 optimistic own
// bubble: body/at are captured at confirm time so the bubble renders
// identically whether the send is still waiting, in flight, failed, or
// done and waiting for the reload to confirm it. id stays empty until the
// real send returns a receipt.
type chatOptimisticMsg struct {
	// seq identifies the entry: its chatReplySentMsg carries it back.
	seq uint64
	// conv is the conversation's chatDraftKey; only the open chat's
	// entries are drawn.
	conv  string
	state chatSendState
	id    string
	body  string
	at    time.Time
	// attachments names the files riding on this send (issue #5), so
	// the optimistic bubble lists them like the stored item will.
	attachments []core.Attachment
	req         chatSendRequest
	// temps are the temporary files (pasted images, recorded notes)
	// this send owns: deleted once it is sent, given back with the
	// draft when it is not.
	temps    []string
	voiceDur time.Duration
}

// pending reports whether e still has to be delivered.
func (e chatOptimisticMsg) pending() bool {
	return e.state == chatSendQueued || e.state == chatSendInFlight
}

func (m Model) chatConvKey() string {
	return chatDraftKey(m.chatChannel, m.chatAccount, m.chatThread)
}

// chatSendBusy reports whether any send is still waiting or in flight.
func (m Model) chatSendBusy() bool {
	return m.chatPendingSends("") > 0
}

// chatPendingSends counts the sends still to be delivered, for conv or,
// when conv is empty, for every conversation.
func (m Model) chatPendingSends(conv string) int {
	n := 0
	for _, e := range m.chatQueue {
		if e.pending() && (conv == "" || e.conv == conv) {
			n++
		}
	}
	return n
}

// chatBubbles is the open chat's queue entries, oldest first.
func (m Model) chatBubbles() []chatOptimisticMsg {
	if !m.chatMode {
		return nil
	}
	conv := m.chatConvKey()
	var out []chatOptimisticMsg
	for _, e := range m.chatQueue {
		if e.conv == conv {
			out = append(out, e)
		}
	}
	return out
}

// ownChatQueue copies the queue before it is changed: Model is a value
// and an earlier copy must not see this one's edits through a shared
// backing array.
func (m Model) ownChatQueue() Model {
	m.chatQueue = append([]chatOptimisticMsg(nil), m.chatQueue...)
	return m
}

// filterChatQueue keeps only the entries keep accepts.
func (m Model) filterChatQueue(keep func(chatOptimisticMsg) bool) Model {
	out := make([]chatOptimisticMsg, 0, len(m.chatQueue))
	for _, e := range m.chatQueue {
		if keep(e) {
			out = append(out, e)
		}
	}
	m.chatQueue = out
	return m
}

// dropSettledChatSends forgets conv's failed entries (their text is
// already back in the composer or the draft) and its done ones (the
// conversation's next load shows the stored items instead).
func (m Model) dropSettledChatSends(conv string) Model {
	return m.filterChatQueue(func(e chatOptimisticMsg) bool {
		return e.conv != conv || e.pending()
	})
}

// enqueueChatSend turns the composer's confirmed draft into a queue
// entry, frees the composer for the next message, and starts delivering
// it unless an earlier send is still in flight.
func (m Model) enqueueChatSend(draft string) (Model, tea.Cmd) {
	conv := m.chatConvKey()
	// A failed send's text is back in this draft: its "no enviado"
	// bubble is replaced by the new one.
	m = m.filterChatQueue(func(e chatOptimisticMsg) bool {
		return e.conv != conv || e.state != chatSendFailed
	})
	m.chatSendSeq++
	e := chatOptimisticMsg{
		seq:         m.chatSendSeq,
		conv:        conv,
		body:        draft,
		at:          m.clock(),
		attachments: attachmentNames(m.chatAttachments),
		req:         m.chatSendRequest(draft),
		temps:       m.chatTempFiles,
		voiceDur:    m.chatVoiceDur,
	}
	if m.chatVoice {
		for i := range e.attachments {
			e.attachments[i].Voice = true
			e.attachments[i].Duration = int((m.chatVoiceDur + 500*time.Millisecond) / time.Second)
		}
	}
	m.chatQueue = append(m.chatQueue, e)
	m.chatQuitArmed = false
	// The entry owns the attachments and their temp files now: the
	// composer starts over empty (clearChatAttachments would delete
	// files the send still needs).
	m.chatAttachments, m.chatTempFiles = nil, nil
	m.chatAttachErr = nil
	m.chatVoice, m.chatVoiceDur = false, 0
	m.chatForward = nil
	m.chatPlan = core.Plan{}
	m.chatSendErr = nil
	m.composer.Reset()
	m = m.resizeChatComposer()
	return m.pumpChatQueue()
}

// pumpChatQueue starts the oldest waiting send, unless one is already in
// flight: sends are delivered one at a time, in order.
func (m Model) pumpChatQueue() (Model, tea.Cmd) {
	for _, e := range m.chatQueue {
		if e.state == chatSendInFlight {
			return m, nil
		}
	}
	for i, e := range m.chatQueue {
		if e.state == chatSendQueued {
			m = m.ownChatQueue()
			m.chatQueue[i].state = chatSendInFlight
			return m, e.req.cmd(m.client, e.seq, false)
		}
	}
	return m, nil
}

// chatQueueIndex finds the entry with seq.
func (m Model) chatQueueIndex(seq uint64) int {
	for i, e := range m.chatQueue {
		if e.seq == seq {
			return i
		}
	}
	return -1
}

// handleChatSent settles the in-flight send msg reports and starts the
// next one.
func (m Model) handleChatSent(msg chatReplySentMsg) (tea.Model, tea.Cmd) {
	i := m.chatQueueIndex(msg.token)
	if i < 0 || m.chatQueue[i].state != chatSendInFlight {
		return m, nil
	}
	if msg.err != nil {
		m = m.failChatSend(i, msg.err)
		return m.pumpChatQueue()
	}
	m = m.ownChatQueue()
	e := &m.chatQueue[i]
	for _, p := range e.temps {
		os.Remove(p)
	}
	e.temps = nil
	e.state = chatSendDone
	e.id = msg.receipt.ID
	var reload tea.Cmd
	if m.chatMode && e.conv == m.chatConvKey() {
		m.chatSendErr = nil
		reload = reloadChatAfterSend(m.client, m.chatChannel, m.chatAccount, m.chatThread, msg.receipt.ID, m.chatToken)
		reload = markQueuedReload(reload)
	} else {
		// No bubble to reconcile: the conversation's next open shows
		// the stored item.
		seq := e.seq
		m = m.filterChatQueue(func(e chatOptimisticMsg) bool { return e.seq != seq })
	}
	m, next := m.pumpChatQueue()
	return m, tea.Batch(reload, next)
}

// markQueuedReload tags a reload as a queued send's, whose token is the
// chat's chatToken rather than chatReplyToken.
func markQueuedReload(cmd tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		msg, _ := cmd().(chatSendReloadMsg)
		msg.queued = true
		return msg
	}
}

// reconcileChatQueue drops the open chat's done entries whose stored
// item the loaded conversation now contains (K10 dedupe): never on the
// mere arrival of a reload, so a stale or short reload never hides the
// only visible copy of a message just sent. Entries still waiting or in
// flight are left alone.
func (m Model) reconcileChatQueue() Model {
	conv := m.chatConvKey()
	return m.filterChatQueue(func(e chatOptimisticMsg) bool {
		return e.conv != conv || e.state != chatSendDone || !chatItemsContainID(m.chatItems, e.id)
	})
}

// failChatSend marks the send at i "no enviado" and stops its
// conversation's queue there: every message still waiting behind it is
// marked "no enviado" too, never sent out of order. No text is lost: the
// failed messages go back into the composer, oldest first, ahead of
// whatever was typed since (or into the conversation's kept draft when
// that chat is no longer open). Nothing is retried automatically.
func (m Model) failChatSend(i int, err error) Model {
	m = m.ownChatQueue()
	conv := m.chatQueue[i].conv
	var failed []chatOptimisticMsg
	for j := i; j < len(m.chatQueue); j++ {
		e := &m.chatQueue[j]
		if e.conv != conv || !e.pending() {
			continue
		}
		e.state = chatSendFailed
		failed = append(failed, *e)
	}
	var texts []string
	for _, e := range failed {
		if strings.TrimSpace(e.body) != "" {
			texts = append(texts, e.body)
		}
	}
	if !m.chatMode || conv != m.chatConvKey() {
		if prev := m.drafts[conv]; strings.TrimSpace(prev) != "" {
			texts = append(texts, prev)
		}
		m, _ = m.keepDraft(conv, strings.Join(texts, "\n"))
		m = m.filterChatQueue(func(e chatOptimisticMsg) bool { return e.conv != conv || e.pending() })
		return m.withFlash("un mensaje no se envió: " + humanError(err) + " · quedó como borrador")
	}
	m.chatSendErr = err
	// A preview or confirm on screen was for the draft without the
	// restored text: cancel it, so what is sent is always what was
	// previewed.
	if m.chatPreviewPending || m.chatConfirm {
		m.chatPreviewPending, m.chatConfirm, m.chatAutoSend = false, false, false
		m.chatPlan = core.Plan{}
		m.chatReplyToken++
	}
	clean := len(m.chatAttachments) == 0 && !m.chatVoice && m.chatForward == nil && m.chatEditID == "" && strings.TrimSpace(m.composer.Value()) == ""
	m = m.restoreChatDraft(texts)
	return m.restoreChatAttachments(failed, clean)
}

// restoreChatDraft puts texts back ahead of the current draft: the
// composer's, or the real draft set aside by an edit or a forward.
func (m Model) restoreChatDraft(texts []string) Model {
	join := func(cur string) string {
		parts := texts
		if strings.TrimSpace(cur) != "" {
			parts = append(append([]string(nil), texts...), cur)
		}
		return strings.Join(parts, "\n")
	}
	switch {
	case len(texts) == 0:
	case m.chatEditID != "":
		m.chatEditDraft = join(m.chatEditDraft)
	case m.chatForward != nil:
		m.chatForward.prevDraft = join(m.chatForward.prevDraft)
	default:
		m.composer.SetValue(join(m.composer.Value()))
		m = m.resizeChatComposer()
	}
	return m
}

// restoreChatAttachments gives the failed sends' attachments back to the
// composer. A lone failed voice note or forward comes back as itself,
// ready to send again when the composer was clean (empty, nothing
// attached, no edit or forward under way); mixed with anything else it
// degrades to plain attachments rather than sending everything as a
// voice note or a forward.
func (m Model) restoreChatAttachments(failed []chatOptimisticMsg, clean bool) Model {
	var with []chatOptimisticMsg
	for _, e := range failed {
		if len(e.req.attachments) > 0 {
			with = append(with, e)
		}
		m.chatAttachments = append(m.chatAttachments, e.req.attachments...)
		m.chatTempFiles = append(m.chatTempFiles, e.temps...)
	}
	if len(with) > 0 && m.chatVoice {
		m.chatVoice, m.chatVoiceDur = false, 0
	}
	if len(failed) == 1 && clean {
		e := failed[0]
		if e.req.voice {
			m.chatVoice, m.chatVoiceDur = true, e.voiceDur
		}
		if e.req.forward {
			m.chatForward = &chatForwardState{}
		}
	}
	return m
}

// pendingSendsNotice tells why the chat cannot be left yet.
func pendingSendsNotice(n int) string {
	if n == 1 {
		return "esperando a que se envíe 1 mensaje…"
	}
	return fmt.Sprintf("esperando a que se envíen %d mensajes…", n)
}

// chatOptimisticStatusText is an optimistic bubble's bottom-right status
// line, replacing the normal "HH:MM" time: "enviando…" while the send
// waits, is in flight or has just succeeded but not yet been confirmed by
// the reload, "no enviado" once it failed (K10).
func chatOptimisticStatusText(e chatOptimisticMsg) string {
	if e.state == chatSendFailed {
		return "no enviado"
	}
	return "enviando…"
}
