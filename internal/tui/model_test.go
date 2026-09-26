package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelStartsWithAnInboxPlaceholder(t *testing.T) {
	model := NewModel(nil)
	if cmd := model.Init(); cmd != nil {
		t.Fatal("skeleton model unexpectedly started an RPC command")
	}
	if view := model.View(); !strings.Contains(view, "Cargando bandeja de entrada") {
		t.Fatalf("initial view = %q, want a loading placeholder", view)
	}
}

func TestModelQuit(t *testing.T) {
	model := NewModel(nil)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q did not return a quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("q command returned %T, want tea.QuitMsg", msg)
	}
	if _, ok := updated.(Model); !ok {
		t.Fatalf("updated model = %T, want Model", updated)
	}
}
