package tui

import (
	"sort"
	"strings"

	"github.com/reyer3/bunker-go/internal/core"
)

type inboxGroup struct {
	items []core.Item
}

type inboxKey struct {
	channel   core.Channel
	account   string
	thread    string
	singleton bool
}

// groupUnread orders the loaded, unread snapshot newest first. A missing
// thread has its own item-ID key, distinct even from a real thread with that ID.
func groupUnread(items []core.Item) []inboxGroup {
	ordered := make([]core.Item, 0, len(items))
	for _, item := range items {
		if item.Unread {
			ordered = append(ordered, item)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].Timestamp.Equal(ordered[j].Timestamp) {
			return ordered[i].Timestamp.After(ordered[j].Timestamp)
		}
		return ordered[i].ID < ordered[j].ID
	})
	groups := make([]inboxGroup, 0, len(ordered))
	indices := make(map[inboxKey]int, len(ordered))
	for _, item := range ordered {
		key := inboxKey{channel: item.Channel, account: item.Account, thread: item.Thread}
		if item.Thread == "" {
			key.thread = item.ID
			key.singleton = true
		}
		index, found := indices[key]
		if !found {
			index = len(groups)
			indices[key] = index
			groups = append(groups, inboxGroup{})
		}
		groups[index].items = append(groups[index].items, item)
	}
	return groups
}

// newest returns g's newest item: its own items are already ordered
// newest-first by groupUnread.
func (g inboxGroup) newest() core.Item {
	return g.items[0]
}

// senderGroup is one Mail sender's collapsible row (mail-sender-groups.md):
// every thread from that sender's address, kept newest-first.
type senderGroup struct {
	// key is senderKey's normalized value: the lower-cased From address.
	// It is what mail-sender-groups.md's expand state (Model.mailExpanded)
	// is keyed by, so it must never change across polls for the same
	// real-world sender.
	key     string
	name    string
	threads []inboxGroup
}

// unreadCount sums every thread's loaded unread item count under s — the
// sender row's unread badge.
func (s senderGroup) unreadCount() int {
	n := 0
	for _, t := range s.threads {
		n += len(t.items)
	}
	return n
}

// senderKey normalizes a mail item's sender identity for grouping: the
// lower-cased From address, never the display name (mail-sender-groups.md
// Decisions), so two different display names for the same address merge
// into one sender row and casing never splits one sender in two.
func senderKey(item core.Item) string {
	return strings.ToLower(strings.TrimSpace(item.From.ID))
}

// groupBySender groups Mail threads (each already an existing per-
// conversation inboxGroup, newest-first) by sender address: senders
// ordered by their own newest thread (mirroring the newest-first order of
// threads itself, since a sender's first appearance in that order is
// necessarily its newest thread), threads within a sender keeping their
// existing newest-first order. A sender with exactly one thread still
// gets its own senderGroup (mail-sender-groups.md's consistency rule).
func groupBySender(threads []inboxGroup) []senderGroup {
	senders := make([]senderGroup, 0, len(threads))
	index := make(map[string]int, len(threads))
	for _, t := range threads {
		if len(t.items) == 0 {
			continue
		}
		key := senderKey(t.newest())
		i, ok := index[key]
		if !ok {
			i = len(senders)
			index[key] = i
			senders = append(senders, senderGroup{key: key})
		}
		senders[i].threads = append(senders[i].threads, t)
	}
	for i := range senders {
		senders[i].name = senderDisplayName(senders[i])
	}
	return senders
}

// senderDisplayName is the newest non-empty From.Name across every item
// of every thread under s (threads/items already newest-first, so the
// first non-empty name found is the newest one), falling back to the
// sender's own address when no item ever carried a name.
func senderDisplayName(s senderGroup) string {
	for _, t := range s.threads {
		for _, item := range t.items {
			if name := strings.TrimSpace(item.From.Name); name != "" {
				return name
			}
		}
	}
	if s.key != "" {
		return s.key
	}
	return "(sin nombre)"
}
