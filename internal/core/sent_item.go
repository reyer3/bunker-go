package core

import (
	"context"
	"log/slog"
	"time"
)

// storeSentItem persists a successful (non-dry-run) send/reply as a
// FromMe item immediately (K7b, conversation-view.md's Usability pass): a
// real live send showed that a message the user had just sent never
// appeared in the chat view. Most channels never echo bunker's own
// outgoing message back as an ingest event — WhatsApp never delivers
// events.Message for a linked device's own send — so without this the
// item would simply never exist in the store at all.
//
// Mail is deliberately skipped: its Sent-folder sync (K2,
// conversation-view.md) already upserts the exact same message once it
// round-trips through IMAP, keyed by its own UID-derived item id. This
// call has no way to learn that later id in advance, so storing a
// second, differently-ID'd copy here would be a permanent duplicate the
// K2 sync could never dedupe against — relying solely on K2 for mail was
// the simpler of the two options the task named.
//
// Idempotent by construction: Upsert keyed by receipt.ID overwrites, it
// never appends, so a channel that later DOES echo the same message id
// back through its normal ingest path just refreshes this same row
// instead of creating a duplicate.
//
// The echo can also win the race: a Matrix sync running concurrently may
// upsert it before Send returns here. That row is the richer one (room
// name, sender, server timestamp, attachment refs), so an existing row
// is kept rather than overwritten with this optimistic sketch.
//
// threadName is the chat's display name when the caller knows it (a reply
// passes the replied item's). Otherwise it falls back to the newest name
// already stored for the thread: the TUI titles a conversation from its
// newest item, so an unnamed sent item would show the bare number.
//
// receipt.Thread, when the adapter reports it, overrides thread: it is
// the chat the message actually went to, in the form live ingest keys
// that conversation on. A WhatsApp send to bare digits would otherwise
// group under the digits as typed, a separate nameless conversation
// beside the person's real chat.
func (s *Service) storeSentItem(ctx context.Context, channel Channel, account, thread, threadName, to, subject, body string, attachments []AttachmentInfo, receipt Receipt) {
	if channel == ChannelMail || receipt.ID == "" {
		return
	}
	if receipt.Thread != "" {
		thread = receipt.Thread
	}
	if _, err := s.store.Get(ctx, receipt.ID); err == nil {
		return
	}
	if threadName == "" {
		threadName = s.lastThreadName(ctx, channel, account, thread)
	}
	item := Item{
		ID:          receipt.ID,
		Channel:     channel,
		Account:     account,
		Thread:      thread,
		ThreadName:  threadName,
		To:          []Address{{ID: to}},
		Subject:     subject,
		Body:        body,
		Attachments: attachmentsFromInfo(attachments),
		FromMe:      true,
		Unread:      false,
		Timestamp:   receipt.At,
	}
	if err := s.store.Upsert(ctx, item); err != nil {
		LogSinkError(channel, account, "upsert", err)
	}
}

// threadNameWindow bounds how many of a thread's newest items
// lastThreadName scans: received items always carry the name, so one is
// almost always among the newest few.
const threadNameWindow = 50

// lastThreadName returns the newest non-empty ThreadName stored for
// channel/account/thread, or "" when none is known. It is best effort: a
// lookup failure only costs the name, never the send that already
// happened, so it is logged and swallowed.
func (s *Service) lastThreadName(ctx context.Context, channel Channel, account, thread string) string {
	if thread == "" {
		return ""
	}
	items, err := s.store.Thread(ctx, Filter{Channel: channel, Account: account, Thread: thread}, time.Time{}, threadNameWindow)
	if err != nil {
		slog.Warn("thread name lookup failed", "channel", string(channel), "account", account, "error", err)
		return ""
	}
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].ThreadName != "" {
			return items[i].ThreadName
		}
	}
	return ""
}

// attachmentsFromInfo converts a Plan's validated AttachmentInfo (name/
// MIME/size, computed before any upload) into the Item.Attachments shape
// storeSentItem persists. Ref is left empty: these are local files
// already sent, not a remote reference the store could re-fetch from.
func attachmentsFromInfo(infos []AttachmentInfo) []Attachment {
	if len(infos) == 0 {
		return nil
	}
	out := make([]Attachment, len(infos))
	for i, a := range infos {
		out[i] = Attachment{Name: a.Name, MIME: a.MIME, Size: a.Size, Voice: a.Voice}
		if a.Voice {
			out[i].Duration = int((a.DurationMS + 500) / 1000)
		}
	}
	return out
}

// outgoingThread resolves the Thread key a stored sent item groups under
// when the adapter's Receipt names none (see storeSentItem):
// explicit when Outgoing.Thread was already set (Reply always sets it
// from the original item), else fallback (the recipient address) — the
// same convention live ingest already uses for a fresh WhatsApp/Matrix
// conversation (a Thread is the chat JID/room id; see
// internal/channel/whatsapp/message.go and
// internal/channel/matrix/adapter.go).
func outgoingThread(explicit, fallback string) string {
	if explicit != "" {
		return explicit
	}
	return fallback
}
