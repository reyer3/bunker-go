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
	{"/", "filtrar", false},
	{"u", "deshacer", false},
	{"g", "refrescar", false},
	{"1/2/3", "secciones", false},
	{"Ctrl+K", "comandos", false},
	{"@", "contacto", false},
	{"c", "llamar", false},
	{"?", "ayuda", true},
}

// sidebarHints are the compact panel's keys (issue #81): few, since the
// line is about 30 cells wide.
var sidebarHints = []keyHint{
	{"q", "salir", true},
	{"↵", "abrir", false},
	{"/", "filtrar", false},
	{"Tab", "canal", false},
	{"g", "refrescar", false},
	{"Ctrl+K", "comandos", false},
	{"@", "contacto", false},
	{"c", "llamar", false},
	{"?", "ayuda", true},
}

var chatHints = []keyHint{
	{"↵", "enviar", false},
	{"Alt+↵", "nueva línea", false},
	{"Ctrl+O", "ver", false},
	{"Ctrl+V", "pegar imagen", false},
	{"Alt+E", "editar", false},
	{"Alt+X", "eliminar", false},
	{"Alt++", "reaccionar", false},
	{"Alt+C", "llamar", false},
	{"F2", "comandos", false},
	{"Alt+V", "grabar", false},
	{"Alt+P", "reproducir", false},
	{"Alt+↑↓", "elegir mensaje", false},
	{"Alt+Y", "copiar", false},
	{"F7", "seleccionar", false},
	{"Alt+O", "abrir adjunto", false},
	{"Esc", "volver", true},
	{"F1", "ayuda", true},
}

// composeHints, confirmSendHints and mailEditorHints (issue #38): every
// composer sends with Ctrl+S (a chat also with ↵), and every send is then
// confirmed with ↵ (or Ctrl+S again) from its preview.
var composeHints = []keyHint{
	{"Ctrl+S", "enviar", false},
	{"Ctrl+R", "adjuntar", false},
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
	{"Ctrl+K", "comandos", false},
	{"Alt+Y", "copiar", false},
	{"F7", "seleccionar", false},
	{"Alt+O", "abrir adjunto", false},
	{"Esc", "volver", true},
	{"q", "salir", false},
	{"?", "ayuda", true},
}

var detailHints = []keyHint{
	{"Esc", "volver", true},
	{"q", "salir", true},
	{"Ctrl+K", "comandos", false},
	{"?", "ayuda", true},
}

// helpSection is one titled block of the help overlay.
type helpSection struct {
	id, title string
	keys      [][2]string
}

