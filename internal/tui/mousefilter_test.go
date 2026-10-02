package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// TestMouseLeakFilterTurnsLeakedWheelIntoMouse pins the fix for scrolling
// a chat typing "[<65;36;28M" into the composer: a whole report that
// leaked as text becomes the wheel event it was.
func TestMouseLeakFilterTurnsLeakedWheelIntoMouse(t *testing.T) {
	filter := mouseLeakFilter()
	for _, text := range []string{"[<65;36;28M", "[<65;36;28M[<65;36;28M", "<64;10;5M"} {
		got := filter(nil, runes(text))
		mouse, ok := got.(tea.MouseMsg)
		if !ok {
			t.Fatalf("%q -> %#v, want a tea.MouseMsg", text, got)
		}
		if mouse.Action != tea.MouseActionPress || (mouse.Button != tea.MouseButtonWheelDown && mouse.Button != tea.MouseButtonWheelUp) {
			t.Fatalf("%q -> %+v, want a wheel press", text, mouse)
		}
	}
	alt := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<65;36;28M"), Alt: true}
	if got, ok := filter(nil, alt).(tea.MouseMsg); !ok || got.X != 35 || got.Y != 27 {
		t.Fatalf("Alt+%q -> %#v, want a mouse event at 35,27", "[<65;36;28M", got)
	}
}

// TestMouseLeakFilterDropsSplitReports covers a report cut across reads:
// the ESC+"[" (Alt+[) and the head and tail fragments are all dropped.
func TestMouseLeakFilterDropsSplitReports(t *testing.T) {
	filter := mouseLeakFilter()
	if got := filter(nil, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("["), Alt: true}); got != nil {
		t.Fatalf("Alt+[ -> %#v, want dropped", got)
	}
	if got := filter(nil, runes("<65;36;28M")); got == nil {
		t.Fatal("the rest of the report after Alt+[ was dropped instead of decoded")
	}
	if got := filter(nil, runes("[<65;36;2")); got != nil {
		t.Fatalf("head fragment -> %#v, want dropped", got)
	}
	if got := filter(nil, runes("8M")); got != nil {
		t.Fatalf("tail fragment -> %#v, want dropped", got)
	}
}

// TestMouseLeakFilterKeepsTyping pins that ordinary text is never eaten:
// a lone "<", "[", "8M" typed without a preceding head, a paste with
// brackets.
func TestMouseLeakFilterKeepsTyping(t *testing.T) {
	filter := mouseLeakFilter()
	for _, text := range []string{"<", "[", "8M", "<3", "[<a]", "hola [<1;2;3M] mundo", "12;3m"} {
		msg := runes(text)
		if got := filter(nil, msg); got == nil {
			t.Fatalf("%q was dropped", text)
		} else if k, ok := got.(tea.KeyMsg); !ok || string(k.Runes) != text {
			t.Fatalf("%q -> %#v, want it unchanged", text, got)
		}
	}
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	if got, ok := filter(nil, enter).(tea.KeyMsg); !ok || got.Type != tea.KeyEnter {
		t.Fatalf("enter -> %#v, want unchanged", got)
	}
}
