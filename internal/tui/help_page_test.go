package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// helpSectionTitles are the titles a help page must show alone: one view
// per page, so none of the others may appear with it.
var helpSectionTitles = []string{"Bandeja", "Panel lateral", "En un chat", "Hilo de correo", "Mensaje", "Vista previa del envío", "Redactar", "Consultas (/)"}

// helpPage opens the help on m's current view and returns the rendered
// page without its title line.
func helpPage(t *testing.T, m Model) []string {
	t.Helper()
	m.height = 0
	m.width = 0
	lines := strings.Split(m.openHelp().helpView(), "\n")
	return lines[1:]
}

// helpPageKeys returns every key token on a page: the key column of each
// indented line, split on " o ", ", " and " / ".
func helpPageKeys(page []string) []string {
	var keys []string
	for _, line := range page {
		if !strings.HasPrefix(line, "  ") {
			continue
		}
		col, _, _ := strings.Cut(strings.TrimPrefix(line, "  "), "  ")
		col = strings.TrimSpace(col)
		if col == "" {
			continue
		}
		for _, sep := range []string{" o ", ", ", " / "} {
			col = strings.ReplaceAll(col, sep, "\x00")
		}
		keys = append(keys, strings.Split(col, "\x00")...)
	}
	return keys
}

func helpLineFor(page []string, key string) (string, bool) {
	for _, line := range page {
		col, desc, _ := strings.Cut(strings.TrimPrefix(line, "  "), "  ")
		if strings.HasPrefix(line, "  ") && strings.TrimSpace(col) == key {
			return strings.TrimSpace(desc), true
		}
	}
	return "", false
}

type helpCase struct {
	name, title string
	model       Model
}

func helpCases(t *testing.T) []helpCase {
	t.Helper()
	asker := WithAgentAsker((&fakeAsker{}).ask)
	inbox := NewModel(nil, asker)
	chat, _ := openedChat(t)
	chat.agentAsk = inbox.agentAsk
	thread := inbox
	thread.detail, thread.threadMode = true, true
	detail := inbox
	detail.detail = true
	preview := inbox
	preview.previewing = true
	editor := inbox
	editor.composing = true
	query := inbox
	query.filtering = true
	return []helpCase{
		{"inbox", "Bandeja", inbox},
		{"sidebar", "Panel lateral", sidebarModel(asker)},
		{"chat", "En un chat", chat},
		{"thread", "Hilo de correo", thread},
		{"detail", "Mensaje", detail},
		{"preview", "Vista previa del envío", preview},
		{"editor", "Redactar", editor},
		{"query", "Consultas (/)", query},
	}
}

func TestHelpShowsOnlyTheCurrentSection(t *testing.T) {
	for _, c := range helpCases(t) {
		page := strings.Join(helpPage(t, c.model), "\n")
		for _, title := range helpSectionTitles {
			has := false
			for _, line := range strings.Split(page, "\n") {
				if strings.HasPrefix(line, title) {
					has = true
				}
			}
			if want := title == c.title; has != want {
				t.Errorf("%s help: section %q shown=%v, want %v:\n%s", c.name, title, has, want, page)
			}
		}
	}
}

func TestHelpPageRepeatsNoKey(t *testing.T) {
	for _, c := range helpCases(t) {
		for _, ringing := range []bool{false, true} {
			m := c.model
			if ringing {
				m.calls = []core.Call{ringingCall()}
			}
			page := helpPage(t, m)
			seen := map[string]bool{}
			for _, k := range helpPageKeys(page) {
				if seen[k] {
					t.Errorf("%s help (ringing=%v) repeats %q:\n%s", c.name, ringing, k, strings.Join(page, "\n"))
				}
				seen[k] = true
			}
		}
	}
}

func TestHelpShowsCallsOnlyDuringACall(t *testing.T) {
	m := NewModel(nil)
	if page := strings.Join(helpPage(t, m), "\n"); strings.Contains(page, "Llamadas de voz") {
		t.Fatalf("calls section without a call:\n%s", page)
	}
	m.calls = []core.Call{ringingCall()}
	if page := strings.Join(helpPage(t, m), "\n"); !strings.Contains(page, "Llamadas de voz") {
		t.Fatalf("calls section missing while a call rings:\n%s", page)
	}
}

func TestHelpDetailAndPreviewListTheirOwnKeys(t *testing.T) {
	cases := map[string]Model{}
	for _, c := range helpCases(t) {
		cases[c.name] = c.model
	}
	detail := helpPage(t, cases["detail"])
	for _, key := range []string{"G", "PgUp/PgDn", "r", "m", "Esc", "q"} {
		if _, ok := helpLineFor(detail, key); !ok {
			t.Errorf("detail help lacks %q:\n%s", key, strings.Join(detail, "\n"))
		}
	}
	for _, key := range []string{"n", "/", "@", "c", "J", "1/2/3"} {
		if _, ok := helpLineFor(detail, key); ok {
			t.Errorf("detail help lists %q, which the detail view ignores", key)
		}
	}
	preview := helpPage(t, cases["preview"])
	if desc, ok := helpLineFor(preview, "Esc"); !ok || !strings.Contains(desc, "editar") {
		t.Errorf("preview Esc = %q, want it to return to editing:\n%s", desc, strings.Join(preview, "\n"))
	}
	if _, ok := helpLineFor(preview, "q"); !ok {
		t.Errorf("preview help lacks q:\n%s", strings.Join(preview, "\n"))
	}
}

func TestHelpListsHelpKeyWhereItWorks(t *testing.T) {
	for _, c := range helpCases(t) {
		page := helpPage(t, c.model)
		keys := strings.Join(helpPageKeys(page), "\x00") + "\x00"
		if !strings.Contains("\x00"+keys, "\x00F1\x00") {
			t.Errorf("%s help lacks F1", c.name)
		}
		wantQ := map[string]bool{"inbox": true, "sidebar": true, "thread": true, "detail": true}[c.name]
		if hasQ := strings.Contains("\x00"+keys, "\x00?\x00"); hasQ != wantQ {
			t.Errorf("%s help lists ? = %v, want %v", c.name, hasQ, wantQ)
		}
	}
}

func TestInboxAskKeyInHelpAndFooterOnlyWithAsker(t *testing.T) {
	with := NewModel(nil, WithAgentAsker((&fakeAsker{}).ask))
	without := NewModel(nil)
	if _, ok := helpLineFor(helpPage(t, with), "a"); !ok {
		t.Error("inbox help lacks a with an asker")
	}
	if _, ok := helpLineFor(helpPage(t, without), "a"); ok {
		t.Error("inbox help lists a without an asker")
	}
	if line := with.footerLine(rowStyles{}, 300); !strings.Contains(line, "a Claude") {
		t.Errorf("inbox footer with an asker = %q", line)
	}
	if line := without.footerLine(rowStyles{}, 300); strings.Contains(line, "a Claude") {
		t.Errorf("inbox footer without an asker = %q", line)
	}
}

func TestHelpTitleNamesEveryCloseAndScrollKey(t *testing.T) {
	m := NewModel(nil)
	title := strings.Split(m.openHelp().helpView(), "\n")[0]
	for _, key := range []string{"j/k", "PgUp/PgDn", "Esc", "q"} {
		if !strings.Contains(title, key) {
			t.Errorf("help title %q lacks %q", title, key)
		}
	}
	updated, _ := m.openHelp().Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if updated.(Model).helpOpen {
		t.Error("q should close the help")
	}
}
