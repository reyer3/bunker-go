package tui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// Query language in the / filter (issue #62). Plain text keeps filtering
// the loaded inbox in memory, on every key, because that is instant and
// is what most typing is. Once the text holds an operator (from:, is:,
// in:, …) the question is about the whole store, not the few unread
// conversations on screen, so the daemon answers it through ListPage,
// one page at a time, and its results replace the inbox groups until
// Esc.

// queryDebounce is how long typing must pause before an operator query
// goes to the daemon on its own: long enough not to send one request
// per key, short enough to feel live. Enter sends at once.
const queryDebounce = 400 * time.Millisecond

// queryPageSize is how many items one ListPage request asks for; the
// next page loads when the selection reaches the last result.
const queryPageSize = 50

// queryTimeout bounds one ListPage round trip, like pollTimeout bounds
// the inbox poll.
const queryTimeout = 10 * time.Second

// PageClient is the optional capability daemon queries need: the RPC
// client and the TUI's query client implement it. A client without it
// (or an older daemon that does not know list_page) keeps the in-memory
// filter.
type PageClient interface {
	ListPage(ctx context.Context, filter core.Filter, query string) (core.Page, error)
}

// queryDebounceMsg fires queryDebounce after a keystroke; token tells a
// stale tick (more typing followed) from the latest one.
type queryDebounceMsg struct {
	token uint64
	text  string
}

// queryPageMsg is one ListPage answer. cursor is the cursor the request
// resumed from, "" for the first page.
type queryPageMsg struct {
	token  uint64
	cursor string
	page   core.Page
	err    error
}

// filterHasOperator reports whether text is operator-language: true
// when it parses to at least one non-free-text term. Negated words and
// quoted phrases alone stay plain text, since the in-memory filter
// handles text well enough. err is the parse error, if any.
func filterHasOperator(text string, now time.Time) (bool, error) {
	q, err := core.ParseQueryAt(text, now)
	if err != nil {
		return false, err
	}
	for _, term := range q.Terms {
		if term.Field != core.QueryText {
			return true, nil
		}
	}
	return false, nil
}

var unknownOperatorRe = regexp.MustCompile(`unknown operator ("[^"]*")`)

// queryErrorText is a parse error in the UI's Spanish. core's messages
// are English for the CLI and MCP; the TUI recognizes each shape
// ParseQuery produces and says the same thing in Spanish, falling back
// to the raw message for anything new rather than hiding it.
func queryErrorText(err error) string {
	msg := err.Error()
	var detail string
	switch {
	case unknownOperatorRe.MatchString(msg):
		detail = "operador desconocido " + unknownOperatorRe.FindStringSubmatch(msg)[1]
	case strings.Contains(msg, "unterminated quote"):
		detail = "comillas sin cerrar"
	case strings.Contains(msg, "needs a value"):
		op := strings.TrimSpace(strings.TrimPrefix(msg[:strings.Index(msg, " needs a value")], "core: query:"))
		detail = op + " necesita un valor"
	case strings.Contains(msg, "want is:unread or is:read"):
		detail = "is: acepta unread o read"
	case strings.Contains(msg, "want has:attachment"):
		detail = "has: acepta attachment"
	case strings.Contains(msg, "want mail, whatsapp or matrix"):
		detail = "channel: acepta mail, whatsapp o matrix"
	case strings.Contains(msg, "want YYYY-MM-DD"):
		op := "fecha"
		for _, name := range []string{"before:", "after:"} {
			if strings.Contains(msg, "query: "+name) {
				op = name
			}
		}
		detail = op + " acepta AAAA-MM-DD o 7d, 2w, 3m"
	default:
		detail = strings.TrimSuffix(strings.TrimPrefix(msg, "core: query: "), ": "+core.ErrInvalidQuery.Error())
	}
	return "consulta inválida: " + safeLine(detail)
}

// pagingUnsupported reports whether err means the daemon cannot page a
// query at all: the capability is missing (core.ErrUnsupported), or an
// older daemon rejected list_page as an unknown method.
func pagingUnsupported(err error) bool {
	return errors.Is(err, core.ErrUnsupported) || strings.Contains(err.Error(), "unknown method")
}

// canQuery reports whether operator queries may go to the daemon.
func (m Model) canQuery() bool {
	if m.client == nil || m.queryNoPaging {
		return false
	}
	_, ok := m.client.(PageClient)
	return ok
}

// afterFilterEdit reclassifies the typed filter after a keystroke: a
// parse error is kept for the filter line (the text stays as typed),
// plain text leaves any daemon query, and an operator query is debounced
// to the daemon.
func (m Model) afterFilterEdit() (Model, tea.Cmd) {
	m.selected = 0
	isQuery, err := filterHasOperator(m.filterQuery, m.clock())
	m.filterErr = err
	m.filterIsQuery = isQuery
	if err != nil {
		return m.clampSelection(), nil
	}
	if !isQuery {
		if m.queryActive {
			m = m.leaveQuery()
		}
		return m.clampSelection(), nil
	}
	if !m.canQuery() {
		if m.client == nil {
			return m.clampSelection(), nil
		}
		return m.noPagingFallback(), nil
	}
	m.queryDebounceToken++
	token, text := m.queryDebounceToken, m.filterQuery
	return m.clampSelection(), tea.Tick(queryDebounce, func(time.Time) tea.Msg {
		return queryDebounceMsg{token: token, text: text}
	})
}

