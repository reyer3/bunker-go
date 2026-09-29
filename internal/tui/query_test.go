package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"

	"github.com/reyer3/bunker-go/internal/core"
)

// pageClient is inboxClient plus ListPage: pages are answered by the
// cursor they resume from, and every call is logged.
type pageClient struct {
	inboxClient
	pages     map[string]core.Page
	pageErr   error
	pageCalls []pageCall
}

type pageCall struct {
	filter core.Filter
	query  string
}

func (c *pageClient) ListPage(_ context.Context, filter core.Filter, query string) (core.Page, error) {
	c.pageCalls = append(c.pageCalls, pageCall{filter: filter, query: query})
	if c.pageErr != nil {
		return core.Page{}, c.pageErr
	}
	return c.pages[filter.Cursor], nil
}

var queryAt = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

func queryResultItems() []core.Item {
	return []core.Item{
		{
			ID: "mail:cl:r1", Channel: core.ChannelMail, Account: "cl", Thread: "r1",
			From:    core.Address{ID: "ana@example.com", Name: "Ana Ruiz"},
			Subject: "Contrato firmado", Body: "Te envío el contrato firmado.",
			Timestamp: queryAt.Add(-time.Hour),
			Meta:      map[string]string{"folder": "INBOX.Clientes.Acme"},
		},
		{
			ID: "mail:cl:r2", Channel: core.ChannelMail, Account: "cl", Thread: "r2",
			From:    core.Address{ID: "ana@example.com", Name: "Ana Ruiz"},
			Subject: "Agenda del viernes", Body: "¿Te parece a las 10?",
			Timestamp: queryAt.Add(-26 * time.Hour),
			Meta:      map[string]string{"folder": "INBOX"},
		},
		{
			ID: "whatsapp:wa:r3", Channel: core.ChannelWhatsApp, Account: "wa", Thread: "r3",
			ThreadName: "Ana Ruiz", From: core.Address{ID: "ana", Name: "Ana Ruiz"},
			Body: "ya llegué", Timestamp: queryAt.Add(-48 * time.Hour),
		},
	}
}

// queryModel is a loaded, sized inbox with one unread conversation
// that no query result contains, so a test can tell the two apart.
func queryModel(t *testing.T, client Client) Model {
	t.Helper()
	m := NewModel(client)
	m.now = func() time.Time { return queryAt }
	m.width, m.height = 80, 30
	return pollOnce(t, m)
}

func inboxOnlyItems() []core.Item {
	return []core.Item{{
		ID: "matrix:m:1", Channel: core.ChannelMatrix, Account: "m", Thread: "g",
		ThreadName: "Sala general", Body: "hola", Unread: true, Timestamp: queryAt,
	}}
}

func press(t *testing.T, m Model, msg tea.KeyMsg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	return updated.(Model), cmd
}

// runQuery types text into a fresh / filter and presses Enter, feeding
// the ListPage answer back like the Bubble Tea runtime would.
func runQuery(t *testing.T, m Model, text string) Model {
	t.Helper()
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = typeFilter(m, text)
	m, cmd := press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("enter on %q should query the daemon", text)
	}
	updated, _ := m.Update(cmd())
	return updated.(Model)
}

