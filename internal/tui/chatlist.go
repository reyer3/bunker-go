package tui

import (
	"context"

	"github.com/reyer3/bunker-go/internal/core"
)

// The WhatsApp and Matrix tabs list conversations the way a messaging
// app does: every recent one, newest message first, read or not, each
// with its unread count. Mail and the overview stay unread-only (inbox
// zero). The unread list the poll already fetches keeps feeding counts
// and notifications; this is a second, display-only list.

// ConversationsClient is the optional capability behind those tabs: the
// RPC client and the TUI's query client implement it. Against a daemon
// that lacks it (an older one), the tabs keep listing unread items only.
type ConversationsClient interface {
	Conversations(ctx context.Context, filter core.ConversationFilter) ([]core.Conversation, error)
}

// chatListChannels are the channels whose focused tab shows the chat list.
var chatListChannels = []core.Channel{core.ChannelWhatsApp, core.ChannelMatrix}

func isChatListChannel(channel core.Channel) bool {
	for _, ch := range chatListChannels {
		if ch == channel {
			return true
		}
	}
	return false
}

// fetchChatLists loads each chat-list channel's conversations. A channel
// whose query fails is left out of the result, so the model keeps what it
// last showed (or the unread-only list when it never loaded): a missing
// capability or a slow daemon must not blank the tab or fail the poll.
func fetchChatLists(ctx context.Context, client Client) map[core.Channel][]core.Conversation {
	lister, ok := client.(ConversationsClient)
	if !ok {
		return nil
	}
	out := make(map[core.Channel][]core.Conversation, len(chatListChannels))
	for _, ch := range chatListChannels {
		convs, err := lister.Conversations(ctx, core.ConversationFilter{Channel: ch, Limit: inboxLimit})
		if err != nil {
			continue
		}
		out[ch] = convs
	}
	return out
}

// groupConversations turns the daemon's conversations into chat-list
// rows, keeping its order (newest message first).
func groupConversations(convs []core.Conversation) []inboxGroup {
	groups := make([]inboxGroup, 0, len(convs))
	for _, c := range convs {
		groups = append(groups, inboxGroup{items: []core.Item{c.Last}, conversation: true, unread: c.Unread})
	}
	return groups
}

// mergeChatLists returns old updated with the channels fresh carries.
func mergeChatLists(old map[core.Channel][]inboxGroup, fresh map[core.Channel][]core.Conversation) map[core.Channel][]inboxGroup {
	if len(fresh) == 0 {
		return old
	}
	out := make(map[core.Channel][]inboxGroup, len(old)+len(fresh))
	for ch, groups := range old {
		out[ch] = groups
	}
	for ch, convs := range fresh {
		if len(convs) > inboxLimit {
			convs = convs[:inboxLimit]
		}
		out[ch] = groupConversations(convs)
	}
	return out
}

// chatList returns channel's conversation rows when the focused tab is
// that channel's and its list has loaded; ok is false everywhere else
// (the overview, Mail, a query's results), which list unread items.
func (m Model) chatList(channel core.Channel) (groups []inboxGroup, ok bool) {
	if m.queryActive || !isChatListChannel(channel) {
		return nil, false
	}
	if focused, isFocused := m.currentChannelFilter(); !isFocused || focused != channel {
		return nil, false
	}
	groups, ok = m.chats[channel]
	return groups, ok
}

// selectedRowUnread reports whether the selected row has unread messages
// to mark read. A chat-list row for a fully read conversation has none,
// so the mark-read key leaves it alone.
func (m Model) selectedRowUnread() bool {
	rows := m.visibleRows()
	if m.selected < 0 || m.selected >= len(rows) {
		return false
	}
	row := rows[m.selected]
	if row.kind != navThread || len(row.thread.items) == 0 {
		return false
	}
	if row.thread.conversation {
		return row.thread.unread > 0 && row.thread.items[0].Unread
	}
	return true
}
