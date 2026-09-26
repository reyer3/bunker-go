package whatsapp

import (
	"context"

	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

// defaultHistoryMessagesPerChat is how many of the most recent messages
// a *events.HistorySync import keeps per unread conversation, when an
// account's "history_messages_per_chat" option does not override it.
// WhatsApp's initial history sync on a freshly linked device can carry
// years of messages; importing all of it would turn one link into an
// unbounded backfill instead of "what's unread right now".
const defaultHistoryMessagesPerChat = 5

// statusBroadcastJID is WhatsApp's pseudo-chat for status updates. It
// shows up in history sync like any other conversation but is never a
// real thread bunker-go should surface.
const statusBroadcastJID = "status@broadcast"

// handleHistorySync bounds a *events.HistorySync payload to what "unread
// right now" actually needs: it records nothing for a conversation with
// no unread messages, skips archived chats and the status broadcast, and
// otherwise imports only the last historyLimit messages of the
// conversation - never its full history - marking the newest
// min(unreadCount, historyLimit) of those as unread and the rest as read.
func (a *Adapter) handleHistorySync(ctx context.Context, sink core.Sink, data *waHistorySync.HistorySync) {
	if data == nil {
		return
	}
	limit := a.historyLimit
	if limit <= 0 {
		limit = defaultHistoryMessagesPerChat
	}

	for _, conv := range data.GetConversations() {
		if conv.GetArchived() {
			continue
		}
		id := conv.GetID()
		if id == statusBroadcastJID {
			continue
		}
		unread := int(conv.GetUnreadCount())
		if unread <= 0 {
			continue
		}
		chatJID, err := types.ParseJID(id)
		if err != nil {
			continue
		}

		msgs := conv.GetMessages()
		if len(msgs) > limit {
			msgs = msgs[len(msgs)-limit:]
		}
		unreadInWindow := unread
		if unreadInWindow > len(msgs) {
			unreadInWindow = len(msgs)
		}
		readCount := len(msgs) - unreadInWindow

		for i, hsMsg := range msgs {
			evt, err := a.cli.ParseWebMessage(chatJID, hsMsg.GetMessage())
			if err != nil || evt == nil {
				continue
			}
			item := toItem(a.account, evt)
			if !isSurfaceable(item) {
				continue
			}
			item = a.enrichItem(ctx, item, evt.Info.Chat, evt.Info.Sender, string(evt.Info.ID), evt.Info.PushName)
			item.Unread = i >= readCount

			a.cacheItem(item)
			_ = sink.Upsert(ctx, item)
		}
	}
}
