package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

const (
	// threadPageLimit is the Thread RPC's default page size
	// (conversation-view.md's contract).
	threadPageLimit = 50
	// presenceKeepaliveInterval is how often the TUI renews the
	// availability lease while a chat view is open and focused; the
	// contract's daemon-side lease expires after 60s without one.
	presenceKeepaliveInterval = 20 * time.Second
	// typingThrottle bounds how often composing=true is sent while
	// typing continuously.
	typingThrottle = 5 * time.Second
	// typingIdleTimeout is how long without a keystroke before
	// composing=false is sent.
	typingIdleTimeout = 5 * time.Second
)

// chatThreadLoadedMsg carries both the initial open (older=false, which
// also carries the mark-read and presence results) and a scroll-up
// pagination page (older=true, ReadErr/Presence unset). Aggregating the
// open's three RPC calls into one message follows the same pattern
// loadInbox already uses for List+Counts.
type chatThreadLoadedMsg struct {
	token       uint64
	older       bool
	items       []core.Item
	itemsErr    error
	readErr     error
	presence    core.Presence
	presenceErr error
}

// openChatCmd performs every RPC an open needs — the newest page of the
// conversation, marking the WHOLE conversation read with a receipt via
// ReadThread (K9's fix: the old Read(id, true) marked only the newest
// item, leaving older unread items stranded), and its current presence —
// sequentially in one command, and fires the initial
// keepalive(focused=true) best-effort alongside them.
func openChatCmd(client Client, channel core.Channel, account, thread string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		items, itemsErr := client.Thread(ctx, string(channel), account, thread, time.Time{}, threadPageLimit)
		_, readErr := client.ReadThread(ctx, string(channel), account, thread, true)
		presence, presenceErr := client.Presence(ctx, string(channel), account, thread)
		_ = client.PresenceKeepalive(ctx, string(channel), account, thread, true)
		return chatThreadLoadedMsg{token: token, items: items, itemsErr: itemsErr, readErr: readErr, presence: presence, presenceErr: presenceErr}
	}
}

// loadOlderChatThread requests one older page for scroll-up pagination.
func loadOlderChatThread(client Client, channel core.Channel, account, thread string, before time.Time, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		items, err := client.Thread(ctx, string(channel), account, thread, before, threadPageLimit)
		return chatThreadLoadedMsg{token: token, items: items, itemsErr: err, older: true}
	}
}

// leaveChatCmd reports focused=false and composing=false best-effort when
// the chat view closes (Esc/quit/blur); the daemon's own 60s lease
// timeout is the safety net if this never arrives (conversation-view.md).
func leaveChatCmd(client Client, channel core.Channel, account, thread string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), previewTimeout)
		defer cancel()
		_ = client.PresenceKeepalive(ctx, string(channel), account, thread, false)
		_ = client.Typing(ctx, string(channel), account, thread, false)
		return nil
	}
}

// sendChatKeepalive renews the availability lease. It is fire-and-forget:
// a failure just means the daemon may go unavailable a little early
// (before the full 60s), it never blocks the chat view.
func sendChatKeepalive(client Client, channel core.Channel, account, thread string, focused bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), previewTimeout)
		defer cancel()
		_ = client.PresenceKeepalive(ctx, string(channel), account, thread, focused)
		return nil
	}
}

// sendChatTyping is fire-and-forget: a failure to report typing state
// degrades the other side's presence display, it never blocks the chat.
func sendChatTyping(client Client, channel core.Channel, account, thread string, composing bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), previewTimeout)
		defer cancel()
		_ = client.Typing(ctx, string(channel), account, thread, composing)
		return nil
	}
}

type chatKeepaliveTickMsg struct{ token uint64 }

func nextChatKeepalive(token uint64) tea.Cmd {
	return tea.Tick(presenceKeepaliveInterval, func(time.Time) tea.Msg { return chatKeepaliveTickMsg{token: token} })
}

type chatTypingIdleTickMsg struct{ token uint64 }

func nextChatTypingIdleTick(token uint64) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return chatTypingIdleTickMsg{token: token} })
}

// chatReplyPreviewMsg/chatReplySentMsg mirror replyPreviewMsg/replySentMsg
// (mail) but drive the chat view's inline confirm instead of the mail
// composer's full preview screen.
type chatReplyPreviewMsg struct {
	token uint64
	plan  core.Plan
	err   error
}

// chatReplySentMsg's token is the seq of the queue entry it delivered
// (chat_queue.go), not chatReplyToken: previews keep bumping that one
// while earlier sends are still in flight.
type chatReplySentMsg struct {
	token   uint64
	receipt core.Receipt
	err     error
}

// attachmentNames describes local attachment paths the way a stored
// item lists its attachments (name and size), for the optimistic bubble.
func attachmentNames(paths []string) []core.Attachment {
	out := make([]core.Attachment, 0, len(paths))
	for _, p := range paths {
		name, size, err := statAttachment(p)
		if err != nil {
			continue
		}
		out = append(out, core.Attachment{Name: name, Size: size})
	}
	return out
}