// helpSections are the help overlay's pages: one per view, shown alone
// (plus the calls block during a call, see helpBody), so a key is
// explained once on the page where it works. The help, palette and quit
// keys are not listed here: helpFooter adds them once, as they apply.
var helpSections = []helpSection{
	{"inbox", "Bandeja", [][2]string{
		{"j/k, ↑/↓", "mover selección"},
		{"↵", "abrir"},
		{"←/→", "plegar/desplegar un remitente"},
		{"n", "nuevo mensaje (elegir contacto)"},
		{"@", "buscar contacto (en la pestaña WhatsApp o Matrix, solo de ese canal)"},
		{"c", "llamar a la conversación de WhatsApp seleccionada (vista previa y confirmación)"},
		{"J", "unirse a la próxima reunión con enlace (o clic en su fila de Reuniones)"},
		{"/", "filtrar por texto o consultar (F1 en el filtro lista los operadores)"},
		{"r", "responder"},
		{"m", "marcar leído (envía confirmación de lectura)"},
		{"u", "deshacer el último leído (vuelve a sin leer)"},
		{"g", "refrescar"},
		{"1/2/3", "enfocar Mail/WhatsApp/Matrix"},
		{"0", "volver a la vista general"},
		{"Tab/⇧Tab", "siguiente/anterior sección"},
		{"q", "salir"},
	}},
	{"query", "Consultas (/)", [][2]string{
		{"texto", "filtra al instante lo ya cargado"},
		{"from: to:", "remitente, destinatarios"},
		{"subject:", "asunto"},
		{"is:", "unread o read (sin leer, leído)"},
		{"has:", "attachment (con adjuntos)"},
		{"in:", "carpeta de correo (in:Archive)"},
		{"channel:", "mail, whatsapp o matrix"},
		{"account:", "cuenta"},
		{"label:", "etiqueta"},
		{"before:", "antes de AAAA-MM-DD, o 7d, 2w, 3m"},
		{"after:", "desde esa fecha"},
		{"\"a b\"", "frase exacta (from:\"Ana María\")"},
		{"-x", "excluir una palabra u operador"},
		{"↵", "con un operador, busca en todo el historial"},
		{"↓ al final", "cargar más resultados"},
		{"Esc", "volver a la bandeja"},
	}},
	{"sidebar", "Panel lateral", [][2]string{
		{"j/k, ↑/↓", "mover selección"},
		{"↵", "abrir (en herdr, en un panel a la derecha)"},
		{"Tab/⇧Tab", "siguiente/anterior canal"},
		{"1/2/3", "Mail/WhatsApp/Matrix"},
		{"0", "Todo"},
		{"/", "filtrar (Esc quita el filtro)"},
		{"@", "buscar contacto (en WhatsApp o Matrix, solo de ese canal)"},
		{"c", "llamar: en herdr avisa de usar Alt+C en el panel de la conversación"},
		{"J", "unirse a la próxima reunión (o clic en su fila de Reuniones)"},
		{"g", "refrescar"},
		{"q", "salir"},
	}},
	{"chat", "En un chat", [][2]string{
		{"↵ o Ctrl+S", "enviar: un texto se envía con un solo ↵ (antes pasa la vista previa); adjuntos, notas de voz, chats nuevos y ediciones piden un segundo ↵"},
		{"Alt+↵", "salto de línea"},
		{"PgUp/PgDn", "ver mensajes anteriores/siguientes"},
		{"clic", "sobre una imagen o video lo abre, sobre una nota de voz la reproduce, sobre otro mensaje lo selecciona (resaltado)"},
		{"Alt+↑ / Alt+↓", "seleccionar el mensaje anterior / siguiente sin ratón (Esc quita la selección)"},
		{"doble clic", "abrir el adjunto con el programa del sistema (BUNKER_OPEN_FILE)"},
		{"Ctrl+O", "ver la imagen / reproducir el video más reciente"},
		{"Ctrl+V", "adjuntar imagen del portapapeles"},
		{"arrastrar", "soltar archivos para adjuntarlos"},
		{"⌫ vacío", "quitar el último adjunto"},
		{":risa", "emoji (Tab elige, ↵ inserta)"},
		{"Ctrl+D", "descargar el último adjunto"},
		{"Alt+E", "editar tu último mensaje (Ctrl+S guarda, Esc cancela)"},
		{"Alt+X", "eliminar tu último mensaje para todos"},
		{"Alt++", "reaccionar al último mensaje recibido (0 quita)"},
		{"Alt+C", "llamar por voz (vista previa y confirmación; requiere calls = true)"},
		{"Alt+V", "grabar una nota de voz (↵ la envía tras la vista previa, Esc cancela)"},
		{"Alt+P", "reproducir la última nota de voz (Esc detiene)"},
		{"Alt+Y", "copiar el mensaje seleccionado al portapapeles (wl-copy, xclip, xsel u OSC 52)"},
		{"Alt+O", "abrir el adjunto del mensaje seleccionado"},
		{"F7 o Alt+S", "modo selección: suelta el ratón para seleccionar texto (Esc/F7 vuelve; Shift+arrastrar también suele servir)"},
		{"", "sin mensaje seleccionado, las acciones actúan sobre el último"},
		{"", "? y Ctrl+K se escriben en el mensaje: la ayuda es F1 y la paleta F2"},
		{"Esc", "quitar la selección, o volver a la bandeja"},
	}},
	{"thread", "Hilo de correo", [][2]string{
		{"j/k", "mover entre mensajes"},
		{"↵", "expandir/colapsar"},
		{"r / R", "responder / responder a todos"},
		{"f", "reenviar"},
		{"d", "descargar adjunto"},
		{"Alt+O", "abrir el adjunto (o doble clic en su línea)"},
		{"Alt+Y", "copiar el cuerpo del mensaje al portapapeles"},
		{"F7 o Alt+S", "modo selección: suelta el ratón para seleccionar texto"},
		{"PgUp/PgDn", "desplazar"},
		{"Esc", "volver"},
		{"q", "salir"},
	}},
	{"detail", "Mensaje", [][2]string{
		{"j/k, ↑/↓", "desplazar"},
		{"PgUp/PgDn", "desplazar una página"},
		{"G", "ir al final"},
		{"r", "responder"},
		{"m", "marcar leído (envía confirmación de lectura)"},
		{"Alt+Y", "copiar el mensaje al portapapeles"},
		{"F7 o Alt+S", "modo selección: suelta el ratón para seleccionar texto"},
		{"g", "refrescar la bandeja"},
		{"Esc", "volver a la bandeja"},
		{"q", "salir"},
	}},
	{"editor", "Redactar (correo o respuesta)", [][2]string{
		{"Tab/⇧Tab", "siguiente/anterior campo (correo)"},
		{"Ctrl+S", "enviar: primero la vista previa, que ↵ o Ctrl+S confirman"},
		{"Ctrl+R / Ctrl+X", "adjuntar una ruta / quitar el último adjunto (respuesta)"},
		{"Ctrl+V", "adjuntar imagen del portapapeles (respuesta)"},
		{"arrastrar", "soltar archivos para adjuntarlos (respuesta)"},
		{"PgUp/PgDn", "desplazar el borrador"},
		{"Esc", "cerrar y guardar el borrador"},
	}},
	{"preview", "Vista previa del envío", [][2]string{
		{"↵ o Ctrl+S", "enviar de verdad"},
		{"Esc", "volver a editar (no envía)"},
		{"q", "dos veces: salir sin enviar (respuesta)"},
	}},
	{"picker", "Nuevo mensaje (n) y buscar contacto (@)", [][2]string{
		{"escribir", "filtrar contactos"},
		{"↑/↓", "elegir"},
		{"↵ o clic", "abrir"},
		{"Alt+C", "llamar al contacto de WhatsApp elegido (vista previa y confirmación)"},
		{"Esc", "cancelar"},
	}},
	{"palette", "Paleta de comandos (Ctrl+K)", [][2]string{
		{"Ctrl+K o F2", "abrir desde la bandeja, un mensaje, un hilo o el panel lateral"},
		{"", "desde un chat solo F2 (allí Ctrl+K borra la línea)"},
		{"escribir", "filtrar comandos y conversaciones (sin acentos)"},
		{"↑/↓", "elegir (también Ctrl+P/Ctrl+N)"},
		{"↵", "ejecutar, como su tecla, o abrir la conversación"},
		{"@", "buscar un contacto para un mensaje nuevo"},
		{"atenuado", "no disponible aquí; dice por qué"},
		{"Esc", "cerrar"},
	}},
}

