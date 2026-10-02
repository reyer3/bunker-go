package tui

import (
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
	"github.com/reyer3/bunker-go/internal/core"
)

func TestPreviewTextLabelsWhatTheMessageCarries(t *testing.T) {
	voice := core.Attachment{Name: "audio", MIME: "audio/ogg", Voice: true, Duration: 12}
	cases := []struct {
		name string
		item core.Item
		want string
	}{
		{"text wins", core.Item{Body: "hola\nmundo", Attachments: []core.Attachment{voice}}, "hola mundo"},
		{"voice with duration", core.Item{Attachments: []core.Attachment{voice}}, "🎤 Nota de voz 0:12"},
		{"voice without duration", core.Item{Attachments: []core.Attachment{{Voice: true}}}, "🎤 Nota de voz"},
		{"image", core.Item{Attachments: []core.Attachment{{Name: "image", MIME: "image/jpeg"}}}, "📷 Foto"},
		{"video", core.Item{Attachments: []core.Attachment{{Name: "video", MIME: "video/mp4"}}}, "🎥 Video"},
		{"sticker", core.Item{Attachments: []core.Attachment{{Name: "sticker", MIME: "image/webp"}}}, "Sticker"},
		{"audio file", core.Item{Attachments: []core.Attachment{{Name: "song.mp3", MIME: "audio/mpeg"}}}, "🎵 Audio"},
		{"file", core.Item{Attachments: []core.Attachment{{Name: "informe.pdf", MIME: "application/pdf"}}}, "📎 informe.pdf"},
		{"nameless file", core.Item{Attachments: []core.Attachment{{MIME: "application/pdf"}}}, "📎 Archivo"},
		{"deleted", core.Item{Deleted: true, Body: "viejo", Attachments: []core.Attachment{voice}}, "🚫 Mensaje eliminado"},
		{"call", core.Item{Body: "Llamada de voz", Meta: map[string]string{"wa_call": "incoming"}}, "📞 Llamada"},
		{"controls stripped", core.Item{Body: "a\x1b[31mb\x07\tc\r\nd"}, "ab c d"},
		{"empty", core.Item{}, ""},
	}
	for _, tc := range cases {
		if got := previewText(tc.item); got != tc.want {
			t.Errorf("%s: previewText = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPreviewLineUsesLabelsAndTu(t *testing.T) {
	voice := core.Item{From: core.Address{Name: "Ana"}, Attachments: []core.Attachment{{Voice: true, Duration: 75}}}
	if got := previewLine(voice, 40); got != "Ana: 🎤 Nota de voz 1:15" {
		t.Fatalf("previewLine = %q", got)
	}
	own := core.Item{FromMe: true, From: core.Address{Name: "Yo"}, Body: "listo"}
	if got := previewLine(own, 40); got != "Tú: listo" {
		t.Fatalf("own previewLine = %q", got)
	}
}

func TestChatPreviewOmitsTheSenderOfAOneToOne(t *testing.T) {
	item := core.Item{From: core.Address{Name: "Ana"}, Body: "hola"}
	if got := chatPreview(item, "Ana"); got != "hola" {
		t.Fatalf("1:1 preview = %q, want only the text", got)
	}
	if got := chatPreview(item, "Equipo"); got != "Ana: hola" {
		t.Fatalf("group preview = %q", got)
	}
	item.FromMe = true
	if got := chatPreview(item, "Ana"); got != "Tú: hola" {
		t.Fatalf("own preview = %q", got)
	}
	item.FromMe, item.From.Name = false, "34600112233@s.whatsapp.net"
	if got := chatPreview(item, "Equipo"); got != "hola" {
		t.Fatalf("raw-id sender preview = %q, want no sender", got)
	}
}

func TestSenderPreviewIsNewestSubjectAndSnippet(t *testing.T) {
	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	newest := core.Item{ID: "mail:cl:2", Subject: "Factura", Body: "Adjunto\nel pago", Timestamp: at}
	older := core.Item{ID: "mail:cl:1", Subject: "Viejo", Timestamp: at.Add(-time.Hour)}
	s := senderGroup{threads: []inboxGroup{{items: []core.Item{newest}}, {items: []core.Item{older}}}}
	if got := senderPreview(s); got != "Factura — Adjunto el pago" {
		t.Fatalf("senderPreview = %q", got)
	}
	s.threads[0].items[0].Body = ""
	if got := senderPreview(s); got != "Factura" {
		t.Fatalf("senderPreview without body = %q", got)
	}
}

// sidebarPreviewModel is a loaded sidebar of unread conversations, one
// per item, on the active tab's overview.
func sidebarPreviewModel(width, height int, items ...core.Item) Model {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	m := NewModel(nil, WithSidebar()).withGlyphs(nil)
	m.render = r
	m.now = func() time.Time { return at }
	m.loaded = true
	m.width, m.height = width, height
	for _, it := range items {
		it.Timestamp = at
		it.Unread = true
		m.groups = append(m.groups, inboxGroup{items: []core.Item{it}})
	}
	return m
}

func waItem(id, thread, name, from, body string) core.Item {
	return core.Item{ID: "whatsapp:p:" + id, Channel: core.ChannelWhatsApp, Account: "p", Thread: thread,
		ThreadName: name, From: core.Address{Name: from}, Body: body}
}

// rowLines returns the plain text and row index of every row line.
func rowLines(m Model) (text []string, rows []int, y []int) {
	lines, hits := m.sidebarLinesAndHits()
	for i, h := range hits {
		if h.kind == hitRow {
			text = append(text, strings.TrimRight(stripANSI(lines[i]), " "))
			rows = append(rows, h.row)
			y = append(y, i)
		}
	}
	return text, rows, y
}

func TestSidebarShowsAPreviewUnderEachRow(t *testing.T) {
	own := waItem("3", "c2", "Luis", "Luis", "ok")
	own.FromMe = true
	m := sidebarPreviewModel(34, 24,
		waItem("1", "g1", "Equipo", "Bob", "nos vemos"),
		waItem("2", "c1", "", "Ana", "hola"),
		own,
	)
	m.selected = 2
	text, rows, _ := rowLines(m)
	want := []string{"  󰖣 Equipo", "    Bob: nos vemos", "  󰖣 Ana", "    hola", "▌ 󰖣 Luis", "▌   Tú: ok"}
	if len(text) != len(want) {
		t.Fatalf("row lines = %q, want %q", text, want)
	}
	for i := range want {
		if !strings.HasPrefix(text[i], strings.TrimRight(want[i], " ")) {
			t.Errorf("line %d = %q, want prefix %q", i, text[i], want[i])
		}
	}
	// Both lines of a row are the same click target.
	if !slices.Equal(rows, []int{0, 0, 1, 1, 2, 2}) {
		t.Fatalf("hit rows = %v, want 0,0,1,1,2,2", rows)
	}
}

func TestSidebarClickOnEitherLineSelectsItsRow(t *testing.T) {
	m := sidebarPreviewModel(34, 24,
		waItem("1", "g1", "Equipo", "Bob", "a"),
		waItem("2", "g2", "Otro", "Ana", "b"),
	)
	_, _, ys := rowLines(m)
	for _, y := range ys[2:] { // both lines of the second row
		updated, _ := m.Update(tea.MouseMsg{Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		if got := updated.(Model).selected; got != 1 {
			t.Fatalf("click on line %d selected %d, want 1", y, got)
		}
	}
}

func TestSidebarNeverOverflowsAndKeepsSelectionVisible(t *testing.T) {
	var items []core.Item
	for i := range 12 {
		n := string(rune('A' + i))
		items = append(items, waItem(n, "t"+n, "Chat "+n, "Ana", "texto"))
	}
	for _, height := range []int{8, 9, 10, 12, 15, 20, 40} {
		m := sidebarPreviewModel(34, height, items...)
		for sel := range 12 {
			m.selected = sel
			lines, hits := m.sidebarLinesAndHits()
			if len(lines) > height {
				t.Fatalf("height %d, selected %d: %d lines overflow the pane", height, sel, len(lines))
			}
			visible := false
			for _, h := range hits {
				if h.kind == hitRow && h.row == sel {
					visible = true
				}
			}
			if !visible {
				t.Fatalf("height %d: selected row %d is not visible", height, sel)
			}
			for _, l := range lines {
				if w := runewidth.StringWidth(stripANSI(l)); w > 34 {
					t.Fatalf("line %q is %d cells wide", stripANSI(l), w)
				}
			}
		}
	}
}

func TestSidebarDropsPreviewsBeforeRowsInATinyPane(t *testing.T) {
	m := sidebarPreviewModel(34, 8, waItem("1", "g1", "Equipo", "Bob", "hola"), waItem("2", "g2", "Otro", "Ana", "b"))
	text, rows, _ := rowLines(m)
	if len(rows) == 0 {
		t.Fatal("no rows at all")
	}
	for _, l := range text {
		if strings.Contains(l, "hola") {
			t.Fatalf("preview shown in a tiny pane: %q", l)
		}
	}
	if len(rows) != len(slices.Compact(slices.Clone(rows))) {
		t.Fatalf("rows span several lines without previews: %v", rows)
	}
}

func TestSidebarSelectionCoversBothLines(t *testing.T) {
	m := sidebarPreviewModel(34, 24, waItem("1", "g1", "Equipo", "Bob", "hola"))
	lines, hits := m.sidebarLinesAndHits()
	n := 0
	for i, h := range hits {
		if h.kind != hitRow {
			continue
		}
		n++
		if !strings.Contains(lines[i], "▌") || !strings.Contains(lines[i], "48;2;27;42;60") {
			t.Errorf("line %d of the selected row lacks the bar/background: %q", n, lines[i])
		}
		if w := runewidth.StringWidth(stripANSI(lines[i])); w != 34 {
			t.Errorf("selected line %d is %d cells, want the full pane (34)", n, w)
		}
	}
	if n != 2 {
		t.Fatalf("selected row spans %d lines, want 2", n)
	}
}

func TestSidebarPreviewTruncatesWideRunes(t *testing.T) {
	m := sidebarPreviewModel(20, 24,
		waItem("1", "g1", "Equipo", "Bob", strings.Repeat("😀", 12)),
		waItem("2", "g2", "Otro", "Ana", strings.Repeat("日本語", 6)),
	)
	text, _, _ := rowLines(m)
	for _, l := range text {
		if w := runewidth.StringWidth(l); w > 20 {
			t.Errorf("%q is %d cells wide, pane is 20", l, w)
		}
	}
	if !strings.HasSuffix(text[1], "…") || !strings.HasSuffix(text[3], "…") {
		t.Fatalf("truncated previews lack the ellipsis: %q", text)
	}
}

func TestSidebarCollapsedMailSenderPreviewsNewestSubject(t *testing.T) {
	mail := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
		From: core.Address{ID: "alice@example.com", Name: "Alice"}, Subject: "Reunión de mañana"}
	m := sidebarPreviewModel(34, 24, mail)
	text, _, _ := rowLines(m)
	if len(text) != 2 || !strings.HasPrefix(text[0], "▌ ▸ Alice") || text[1] != "▌   Reunión de mañana" {
		t.Fatalf("collapsed sender row = %q", text)
	}
}

func TestInboxCollapsedMailSenderHasPreviewLine(t *testing.T) {
	mail := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
		From: core.Address{ID: "alice@example.com", Name: "Alice"}, Subject: "Reunión de mañana"}
	m := sidebarPreviewModel(60, 24, mail)
	m.sidebar = false
	if out := stripANSI(m.inboxView()); !strings.Contains(out, "  Reunión de mañana") {
		t.Fatalf("inbox sender row lacks the preview:\n%s", out)
	}
	m.width = narrowWidth - 1
	if strings.Contains(stripANSI(m.inboxView()), "Reunión") {
		t.Fatal("a narrow inbox must stay one line per row")
	}
}
