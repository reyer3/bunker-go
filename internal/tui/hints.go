package tui

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// Key hints (issue #36): every view's bottom line lists its own keys in
// one notation ("key label", joined by " · "). When the line does not fit,
// the lowest-priority hints are dropped, never wrapped; pinned hints (how
// to leave and how to get help) always stay.

type keyHint struct {
	key, label string
	pinned     bool
}

const hintSep = " · "

// hintLine renders hints in order, dropping unpinned ones from the end
// until the line fits width (0 means no limit). If even the pinned ones
// do not fit, the line is truncated.
func hintLine(width int, hints ...keyHint) string {
	kept := append([]keyHint(nil), hints...)
	for width > 0 && runewidth.StringWidth(joinHints(kept)) > width {
		drop := -1
		for i := len(kept) - 1; i >= 0; i-- {
			if !kept[i].pinned {
				drop = i
				break
			}
		}
		if drop < 0 {
			break
		}
		kept = append(kept[:drop], kept[drop+1:]...)
	}
	return truncatePlain(joinHints(kept), width)
}

func joinHints(hints []keyHint) string {
	parts := make([]string, len(hints))
	for i, h := range hints {
		parts[i] = h.key + " " + h.label
	}
	return strings.Join(parts, hintSep)
}

// inboxHints are the inbox's keys, highest priority first after the
// pinned "q salir" (kept first so it is never cut, see footerLine).
var inboxHints = []keyHint{
	{"q", "salir", true},
	{"↵", "abrir", false},
	{"n", "nuevo", false},
	{"r", "responder", false},
	{"m", "leído", false},
	{"u", "deshacer", false},
	{"g", "refrescar", false},
	{"1/2/3", "secciones", false},
	{"?", "ayuda", true},
}

var chatHints = []keyHint{
	{"↵", "enviar", false},
	{"Alt+↵", "nueva línea", false},
	{"Ctrl+O", "ver", false},
	{"Ctrl+V", "pegar imagen", false},
	{"Esc", "volver", true},
	{"F1", "ayuda", true},
}

// composeHints, confirmSendHints and mailEditorHints (issue #38): every
// composer sends with Ctrl+S (a chat also with ↵), and every send is then
// confirmed with ↵ (or Ctrl+S again) from its preview.
var composeHints = []keyHint{
	{"Ctrl+S", "enviar", false},
	{"Ctrl+A", "adjuntar", false},
	{"Ctrl+X", "quitar adjunto", false},
	{"Esc", "cerrar", true},
}

var confirmSendHints = []keyHint{
	{"↵", "enviar", true},
	{"Esc", "editar", true},
	{"q", "salir", false},
}

var mailEditorHints = []keyHint{
	{"Tab", "campo", false},
	{"Ctrl+S", "enviar", false},
	{"Esc", "cerrar", true},
}

var threadHints = []keyHint{
	{"j/k", "mover", false},
	{"↵", "expandir", false},
	{"r", "responder", false},
	{"R", "a todos", false},
	{"f", "reenviar", false},
	{"d", "descargar", false},
	{"Esc", "volver", true},
	{"?", "ayuda", true},
}

var detailHints = []keyHint{
	{"Esc", "volver", true},
	{"q", "salir", true},
	{"?", "ayuda", true},
}

// helpSection is one titled block of the help overlay.
type helpSection struct {
	id, title string
	keys      [][2]string
}

