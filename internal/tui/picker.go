package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/reyer3/bunker-go/internal/core"
)

// New conversation (issue #26): "n" opens a contact picker. Typing
// filters the daemon's contacts (core.Service.Contacts); Enter or a click
// opens the chat with that contact even when there is no history yet, or,
// for mail, the full editor with the address in To. Sending then goes
// through the usual dry-run preview and confirm.
//
// "@" opens the same picker as a contact search: on the WhatsApp or Matrix
// tab it is scoped to that channel, and Alt+C on a WhatsApp contact opens
// its chat and starts the call action (preview, then confirm).

// ContactsClient is the optional capability the picker needs: the RPC
// client and the TUI's query client implement it.
type ContactsClient interface {
	Contacts(ctx context.Context, filter core.ContactFilter) ([]core.Contact, error)
}

// pickerLimit bounds how many matches the picker asks for and shows.
const pickerLimit = 50

// pickerHeaderLines are the lines above the first result: the title,
// the query line and a rule. Mouse clicks map through it.
const pickerHeaderLines = 3

type contactPicker struct {
	// channel, when set, scopes the search to one channel; title heads
	// the picker; notice says why Alt+C did nothing.
	channel  core.Channel
	title    string
	notice   string
	query    string
	results  []core.Contact
	selected int
	loading  bool
	err      error
	token    uint64
}

type contactsLoadedMsg struct {
	token    uint64
	contacts []core.Contact
	err      error
}

func loadContactsCmd(client Client, channel core.Channel, query string, token uint64) tea.Cmd {
	return func() tea.Msg {
		lister, ok := client.(ContactsClient)
		if !ok {
			return contactsLoadedMsg{token: token, err: fmt.Errorf("el daemon no lista contactos: %w", core.ErrUnsupported)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		contacts, err := lister.Contacts(ctx, core.ContactFilter{Channel: channel, Query: query, Limit: pickerLimit})
		return contactsLoadedMsg{token: token, contacts: contacts, err: err}
	}
}

// openPicker shows the contact picker with every contact, newest query
// results replacing older ones as the user types.
func (m Model) openPicker() (Model, tea.Cmd) {
	return m.openPickerFor("", "Nuevo mensaje")
}

// openSearchPicker is "@": the contact picker as a search, scoped to the
// focused tab's channel on the WhatsApp and Matrix tabs and open to every
// channel from the overview and Mail (where it is the same as "n").
func (m Model) openSearchPicker() (Model, tea.Cmd) {
	channel, ok := m.currentChannelFilter()
	if !ok || channel == core.ChannelMail {
		return m.openPickerFor("", "Buscar contacto")
	}
	return m.openPickerFor(channel, "Buscar contacto en "+channelLabel(channel))
}

func (m Model) openPickerFor(channel core.Channel, title string) (Model, tea.Cmd) {
	if m.client == nil {
		return m, nil
	}
	token := uint64(1)
	if m.picker != nil {
		token = m.picker.token + 1
	}
	m.picker = &contactPicker{loading: true, token: token, channel: channel, title: title}
	return m, loadContactsCmd(m.client, channel, "", token)
}

func (m Model) refilterPicker() (Model, tea.Cmd) {
	p := *m.picker
	p.token++
	p.loading = true
	p.selected = 0
	m.picker = &p
	return m, loadContactsCmd(m.client, p.channel, p.query, p.token)
}

func (m Model) handleContactsLoaded(msg contactsLoadedMsg) (tea.Model, tea.Cmd) {
	if m.picker == nil || msg.token != m.picker.token {
		return m, nil
	}
	p := *m.picker
	p.loading = false
	p.results, p.err = msg.contacts, msg.err
	if p.selected >= len(p.results) {
		p.selected = 0
	}
	m.picker = &p
	return m, nil
}

func (m Model) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := *m.picker
	switch msg.String() {
	case "esc":
		m.picker = nil
		m.forwardPick = nil
		return m, nil
	case "enter":
		if p.selected >= 0 && p.selected < len(p.results) {
			return m.pickTarget(p.results[p.selected])
		}
		return m, nil
	case callPlaceKey:
		if m.forwardPick != nil {
			return m, nil
		}
		return m.callPickedContact()
	case "up", "ctrl+p", "shift+tab":
		p.notice = ""
		if p.selected > 0 {
			p.selected--
		}
	case "down", "ctrl+n", "tab":
		p.notice = ""
		if p.selected < len(p.results)-1 {
			p.selected++
		}
	case "backspace":
		if p.query == "" {
			return m, nil
		}
		r := []rune(p.query)
		p.query = string(r[:len(r)-1])
		p.notice = ""
		m.picker = &p
		return m.refilterPicker()
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			p.query += string(msg.Runes)
			p.notice = ""
			if msg.Type == tea.KeySpace {
				p.query += " "
			}
			m.picker = &p
			return m.refilterPicker()
		}
	}
	m.picker = &p
	return m, nil
}

