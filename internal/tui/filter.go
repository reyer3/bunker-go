package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// Inbox filter (issue #39): "/" narrows the inbox to conversations whose
// name, sender, subject or text contains what is typed, ignoring case
// and accents. Enter keeps the filter and returns to the list; Esc
// clears it.

// groupMatches reports whether any item of g matches the folded query.
func groupMatches(g inboxGroup, query string) bool {
	if query == "" {
		return true
	}
	for _, it := range g.items {
		hay := core.FoldSearch(strings.Join([]string{it.ThreadName, it.From.Name, it.From.ID, it.Subject, it.Body}, " "))
		if strings.Contains(hay, query) {
			return true
		}
	}
	return false
}

func (m Model) foldedFilter() string { return core.FoldSearch(m.filterQuery) }

func (m Model) startFilter() Model {
	m.filtering = true
	return m
}

func (m Model) clearFilter() Model {
	m.filtering = false
	m.filterQuery = ""
	return m.clampSelection()
}

func (m Model) clampSelection() Model {
	if visible := len(m.visibleRows()); m.selected >= visible {
		m.selected = max(0, visible-1)
	}
	return m
}

// updateFilter handles keys while typing the filter.
func (m Model) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m.clearFilter(), nil
	case "enter":
		m.filtering = false
		return m, nil
	case "backspace":
		if r := []rune(m.filterQuery); len(r) > 0 {
			m.filterQuery = string(r[:len(r)-1])
		}
	default:
		switch msg.Type {
		case tea.KeyRunes:
			m.filterQuery += string(msg.Runes)
		case tea.KeySpace:
			m.filterQuery += " "
		default:
			return m, nil
		}
	}
	m.selected = 0
	return m.clampSelection(), nil
}

// filterLine is the line above the inbox while a filter is typed or set.
func (m Model) filterLine() (string, bool) {
	switch {
	case m.filtering:
		return "/" + safeLine(m.filterQuery) + "▏ · ↵ aplicar · Esc quitar", true
	case m.filterQuery != "":
		return "filtro: " + safeLine(m.filterQuery) + " · / editar · Esc quitar", true
	}
	return "", false
}
