package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// Inbox filter (issue #39): "/" narrows the inbox to conversations whose
// name, sender, subject or text contains what is typed, ignoring case
// and accents. Enter keeps the filter and returns to the list; Esc
// clears it. Text with a query operator is sent to the daemon instead
// (issue #62, query.go).

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
	m.filterErr = nil
	m.filterIsQuery = false
	if m.queryActive {
		m = m.leaveQuery()
	}
	m.queryDebounceToken++
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
		if m.filterErr != nil {
			// Keep typing: the error on the filter line says what to fix,
			// and the text stays as typed.
			return m, nil
		}
		m.filtering = false
		if m.filterIsQuery && m.canQuery() && (!m.queryActive || m.queryText != m.filterQuery || m.queryErr != nil) {
			m.queryDebounceToken++
			return m.startQuery(m.filterQuery)
		}
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
	return m.afterFilterEdit()
}

// filterLine is the line above the inbox while a filter is typed or set.
func (m Model) filterLine() (string, bool) {
	switch {
	case m.filtering && m.filterErr != nil:
		return "/" + safeLine(m.filterQuery) + "▏ · " + queryErrorText(m.filterErr), true
	case m.filtering && m.filterIsQuery && m.canQuery():
		return "/" + safeLine(m.filterQuery) + "▏ · ↵ buscar · Esc quitar", true
	case m.filtering:
		return "/" + safeLine(m.filterQuery) + "▏ · ↵ aplicar · Esc quitar", true
	case m.queryActive:
		// The query itself is on the status line (queryStatus).
		return "/ editar · Esc volver a la bandeja", true
	case m.filterQuery != "":
		return "filtro: " + safeLine(m.filterQuery) + " · / editar · Esc quitar", true
	}
	return "", false
}