// helpKeyColumn is the width of the help overlay's key column; a longer
// key still keeps two spaces before its description.
const helpKeyColumn = 13

// helpContext names the help section for what is on screen.
func (m Model) helpContext() string {
	switch {
	case m.palette != nil:
		return "palette"
	case m.chatMode:
		return "chat"
	case m.previewing, m.mailComposing && m.mailPreviewing:
		return "preview"
	case m.mailComposing, m.composing:
		return "editor"
	case m.threadMode:
		return "thread"
	case m.picker != nil:
		return "picker"
	case m.filtering:
		return "query"
	case m.detail:
		return "detail"
	case m.sidebar:
		return "sidebar"
	}
	return "inbox"
}

// askHelpKeys are the ask key's help lines per section (issue #82),
// shown only when an agent asker is wired.
var askHelpKeys = map[string][2]string{
	"inbox":   {"a", "preguntar a Claude (herdr) por la conversación"},
	"sidebar": {"a", "preguntar a Claude (herdr) por la conversación"},
	"chat":    {"Alt+A", "preguntar a Claude (herdr) por la conversación"},
	"thread":  {"a", "preguntar a Claude (herdr) por el mensaje"},
	"detail":  {"a", "preguntar a Claude (herdr) por el mensaje"},
}

// helpPlainKeys are the sections where plain keys are commands, so ? opens
// the help and Ctrl+K the palette; elsewhere a text field takes them.
var helpPlainKeys = map[string]bool{"inbox": true, "sidebar": true, "thread": true, "detail": true}

