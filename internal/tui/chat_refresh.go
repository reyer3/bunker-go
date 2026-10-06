package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// chatRefreshInterval is how often an open chat re-reads its newest page.
// The chat otherwise only loads at open and after a send, so messages
// that arrive while it is open never showed up. The read is a local
// store query over the socket, so a short interval is cheap.
const chatRefreshInterval = 2 * time.Second

type chatRefreshTickMsg struct{ token uint64 }

func nextChatRefresh(token uint64) tea.Cmd {
	return tea.Tick(chatRefreshInterval, func(time.Time) tea.Msg { return chatRefreshTickMsg{token: token} })
}

// chatRefreshedMsg carries one refresh of the open chat's newest page.
type chatRefreshedMsg struct {
	token uint64
	items []core.Item
	err   error
}

func refreshChatCmd(client Client, channel core.Channel, account, thread string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		items, err := client.Thread(ctx, string(channel), account, thread, time.Time{}, threadPageLimit)
		return chatRefreshedMsg{token: token, items: items, err: err}
	}
}

// chatRefreshReadMsg reports that markChatReadCmd finished; the next
// refresh tick is scheduled only then, so reads never overlap.
type chatRefreshReadMsg struct{ token uint64 }

// markChatReadCmd marks the conversation read once new incoming messages
// show up while the chat is open and focused, the same as reopening it
// would. Best-effort: a failure only leaves the unread count stale.
func markChatReadCmd(client Client, channel core.Channel, account, thread string, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		_, _ = client.ReadThread(ctx, string(channel), account, thread, true)
		return chatRefreshReadMsg{token: token}
	}
}

func (m Model) handleChatRefreshTick(msg chatRefreshTickMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.chatToken || !m.chatMode || m.client == nil {
		return m, nil
	}
	if m.chatLoading {
		// An open or an older-page load is in flight; its result would
		// race this one, so try again on the next tick.
		return m, nextChatRefresh(m.chatToken)
	}
	return m, refreshChatCmd(m.client, m.chatChannel, m.chatAccount, m.chatThread, m.chatToken)
}

func (m Model) handleChatRefreshed(msg chatRefreshedMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.chatToken || !m.chatMode {
		return m, nil
	}
	next := nextChatRefresh(m.chatToken)
	if msg.err != nil || m.chatLoading {
		// A failed refresh keeps what is on screen; the next tick retries.
		return m, next
	}
	merged, incoming := mergeChatPage(m.chatItems, msg.items)
	m.chatItems = merged
	m = m.reconcileChatQueue()
	if incoming && !m.blurred {
		return m, markChatReadCmd(m.client, m.chatChannel, m.chatAccount, m.chatThread, m.chatToken)
	}
	return m, next
}

func (m Model) handleChatRefreshRead(msg chatRefreshReadMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.chatToken || !m.chatMode {
		return m, nil
	}
	return m, nextChatRefresh(m.chatToken)
}

// mergeChatPage folds a fresh newest page into the loaded conversation
// (oldest first): older pages loaded by scrolling up stay, the overlap
// is replaced by the page's copy (so edits, reactions and deletions show),
// and newer items are appended. incoming reports whether the page added
// a message from someone else.
func mergeChatPage(loaded, page []core.Item) (merged []core.Item, incoming bool) {
	if len(page) == 0 {
		return loaded, false
	}
	known := make(map[string]bool, len(loaded))
	for _, it := range loaded {
		known[it.ID] = true
	}
	inPage := make(map[string]bool, len(page))
	for _, it := range page {
		inPage[it.ID] = true
		if !known[it.ID] && !it.FromMe {
			incoming = true
		}
	}
	oldest := page[0].Timestamp
	merged = make([]core.Item, 0, len(loaded)+len(page))
	for _, it := range loaded {
		if !inPage[it.ID] && it.Timestamp.Before(oldest) {
			merged = append(merged, it)
		}
	}
	return append(merged, page...), incoming
}