func TestFilterHasOperator(t *testing.T) {
	cases := []struct {
		text    string
		want    bool
		wantErr bool
	}{
		{"", false, false},
		{"jose", false, false},
		{"reunión mañana", false, false},
		{"-spam", false, false},
		{`"a las 10:30"`, false, false},
		{"10:30", false, false},
		{"from:ana", true, false},
		{"hola is:unread", true, false},
		{"-in:Archive", true, false},
		{"FROM:ana", true, false},
		{"foo:bar", false, true},
		{"from:", false, true},
		{`"sin cerrar`, false, true},
	}
	for _, tc := range cases {
		got, err := filterHasOperator(tc.text, queryAt)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("filterHasOperator(%q) = %v, %v; want %v, err %v", tc.text, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestQueryErrorTextIsSpanish(t *testing.T) {
	cases := map[string]string{
		"foo:bar":          `consulta inválida: operador desconocido "foo:"`,
		"from:":            "consulta inválida: from: necesita un valor",
		`"abc`:             "consulta inválida: comillas sin cerrar",
		"is:nuevo":         "consulta inválida: is: acepta unread o read",
		"has:foto":         "consulta inválida: has: acepta attachment",
		"channel:sms":      "consulta inválida: channel: acepta mail, whatsapp o matrix",
		"before:ayer":      "consulta inválida: before: acepta AAAA-MM-DD o 7d, 2w, 3m",
		"after:2026-13-45": "consulta inválida: after: acepta AAAA-MM-DD o 7d, 2w, 3m",
	}
	for text, want := range cases {
		_, err := core.ParseQueryAt(text, queryAt)
		if err == nil {
			t.Fatalf("%q should not parse", text)
		}
		if got := queryErrorText(err); got != want {
			t.Errorf("queryErrorText(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestQueryParseErrorKeepsInput(t *testing.T) {
	client := &pageClient{inboxClient: inboxClient{items: inboxOnlyItems()}}
	m := queryModel(t, client)
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = typeFilter(m, "foo:bar")
	view := m.View()
	if !strings.Contains(view, `consulta inválida: operador desconocido "foo:"`) {
		t.Fatalf("the parse error should show on the filter line:\n%s", view)
	}
	m, cmd := press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || !m.filtering || m.filterQuery != "foo:bar" {
		t.Fatalf("enter on an invalid query must keep typing with the text intact: filtering=%v text=%q", m.filtering, m.filterQuery)
	}
	if len(client.pageCalls) != 0 {
		t.Fatalf("an invalid query must never reach the daemon: %+v", client.pageCalls)
	}
	// Fixing the typo turns it into a valid query.
	for range "foo:bar" {
		m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = typeFilter(m, "from:ana")
	if m.filterErr != nil || !strings.Contains(m.View(), "↵ buscar") {
		t.Fatalf("a valid query should clear the error:\n%s", m.View())
	}
}

func TestQueryRendersDaemonResults(t *testing.T) {
	client := &pageClient{
		inboxClient: inboxClient{items: inboxOnlyItems()},
		pages:       map[string]core.Page{"": {Items: queryResultItems()}},
	}
	m := queryModel(t, client)
	m = runQuery(t, m, "from:ana")

	if len(client.pageCalls) != 1 {
		t.Fatalf("want one ListPage call, got %+v", client.pageCalls)
	}
	call := client.pageCalls[0]
	if call.query != "from:ana" || call.filter.Limit != queryPageSize || call.filter.Cursor != "" {
		t.Fatalf("ListPage call = %+v", call)
	}
	view := m.View()
	for _, want := range []string{"Contrato firmado", "Agenda del viernes", "Ana Ruiz", "consulta: from:ana", "3 conversaciones"} {
		if !strings.Contains(view, want) {
			t.Fatalf("results view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Sala general") {
		t.Fatalf("query results replace the inbox groups:\n%s", view)
	}
	// Results are rows like the inbox's: Enter opens the selected one.
	rows := m.visibleRows()
	if len(rows) != 3 || rows[0].kind != navThread || rows[0].thread.items[0].ID != "mail:cl:r1" {
		t.Fatalf("result rows = %+v", rows)
	}

	// Esc returns to the inbox.
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	view = m.View()
	if m.queryActive || m.filterQuery != "" || !strings.Contains(view, "Sala general") || strings.Contains(view, "Contrato firmado") {
		t.Fatalf("esc should return to the inbox:\n%s", view)
	}
}

func TestQueryPaginatesOnScroll(t *testing.T) {
	items := queryResultItems()
	client := &pageClient{
		inboxClient: inboxClient{items: inboxOnlyItems()},
		pages: map[string]core.Page{
			"":   {Items: items[:2], NextCursor: "c1"},
			"c1": {Items: items[2:]},
		},
	}
	m := queryModel(t, client)
	m = runQuery(t, m, "from:ana")
	if !strings.Contains(m.View(), "↓ más") {
		t.Fatalf("a page with a cursor should say there is more:\n%s", m.View())
	}

	// j to the first row does not page yet; reaching the last one does.
	m, cmd := press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if cmd == nil {
		t.Fatalf("reaching the last result should load the next page")
	}
	if len(client.pageCalls) != 1 {
		t.Fatalf("the page loads when the command runs, not before")
	}
	// A second j while that page is in flight must not request it again.
	if _, again := press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}); again != nil {
		t.Fatalf("a page already loading must not be requested twice")
	}
	updated, _ := m.Update(cmd())
	m = updated.(Model)
	if got := client.pageCalls[1]; got.filter.Cursor != "c1" || got.query != "from:ana" {
		t.Fatalf("next page call = %+v", got)
	}
	view := m.View()
	if !strings.Contains(view, "3 conversaciones") || strings.Contains(view, "↓ más") {
		t.Fatalf("the second page should append and end the paging:\n%s", view)
	}
	if len(m.visibleRows()) != 3 {
		t.Fatalf("want 3 rows after two pages, got %d", len(m.visibleRows()))
	}
	// The last page has no cursor: nothing more is requested.
	m.selected = len(m.visibleRows()) - 1
	if _, cmd := press(t, m, tea.KeyMsg{Type: tea.KeyDown}); cmd != nil {
		t.Fatalf("no request past the last page")
	}
}

func TestQueryWheelPaginates(t *testing.T) {
	items := queryResultItems()
	client := &pageClient{pages: map[string]core.Page{
		"":   {Items: items[:1], NextCursor: "c1"},
		"c1": {Items: items[1:]},
	}}
	m := queryModel(t, client)
	m = runQuery(t, m, "from:ana")
	updated, cmd := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if cmd == nil {
		t.Fatalf("the wheel at the last result should load the next page")
	}
	updated, _ = updated.(Model).Update(cmd())
	if n := len(updated.(Model).visibleRows()); n != 3 {
		t.Fatalf("want 3 rows after the wheel paged, got %d", n)
	}
}

func TestQueryDebounce(t *testing.T) {
	client := &pageClient{pages: map[string]core.Page{"": {Items: queryResultItems()}}}
	m := queryModel(t, client)
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = typeFilter(m, "from:an")
	stale := queryDebounceMsg{token: m.queryDebounceToken, text: m.filterQuery}
	m, cmd := press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd == nil {
		t.Fatalf("typing an operator query should schedule a debounce")
	}
	updated, cmd := m.Update(stale)
	m = updated.(Model)
	if cmd != nil || m.queryActive {
		t.Fatalf("a debounce tick overtaken by more typing must be ignored")
	}
	updated, cmd = m.Update(queryDebounceMsg{token: m.queryDebounceToken, text: m.filterQuery})
	m = updated.(Model)
	if cmd == nil || !m.queryActive || !m.filtering {
		t.Fatalf("the latest tick should query while typing continues")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if !strings.Contains(m.View(), "Contrato firmado") || client.pageCalls[0].query != "from:ana" {
		t.Fatalf("debounced results:\n%s", m.View())
	}
	// Enter on the query already shown does not ask again.
	if _, cmd := press(t, m, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatalf("enter on the active query should not re-query")
	}
}

func TestPlainTextFilterStaysInMemory(t *testing.T) {
	client := &pageClient{inboxClient: inboxClient{items: inboxOnlyItems()}}
	m := queryModel(t, client)
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "general" {
		var cmd tea.Cmd
		m, cmd = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if cmd != nil {
			t.Fatalf("plain text must not schedule a daemon query")
		}
	}
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(client.pageCalls) != 0 || m.queryActive || !strings.Contains(m.View(), "Sala general") {
		t.Fatalf("plain text filters in memory:\n%s", m.View())
	}
}

func TestQueryFallsBackWithoutListPage(t *testing.T) {
	client := &inboxClient{items: inboxOnlyItems()}
	m := queryModel(t, client)
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = typeFilter(m, "from:ana")
	if m.queryActive {
		t.Fatalf("a client without ListPage cannot run daemon queries")
	}
	if view := m.View(); !strings.Contains(view, "el daemon no admite consultas") || !strings.Contains(view, "/from:ana") {
		t.Fatalf("the fallback should say why, keeping the text:\n%s", view)
	}
	// The in-memory filter still works for plain text.
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = typeFilter(m, "general")
	if !strings.Contains(m.View(), "Sala general") {
		t.Fatalf("plain filter after the fallback:\n%s", m.View())
	}
}

func TestQueryFallsBackOnOlderDaemon(t *testing.T) {
	client := &pageClient{
		inboxClient: inboxClient{items: inboxOnlyItems()},
		pageErr:     errors.New(`rpc: unknown method "list_page"`),
	}
	m := queryModel(t, client)
	m = runQuery(t, m, "from:ana")
	if m.queryActive || !m.queryNoPaging {
		t.Fatalf("an older daemon should turn daemon queries off")
	}
	if view := m.View(); !strings.Contains(view, "el daemon no admite consultas") {
		t.Fatalf("the fallback should flash a notice:\n%s", view)
	}
	// Later operator queries stay in memory without asking again.
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = typeFilter(m, "is:unread")
	if m, cmd := press(t, m, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || m.queryActive {
		t.Fatalf("no further daemon queries after the fallback")
	}
	if len(client.pageCalls) != 1 {
		t.Fatalf("want exactly the one failed call, got %d", len(client.pageCalls))
	}
}

func TestQueryDaemonErrorShowsOnStatusLine(t *testing.T) {
	client := &pageClient{pageErr: errors.New("store: busy")}
	m := queryModel(t, client)
	m = runQuery(t, m, "from:ana")
	if view := m.View(); !m.queryActive || !strings.Contains(view, "consulta: from:ana · error:") {
		t.Fatalf("a daemon failure shows with the query:\n%s", view)
	}
}

func TestHelpListsQueryOperators(t *testing.T) {
	body, _ := helpBody("query")
	text := strings.Join(body, "\n")
	for _, want := range []string{"Consultas (/)", "from: to:", "is:", "in:", "before:", "-x", "Esc"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help lacks %q:\n%s", want, text)
		}
	}
	m := queryModel(t, &pageClient{})
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if m.helpContext() != "query" {
		t.Fatalf("F1 while typing a filter should open on the query section, got %q", m.helpContext())
	}
}

// TestQueryResultsGolden goldens daemon results: the same rows as the
// inbox, grouped by channel section, and the active query on the status
// line.
func TestQueryResultsGolden(t *testing.T) {
	items := queryResultItems()
	client := &pageClient{pages: map[string]core.Page{"": {Items: items, NextCursor: "c1"}}}
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	m := NewModel(client).withGlyphs(nil)
	m.render = r
	m.now = func() time.Time { return queryAt }
	m.width, m.height = 72, 22
	m = pollOnce(t, m)
	m = runQuery(t, m, "from:ana")
	teatest.RequireEqualOutput(t, []byte(m.View()))
}