func (m Model) updatePickerMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	p := *m.picker
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if p.selected > 0 {
			p.selected--
		}
	case tea.MouseButtonWheelDown:
		if p.selected < len(p.results)-1 {
			p.selected++
		}
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		i := msg.Y - pickerHeaderLines + m.pickerOffset()
		if msg.Y >= pickerHeaderLines && i >= 0 && i < len(p.results) {
			return m.pickTarget(p.results[i])
		}
	}
	m.picker = &p
	return m, nil
}

// callPickedContact is Alt+C on the highlighted contact: open its chat and
// start the chat's call action, which previews the call and waits for
// ↵. A contact that cannot be called says why in the picker and stays.
func (m Model) callPickedContact() (tea.Model, tea.Cmd) {
	p := *m.picker
	if p.selected < 0 || p.selected >= len(p.results) {
		p.notice = "no hay un contacto seleccionado"
		m.picker = &p
		return m, nil
	}
	c := p.results[p.selected]
	if err := m.callBlockedFor(c.Channel, c.Address); err != nil {
		p.notice = humanError(err)
		m.picker = &p
		return m, nil
	}
	next, open := m.pickContact(c)
	after, call := next.(Model).startChatCall()
	return after, tea.Batch(open, call)
}

// pickTarget is a pick in the picker: the forward's target while Alt+F
// is choosing one, else the contact to open.
func (m Model) pickTarget(c core.Contact) (tea.Model, tea.Cmd) {
	if m.forwardPick != nil {
		return m.pickForwardTarget(c)
	}
	return m.pickContact(c)
}

// pickContact opens the chosen contact: the chat view for chat channels,
// with an empty history when there is none yet, or the mail editor with
// the address in To.
func (m Model) pickContact(c core.Contact) (tea.Model, tea.Cmd) {
	m.picker = nil
	if c.Channel == core.ChannelMail {
		m = m.openNewMail(c)
		return m, nil
	}
	thread := c.Thread
	if thread == "" {
		thread = c.Address
	}
	m, cmd := m.openChat(core.Item{Channel: c.Channel, Account: c.Account, Thread: thread, ThreadName: c.Name, From: core.Address{Name: c.Name}})
	m.chatNewTo = c.Address
	return m, cmd
}

// openNewMail opens K6's editor for a fresh message to c: no item to
// reply to, the cursor on the subject.
func (m Model) openNewMail(c core.Contact) Model {
	m.mailComposing = true
	m.mailAction = "new"
	m.mailTargetID = ""
	m.mailChannel = c.Channel
	m.mailAccount = c.Account
	m.mailThread = ""
	m.mailTo = newLineEditor()
	m.mailCc = newLineEditor()
	m.mailSubject = newLineEditor()
	m.mailAttachInfo = nil
	m.mailPreviewing = false
	m.mailSending = false
	m.mailSendErr = nil
	m.mailPlan = core.Plan{}
	m.composer = newComposer(m.width, m.renderer())
	m.mailTo.SetValue(c.Address)
	m.mailFocus = 2
	m = m.restoreMailDraft()
	return m.withMailFocusApplied()
}

// pickerRows is how many results fit under the header and above the
// footer hint.
func (m Model) pickerRows() int {
	if m.height <= 0 {
		return pickerLimit
	}
	reserved := 1
	if m.picker != nil && m.picker.notice != "" {
		reserved++
	}
	return max(1, m.height-pickerHeaderLines-reserved)
}

// pickerOffset scrolls the result list so the selection stays visible.
func (m Model) pickerOffset() int {
	rows := m.pickerRows()
	if m.picker == nil || m.picker.selected < rows {
		return 0
	}
	return m.picker.selected - rows + 1
}

func (m Model) pickerView() string {
	p := m.picker
	title := p.title
	if title == "" {
		title = "Nuevo mensaje"
	}
	lines := []string{
		title,
		"Para: " + p.query + "▏",
		strings.Repeat("─", max(1, min(m.width, 40))),
	}
	glyphs := m.resolvedGlyphs()
	switch {
	case p.err != nil:
		lines = append(lines, "Error: "+humanError(p.err))
	case p.loading && len(p.results) == 0:
		lines = append(lines, "buscando…")
	case len(p.results) == 0:
		lines = append(lines, "sin coincidencias")
	default:
		offset, rows := m.pickerOffset(), m.pickerRows()
		for i := offset; i < len(p.results) && i < offset+rows; i++ {
			c := p.results[i]
			marker := "  "
			if i == p.selected {
				marker = "▸ "
			}
			name := c.Name
			if name == "" {
				name = c.Address
			}
			line := fmt.Sprintf("%s%s %s  %s/%s", marker, glyphs[c.Channel], safeLine(name), c.Channel, c.Account)
			lines = append(lines, line)
		}
	}
	if p.notice != "" {
		lines = append(lines, "No se puede llamar: "+p.notice)
	}
	if m.forwardPick != nil {
		lines = append(lines, "↵ reenviar · ↑/↓ elegir · Esc cancelar")
	} else {
		lines = append(lines, "↵ abrir · ↑/↓ elegir · Esc cancelar · Alt+C llamar")
	}
	if m.width > 0 {
		for i, line := range lines {
			lines[i] = runewidth.Truncate(line, m.width, "…")
		}
	}
	return strings.Join(lines, "\n")
}
