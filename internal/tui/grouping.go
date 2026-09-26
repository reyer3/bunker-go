package tui

import (
	"sort"

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