// helpOptions is what a help page depends on besides its section.
type helpOptions struct {
	ask bool
	// call is the call block's answer, reject and hang-up keys, empty
	// with no call ringing or live.
	answer, reject, hangup string
	ringing, live          bool
}

// helpFooter is the page's help, palette and quit keys, listed once.
func helpFooter(ctx string) [][2]string {
	quit := [2]string{"Ctrl+C", "salir de bunker desde cualquier vista"}
	switch {
	case helpPlainKeys[ctx]:
		return [][2]string{{"? o F1", "esta ayuda"}, {"Ctrl+K o F2", "paleta de comandos"}, quit}
	case ctx == "chat":
		return [][2]string{{"F1", "esta ayuda"}, {"F2", "paleta de comandos"}, quit}
	}
	return [][2]string{{"F1", "esta ayuda"}, quit}
}

// helpCallKeys is the calls block for a ringing or live call: only its
// keys in the view under the help (plain or Alt), joined so they never
// read as a second meaning of the view's own a, x or Alt+A.
func helpCallKeys(o helpOptions) [][2]string {
	var keys [][2]string
	if o.ringing {
		keys = append(keys, [2]string{o.answer + "/" + strings.TrimPrefix(o.reject, "Alt+"), "contestar/rechazar la llamada que suena (prioridad sobre sus otros usos)"})
	}
	if o.live {
		keys = append(keys, [2]string{o.hangup, "colgar la llamada en curso"})
	}
	return append(keys, [2]string{"", "la franja de llamada ocupa la última línea de cualquier vista"})
}

func helpSectionFor(ctx string) helpSection {
	for _, s := range helpSections {
		if s.id == ctx {
			return s
		}
	}
	return helpSections[0]
}

// helpBody renders the page for ctx: its section (with the ask key when
// o.ask), the calls block during a call, and the help/palette footer.
func helpBody(ctx string, o helpOptions) []string {
	s := helpSectionFor(ctx)
	keys := append([][2]string(nil), s.keys...)
	if k, ok := askHelpKeys[s.id]; ok && o.ask {
		keys = append(keys, k)
	}
	lines := append([]string{s.title}, helpKeyLines(keys)...)
	if o.ringing || o.live {
		lines = append(lines, "", "Llamadas de voz")
		lines = append(lines, helpKeyLines(helpCallKeys(o))...)
	}
	lines = append(lines, "")
	return append(lines, helpKeyLines(helpFooter(s.id))...)
}

func helpKeyLines(keys [][2]string) []string {
	lines := make([]string, len(keys))
	for i, k := range keys {
		pad := max(2, helpKeyColumn-runewidth.StringWidth(k[0]))
		lines[i] = "  " + k[0] + strings.Repeat(" ", pad) + k[1]
	}
	return lines
}

// helpOptions reads the page's options from the view under the help.
func (m Model) helpOptions() helpOptions {
	under := m
	under.helpOpen = false
	o := helpOptions{ask: m.agentAsk != nil}
	o.answer, o.reject, o.hangup = under.callKeyNames()
	o.ringing = m.ringingCall() != nil
	o.live = m.liveCall() != nil
	return o
}

func (m Model) helpPage() []string {
	ctx := m.helpCtx
	if ctx == "" {
		ctx = m.helpContext()
	}
	return helpBody(ctx, m.helpOptions())
}

// openHelp shows the overlay with the current view's page, from its top.
func (m Model) openHelp() Model {
	m.helpCtx = m.helpContext()
	m.helpScroll = 0
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
	body := m.helpPage()
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

// helpTitle is the overlay's fixed first line: how to scroll and close.
const helpTitle = "Ayuda · j/k, PgUp/PgDn desplazar · Esc, q o ? cerrar"

// helpView renders the overlay: a fixed title line, then a window of
// the page that scrolls to fit the pane.
func (m Model) helpView() string {
	body := m.helpPage()
	if h := m.helpBodyHeight(); h > 0 && len(body) > h {
		start := min(m.helpScroll, len(body)-h)
		body = body[start : start+h]
	}
	lines := append([]string{helpTitle}, body...)
	if m.width > 0 {
		for i, line := range lines {
			lines[i] = runewidth.Truncate(line, m.width, "…")
		}
	}
	return strings.Join(lines, "\n")
}
