package tui

// Keyboard message selection in the chat view. The composer always has
// focus there, so plain arrows move the draft's cursor; Alt+Up/Alt+Down
// move the selection (m.chatFocus, the same one a click sets) instead,
// and every action that works on "the selected message, else the newest"
// (Alt+Y, Alt+O, ...) then works without the mouse.

const (
	chatSelectPrevKey = "alt+up"
	chatSelectNextKey = "alt+down"
)

// moveChatFocus moves the selection by delta messages: -1 to the older
// one, +1 to the newer one. With nothing selected, going older selects
// the newest and going newer does nothing; going newer past the newest
// drops the selection, and the oldest loaded message stays selected
// (older pages load with PgUp, as before). The selected bubble is
// scrolled into view.
func (m Model) moveChatFocus(delta int) Model {
	var ids []string
	for _, it := range m.chatItems {
		if it.ID != "" {
			ids = append(ids, it.ID)
		}
	}
	if len(ids) == 0 {
		return m
	}
	cur := -1
	for i, id := range ids {
		if id == m.chatFocus {
			cur = i
		}
	}
	switch {
	case cur < 0 && delta < 0:
		cur = len(ids) - 1
	case cur < 0:
		return m
	default:
		cur += delta
	}
	switch {
	case cur >= len(ids):
		m.chatFocus = ""
		return m
	case cur < 0:
		cur = 0
	}
	m.chatFocus = ids[cur]
	return m.scrollChatToFocus()
}

// scrollChatToFocus adjusts m.chatScroll (measured from the bottom, see
// windowTail) the least needed for the selected bubble to be on screen;
// a bubble taller than the window shows its first lines.
func (m Model) scrollChatToFocus() Model {
	if m.chatFocus == "" {
		return m
	}
	_, meta := m.chatBodyMeta()
	first, last := -1, -1
	for i, mt := range meta {
		if mt.item == m.chatFocus {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return m
	}
	total := len(meta)
	budget := m.chatScrollBudget()
	start, end, scroll := windowBounds(total, m.chatScroll, budget)
	switch {
	case last >= end:
		scroll = total - (last + 1)
	case first < start:
		scroll = total - first - budget
	}
	if first < total-scroll-budget {
		scroll = total - first - budget
	}
	m.chatScroll = clampScroll(scroll, total, budget)
	return m
}