// noPagingFallback keeps the in-memory filter for an operator query the
// daemon cannot answer, telling the user once why it matches less.
func (m Model) noPagingFallback() Model {
	if m.queryActive {
		m = m.leaveQuery()
	}
	if !m.queryNoPaging {
		m.queryNoPaging = true
		m = m.withFlash("el daemon no admite consultas · se filtra solo lo cargado")
	}
	return m.clampSelection()
}

// startQuery sends text to the daemon as a new query, replacing any
// previous results.
func (m Model) startQuery(text string) (Model, tea.Cmd) {
	m.queryActive = true
	m.queryText = text
	m.queryItems = nil
	m.queryGroups = nil
	m.queryCursor = ""
	m.queryLoading = true
	m.queryErr = nil
	m.queryToken++
	m.selected = 0
	return m, listPageCmd(m.client, text, "", m.queryToken)
}

// leaveQuery drops the daemon results and returns to the inbox groups;
// bumping the tokens discards any page or debounce still in flight.
func (m Model) leaveQuery() Model {
	m.queryActive = false
	m.queryText = ""
	m.queryItems = nil
	m.queryGroups = nil
	m.queryCursor = ""
	m.queryLoading = false
	m.queryErr = nil
	m.queryToken++
	m.queryDebounceToken++
	return m.clampSelection()
}

func listPageCmd(client Client, text, cursor string, token uint64) tea.Cmd {
	return func() tea.Msg {
		pc, ok := client.(PageClient)
		if !ok {
			return queryPageMsg{token: token, cursor: cursor, err: fmt.Errorf("tui: list page: %w", core.ErrUnsupported)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		page, err := pc.ListPage(ctx, core.Filter{Limit: queryPageSize, Cursor: cursor}, text)
		return queryPageMsg{token: token, cursor: cursor, page: page, err: err}
	}
}

func (m Model) handleQueryDebounce(msg queryDebounceMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.queryDebounceToken || msg.text != m.filterQuery {
		return m, nil
	}
	if m.queryActive && m.queryText == msg.text && m.queryErr == nil {
		return m, nil
	}
	return m.startQuery(msg.text)
}

func (m Model) handleQueryPage(msg queryPageMsg) (tea.Model, tea.Cmd) {
	if msg.token != m.queryToken || !m.queryActive {
		return m, nil
	}
	m.queryLoading = false
	if msg.err != nil {
		if pagingUnsupported(msg.err) {
			return m.noPagingFallback(), nil
		}
		m.queryErr = msg.err
		return m, nil
	}
	m.queryErr = nil
	if msg.cursor == "" {
		m.queryItems = nil
	}
	m.queryItems = append(m.queryItems, msg.page.Items...)
	m.queryGroups = groupItems(m.queryItems, false)
	m.queryCursor = msg.page.NextCursor
	return m.clampSelection(), nil
}

// maybeLoadMore asks for the next page once the selection reaches the
// last result and the daemon said there is more.
func (m Model) maybeLoadMore() (Model, tea.Cmd) {
	if !m.queryActive || m.queryLoading || m.queryCursor == "" || m.client == nil {
		return m, nil
	}
	if m.selected < len(m.visibleRows())-1 {
		return m, nil
	}
	m.queryLoading = true
	return m, listPageCmd(m.client, m.queryText, m.queryCursor, m.queryToken)
}

// headerCounts is what section headers and sidebar tabs count: the
// daemon's unread counts for the inbox, or the conversations found per
// channel while query results are shown.
func (m Model) headerCounts() map[core.Channel]map[string]int {
	if !m.queryActive {
		return m.counts
	}
	out := map[core.Channel]map[string]int{}
	for _, g := range m.queryGroups {
		ch := g.items[0].Channel
		if out[ch] == nil {
			out[ch] = map[string]int{}
		}
		out[ch][""]++
	}
	return out
}

// queryStatus is the status line while daemon results are shown: the
// active query, how many conversations it found so far, and whether
// scrolling further loads more.
func (m Model) queryStatus() string {
	line := "consulta: " + safeLine(m.queryText)
	switch {
	case m.queryErr != nil:
		return line + " · error: " + humanError(m.queryErr)
	case m.queryLoading && len(m.queryGroups) == 0:
		return line + " · buscando…"
	}
	n := len(m.queryGroups)
	if n == 1 {
		line += " · 1 conversación"
	} else {
		line += fmt.Sprintf(" · %d conversaciones", n)
	}
	switch {
	case m.queryLoading:
		line += " · cargando más…"
	case m.queryCursor != "":
		line += " · ↓ más"
	}
	return line
}