var helpSections = []helpSection{
	{"inbox", "Bandeja", [][2]string{
		{"j/k, ↑/↓", "mover selección"},
		{"↵", "abrir"},
		{"←/→", "plegar/desplegar un remitente"},
		{"n", "nuevo mensaje (elegir contacto)"},
		{"r", "responder"},
		{"m", "marcar leído (envía confirmación de lectura)"},
		{"u", "deshacer el último leído (vuelve a sin leer)"},
		{"g", "refrescar"},
		{"1/2/3", "enfocar Mail/WhatsApp/Matrix"},
		{"0", "volver a la vista general"},
		{"Tab/⇧Tab", "siguiente/anterior sección"},
		{"q", "salir"},
	}},
	{"chat", "En un chat", [][2]string{
		{"↵ o Ctrl+S", "enviar (con vista previa)"},
		{"Alt+↵", "salto de línea"},
		{"PgUp/PgDn", "ver mensajes anteriores/siguientes"},
		{"Ctrl+O", "ver imagen / reproducir video"},
		{"clic", "abrir la imagen o video bajo el cursor"},
		{"Ctrl+V", "adjuntar imagen del portapapeles"},
		{"arrastrar", "soltar archivos para adjuntarlos"},
		{"⌫ vacío", "quitar el último adjunto"},
		{":risa", "emoji (Tab elige, ↵ inserta)"},
		{"Ctrl+D", "descargar el último adjunto"},
		{"F1", "esta ayuda (? se escribe en el mensaje)"},
		{"Esc", "volver a la bandeja"},
	}},
	{"thread", "Hilo de correo", [][2]string{
		{"j/k", "mover entre mensajes"},
		{"↵", "expandir/colapsar"},
		{"r / R", "responder / responder a todos"},
		{"f", "reenviar"},
		{"d", "descargar adjunto"},
		{"PgUp/PgDn", "desplazar"},
		{"Esc", "volver"},
	}},
	{"editor", "Redactar (correo o respuesta)", [][2]string{
		{"Tab/⇧Tab", "siguiente/anterior campo"},
		{"Ctrl+S", "enviar (primero la vista previa)"},
		{"↵ o Ctrl+S", "confirmar el envío en la vista previa"},
		{"Ctrl+A/X", "adjuntar / quitar adjunto (respuesta)"},
		{"Esc", "cerrar y guardar el borrador"},
	}},
	{"picker", "Nuevo mensaje (n)", [][2]string{
		{"escribir", "filtrar contactos"},
		{"↑/↓", "elegir"},
		{"↵ o clic", "abrir"},
		{"Esc", "cancelar"},
	}},
}

// helpKeyColumn is the width of the help overlay's key column.
const helpKeyColumn = 12

// helpContext names the help section for what is on screen.
func (m Model) helpContext() string {
	switch {
	case m.chatMode:
		return "chat"
	case m.mailComposing, m.composing, m.previewing:
		return "editor"
	case m.threadMode:
		return "thread"
	case m.picker != nil:
		return "picker"
	}
	return "inbox"
}

// helpBody renders every help section and reports where the section for
// ctx starts, so the overlay can open on it.
func helpBody(ctx string) ([]string, int) {
	var lines []string
	start := 0
	for i, s := range helpSections {
		if i > 0 {
			lines = append(lines, "")
		}
		if s.id == ctx {
			start = len(lines)
		}
		lines = append(lines, s.title)
		for _, k := range s.keys {
			pad := max(1, helpKeyColumn-runewidth.StringWidth(k[0]))
			lines = append(lines, "  "+k[0]+strings.Repeat(" ", pad)+k[1])
		}
	}
	return lines, start
}

// openHelp shows the overlay on the current view's section.
func (m Model) openHelp() Model {
	m.helpCtx = m.helpContext()
	_, m.helpScroll = helpBody(m.helpCtx)
	m.helpOpen = true
	return m.clampHelpScroll()
}

func (m Model) helpBodyHeight() int {
	if m.height <= 0 {
		return 0
	}
	return max(1, m.height-1)
}

func (m Model) clampHelpScroll() Model {
	body, _ := helpBody(m.helpCtx)
	h := m.helpBodyHeight()
	if h == 0 {
		m.helpScroll = 0
		return m
	}
	m.helpScroll = clampScroll(m.helpScroll, len(body), h)
	return m
}

func (m Model) updateHelp(key string) Model {
	switch key {
	case "esc", "?", "f1", "q":
		m.helpOpen = false
		return m
	case "j", "down":
		m.helpScroll++
	case "k", "up":
		m.helpScroll--
	case "pgdown", " ":
		m.helpScroll += max(1, m.helpBodyHeight()-1)
	case "pgup":
		m.helpScroll -= max(1, m.helpBodyHeight()-1)
	case "g", "home":
		m.helpScroll = 0
	}
	return m.clampHelpScroll()
}

// helpView renders the overlay: a fixed title line, then a window of
// the sections that scrolls to fit the pane.
func (m Model) helpView() string {
	body, _ := helpBody(m.helpCtx)
	if h := m.helpBodyHeight(); h > 0 && len(body) > h {
		start := min(m.helpScroll, len(body)-h)
		body = body[start : start+h]
	}
	lines := append([]string{"Ayuda · j/k desplazar · Esc cerrar"}, body...)
	if m.width > 0 {
		for i, line := range lines {
			lines[i] = runewidth.Truncate(line, m.width, "…")
		}
	}
	return strings.Join(lines, "\n")
}
