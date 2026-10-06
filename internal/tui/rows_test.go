package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/style"
)

// TestBuildRowWidthMathHandlesRealWorldText covers the width-math edge
// cases real accounts hit: emoji, accented text, a very long subject, and
// a custom glyph override in the Supplementary Private Use Area (U+100000,
// go-runewidth width 1) configured via [render.glyphs]. Every produced
// line, selected or not, at narrow and wide widths, must stay within its
// budget: go-runewidth must be the single source of truth for width, not
// len()/range byte or rune counting, and not lipgloss's own (different)
// width calculation.
func TestBuildRowWidthMathHandlesRealWorldText(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	counts := map[core.Channel]map[string]int{
		core.ChannelMail: {"cl": 1, "com": 1}, // two accounts: exercise the account tag
	}
	glyphs := map[core.Channel]string{
		core.ChannelMail: "\U00100000", // custom Supplementary PUA glyph override
	}
	group := inboxGroup{items: []core.Item{{
		ID:        "mail:cl:1",
		Channel:   core.ChannelMail,
		Account:   "cl",
		Subject:   "ANÁLISIS DE LOS COMPROMISOS DE ACME: reunión de turno 🚚🚧 con acentos añadidos y emojis 🎉",
		From:      core.Address{Name: "Alice Doe 🧑‍💻"},
		Body:      "Adjunto el detalle por turno con acentos: revisión, gestión, año 🚚",
		Unread:    true,
		Timestamp: now,
	}}}

	for _, profile := range []termenv.Profile{termenv.Ascii, termenv.TrueColor} {
		renderer := lipgloss.NewRenderer(io.Discard)
		renderer.SetColorProfile(profile)
		styles := newRowStyles(renderer, style.DefaultPalette())

		for _, width := range []int{20, 24, 36, 40, 60, 100} {
			for _, selected := range []bool{false, true} {
				line1, line2 := buildRow(group, selected, width, glyphs, counts, styles, now, "")
				if w := runewidth.StringWidth(stripANSI(line1)); w > width {
					t.Errorf("profile=%v width=%d selected=%v: line1 %q is %d cells wide", profile, width, selected, line1, w)
				}
				if line2 != "" {
					if w := runewidth.StringWidth(stripANSI(line2)); w > width {
						t.Errorf("profile=%v width=%d selected=%v: line2 %q is %d cells wide", profile, width, selected, line2, w)
					}
				}
			}
		}
	}
}

// TestInboxDegradesGracefullyForNoColorAndNarrowWidth covers G1's two
// degrade rules: an Ascii color profile (what termenv resolves NO_COLOR
// to; see github.com/muesli/termenv's own NO_COLOR handling, which this
// package relies on rather than re-implementing) must produce zero ANSI
// escapes anywhere in the inbox view, and a width under narrowWidth must
// fall back to one line per conversation (no dim preview line).
func TestInboxDegradesGracefullyForNoColorAndNarrowWidth(t *testing.T) {
	client := &inboxClient{
		items:  []core.Item{{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Subject: "Hola", Unread: true}},
		counts: map[core.Channel]map[string]int{core.ChannelMail: {"cl": 1}},
	}
	model := NewModel(client)
	renderer := lipgloss.NewRenderer(io.Discard)
	renderer.SetColorProfile(termenv.Ascii)
	model.render = renderer

	updated, _ := model.Update(model.Init()())
	updated, _ = updated.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	view := updated.View()
	if strings.Contains(view, "\x1b") {
		t.Errorf("Ascii/NO_COLOR profile leaked an ANSI escape:\n%q", view)
	}

	narrow, _ := model.Update(model.Init()())
	narrow, _ = narrow.Update(tea.WindowSizeMsg{Width: narrowWidth - 1, Height: 20})
	narrowView := narrow.View()
	if strings.Count(narrowView, "\n") == 0 {
		t.Fatal("narrow view has no content to check")
	}
	for _, line := range strings.Split(narrowView, "\n") {
		if runewidth.StringWidth(line) > narrowWidth-1 {
			t.Errorf("narrow line %q exceeds width %d", line, narrowWidth-1)
		}
	}
	// The one-line fallback drops the dim "Sender: body" preview: the
	// row line and its own would-be preview line collapse into one.
	line1, line2 := buildRow(inboxGroup{items: client.items}, false, narrowWidth-1, style.Glyphs, client.counts, newRowStyles(renderer, style.DefaultPalette()), time.Now(), "")
	if line2 != "" {
		t.Errorf("narrow width still produced a second preview line: %q / %q", line1, line2)
	}
}

// TestFooterKeepsQVisibleAtNarrowWidth pins bunker-tui.md's follow-up gap:
// at a narrow width (40 columns), the inbox footer's plain ellipsis
// truncation must never cut away the "q" quit hint the way it silently
// did before ("q" was not even in the untruncated string).
func TestFooterKeepsQVisibleAtNarrowWidth(t *testing.T) {
	renderer := lipgloss.NewRenderer(io.Discard)
	styles := newRowStyles(renderer, style.DefaultPalette())

	got := stripANSI(footerLine(styles, 40))
	if !strings.Contains(got, "q") {
		t.Fatalf("footer at width 40 = %q, want it to keep \"q\" visible", got)
	}
	if w := runewidth.StringWidth(got); w > 40 {
		t.Fatalf("footer at width 40 = %q, is %d cells wide, want <= 40", got, w)
	}
}

// stripANSI removes SGR escape sequences so a colored render's plain
// width can be measured with go-runewidth (which does not parse ANSI).
// Only used by tests: buildRow itself must never truncate colored text.
// stripANSI removes every escape sequence (CSI color/style, OSC 8
// hyperlinks, etc.), reusing the production skipEscape logic (sanitize.go)
// rather than a CSI-only reimplementation, so it strips exactly what
// wrapView treats as zero-width.
func stripANSI(s string) string {
	var out []rune
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\x1b' {
			i = skipEscape(runes, i)
			continue
		}
		out = append(out, runes[i])
	}
	return string(out)
}

// TestLayoutSectionRowsRespectsShareWithMixedRowHeights covers a case
// sender-groups.md introduces that never existed before it: a Mail
// section can now mix 1-line (collapsed sender) and 2-line (an expanded
// sender's thread row) units in the very same render, whereas every row
// used to share one uniform height per render. layoutSectionRows must
// still never render more physical lines than its share, even when the
// units it is given are not all the same height.
func TestLayoutSectionRowsRespectsShareWithMixedRowHeights(t *testing.T) {
	styles := newRowStyles(lipgloss.NewRenderer(io.Discard), style.DefaultPalette())
	units := []rowUnit{
		{lines: []string{"sender0"}},                      // collapsed sender: 1 line
		{lines: []string{"sender1"}},                      // collapsed sender: 1 line
		{lines: []string{"thread line1", "thread line2"}}, // an expanded sender's thread: 2 lines
		{lines: []string{"sender2"}},                      // collapsed sender: 1 line
	}
	const share = 4
	shown := layoutSectionRows(units, share, 0, 1, styles, 40)
	total := 0
	for _, u := range shown {
		total += len(u.lines)
	}
	if total > share {
		t.Fatalf("layoutSectionRows rendered %d physical lines into a %d-line share with mixed row heights: %+v", total, share, shown)
	}
}