// chatSendReloadMsg carries the K10 post-send reload: once a chat send
// succeeds, the thread is reloaded (newest page) so the stored FromMe
// item (K7b) appears in place of the optimistic bubble, matched by
// receiptID.
type chatSendReloadMsg struct {
	token uint64
	// queued marks the reload after a queued send (chat_queue.go): its
	// token is the chat's own chatToken, since chatReplyToken keeps
	// moving with every preview.
	queued    bool
	receiptID string
	items     []core.Item
	err       error
}

// chatSendRequest is everything one chat send needs, captured from the
// chat view when the draft is previewed or confirmed. A confirmed send
// waits in the send queue (chat_queue.go) while the composer moves on to
// the next message, so it must never read the composer, the attachments
// or the forward state again once captured.
type chatSendRequest struct {
	channel         core.Channel
	account, thread string
	// draftID is the item a reply quotes; newTo the address of a chat
	// opened from the contact picker (no item to reply to).
	draftID, newTo string
	forward        bool
	body           string
	attachments    []string
	voice          bool
}

// chatSendRequest captures the open chat's current send state for body.
func (m Model) chatSendRequest(body string) chatSendRequest {
	return chatSendRequest{
		channel:     m.chatChannel,
		account:     m.chatAccount,
		thread:      m.chatThread,
		draftID:     m.chatDraftID,
		newTo:       m.chatNewTo,
		forward:     m.chatForward != nil,
		body:        body,
		attachments: append([]string(nil), m.chatAttachments...),
		voice:       m.chatVoice,
	}
}

// chatSendCmd previews (dryRun) or sends the chat draft right now, with
// the preview token (chatReplyToken).
func (m Model) chatSendCmd(body string, dryRun bool) tea.Cmd {
	return m.chatSendRequest(body).cmd(m.client, m.chatReplyToken, dryRun)
}

// cmd previews (dryRun) or sends r: a reply to the conversation's newest
// item, or, for a chat opened from the contact picker (no item to reply
// to) and for a forward, a fresh send to its thread and address. token
// is the preview token for a dry-run, and the queue entry's seq for a
// real send (chatReplySentMsg.token).
func (r chatSendRequest) cmd(client Client, token uint64, dryRun bool) tea.Cmd {
	if r.forward {
		// A forward is a fresh send to the chat, never a reply quoting
		// its newest message.
		to := r.newTo
		if to == "" {
			to = r.thread
		}
		return chatOutgoingCmd(client, core.Outgoing{
			Channel:     r.channel,
			Account:     r.account,
			To:          []string{to},
			Thread:      r.thread,
			Body:        r.body,
			Attachments: r.attachments,
			Voice:       r.voice,
			Forward:     true,
		}, token, dryRun)
	}
	if r.draftID != "" || r.newTo == "" {
		if dryRun {
			return previewChatReply(client, r.draftID, r.body, r.attachments, token, r.voice)
		}
		return sendChatReply(client, r.draftID, r.body, r.attachments, token, r.voice)
	}
	return chatOutgoingCmd(client, core.Outgoing{
		Channel:     r.channel,
		Account:     r.account,
		To:          []string{r.newTo},
		Thread:      r.thread,
		Body:        r.body,
		Attachments: r.attachments,
		Voice:       r.voice,
	}, token, dryRun)
}

// chatOutgoingCmd previews (dryRun) or sends out as a fresh message from
// the chat composer.
func chatOutgoingCmd(client Client, out core.Outgoing, token uint64, dryRun bool) tea.Cmd {
	return func() tea.Msg {
		timeout := sendTimeout
		if dryRun {
			timeout = previewTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		plan, receipt, err := client.Send(ctx, out, dryRun)
		if dryRun {
			return chatReplyPreviewMsg{token: token, plan: plan, err: err}
		}
		return chatReplySentMsg{token: token, receipt: receipt, err: err}
	}
}

func previewChatReply(client Client, id, body string, attachments []string, token uint64, voice bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), previewTimeout)
		defer cancel()
		if voice {
			ctx = core.WithVoice(ctx)
		}
		plan, _, err := client.Reply(ctx, id, body, nil, attachments, true)
		return chatReplyPreviewMsg{token: token, plan: plan, err: err}
	}
}

func sendChatReply(client Client, id, body string, attachments []string, token uint64, voice bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		if voice {
			ctx = core.WithVoice(ctx)
		}
		_, receipt, err := client.Reply(ctx, id, body, nil, attachments, false)
		return chatReplySentMsg{token: token, receipt: receipt, err: err}
	}
}

// reloadChatAfterSend fetches the newest page of the conversation after a
// successful send (K10), so the stored FromMe item (K7b) replaces the
// optimistic bubble instead of leaving it as the only visible copy of the
// message just sent. It also reports composing=false now that the send
// is done (best-effort, matching sendChatTyping's own fire-and-forget
// contract) — folded into this one command instead of a separate
// tea.Batch, so callers (and their tests) get straight to the reload
// result without unpacking a tea.BatchMsg.
func reloadChatAfterSend(client Client, channel core.Channel, account, thread, receiptID string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		_ = client.Typing(ctx, string(channel), account, thread, false)
		items, err := client.Thread(ctx, string(channel), account, thread, time.Time{}, threadPageLimit)
		return chatSendReloadMsg{token: token, receiptID: receiptID, items: items, err: err}
	}
}
