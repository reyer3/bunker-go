package tui

import (
	"regexp"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"
)

// Mouse reports (SGR, "ESC [ < b ; x ; y M") sometimes reach bubbletea
// split across reads, above all through a multiplexer such as herdr or
// tmux while the wheel spins fast. bubbletea then misses the sequence and
// hands its printable tail on as typed text, so scrolling a chat filled
// the composer with "[<65;36;28M[<65;36;28M…". mouseLeakFilter catches
// those fragments before any view sees them: a whole report becomes the
// mouse event it was meant to be, a partial one is dropped.

var (
	// sgrMouseText is one or more complete reports without their ESC.
	sgrMouseText = regexp.MustCompile(`^(?:\[?<\d+;\d+;\d+[Mm])+$`)
	sgrMouseOne  = regexp.MustCompile(`^\[?<(\d+);(\d+);(\d+)([Mm])`)
	// sgrMouseHead is a report cut short: "[<65;36;2".
	sgrMouseHead = regexp.MustCompile(`^\[<[\d;]*$`)
	// sgrMouseTail is the rest of a report cut short: "8M".
	sgrMouseTail = regexp.MustCompile(`^[\d;]*[Mm]$`)
)

// mouseLeakFilter returns a tea.WithFilter function. It keeps one bit of
// state: whether the previous message was the head of a split report, so
// only then is a digits-and-M tail treated as one (typed "8M" stays text).
func mouseLeakFilter() func(tea.Model, tea.Msg) tea.Msg {
	pendingTail := false
	return func(_ tea.Model, msg tea.Msg) tea.Msg {
		key, ok := msg.(tea.KeyMsg)
		if !ok {
			pendingTail = false
			return msg
		}
		wasPending := pendingTail
		pendingTail = false
		if key.Type != tea.KeyRunes {
			return msg
		}
		text := string(key.Runes)
		switch {
		case key.Alt && text == "[":
			// The ESC and "[" of a report split from its "<…M" arrive as
			// Alt+[. bunker binds nothing to it, so it is never typing.
			pendingTail = true
			return nil
		case sgrMouseText.MatchString(text):
			return sgrMouseMsg(text)
		case len(key.Runes) > 1 && sgrMouseHead.MatchString(text):
			pendingTail = true
			return nil
		case wasPending && sgrMouseTail.MatchString(text):
			return nil
		}
		return msg
	}
}

// sgrMouseMsg decodes the first report in text, or drops it (nil) when it
// is not one bunker acts on, so a leaked wheel turn still scrolls.
func sgrMouseMsg(text string) tea.Msg {
	m := sgrMouseOne.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	code, _ := strconv.Atoi(m[1])
	x, _ := strconv.Atoi(m[2])
	y, _ := strconv.Atoi(m[3])
	ev := tea.MouseEvent{X: x - 1, Y: y - 1, Shift: code&4 != 0, Alt: code&8 != 0, Ctrl: code&16 != 0}
	switch {
	case code&64 != 0:
		ev.Action = tea.MouseActionPress
		ev.Button = []tea.MouseButton{tea.MouseButtonWheelUp, tea.MouseButtonWheelDown, tea.MouseButtonWheelLeft, tea.MouseButtonWheelRight}[code&3]
	case code&32 != 0:
		ev.Action = tea.MouseActionMotion
		ev.Button = []tea.MouseButton{tea.MouseButtonLeft, tea.MouseButtonMiddle, tea.MouseButtonRight, tea.MouseButtonNone}[code&3]
	default:
		ev.Action = tea.MouseActionPress
		if m[4] == "m" {
			ev.Action = tea.MouseActionRelease
		}
		ev.Button = []tea.MouseButton{tea.MouseButtonLeft, tea.MouseButtonMiddle, tea.MouseButtonRight, tea.MouseButtonNone}[code&3]
	}
	if ev.X < 0 || ev.Y < 0 {
		return nil
	}
	return tea.MouseMsg(ev)
}
