package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// Launch options (issue #81): herdr runs bunker in two kinds of pane, a
// narrow list ("bunker sidebar") and one conversation ("bunker open"),
// and both are the same Model started in a different mode rather than
// separate programs, so every view and key behaves as in the full TUI.

// Option configures a Model at start (see NewModel and Run).
type Option func(*Model)

// WithSidebar starts in the compact layout for a narrow pane (sidebar.go).
func WithSidebar() Option {
	return func(m *Model) { m.sidebar = true }
}

// WithExternalOpener makes Enter on a conversation call open with its
// item id instead of opening it in place. The caller decides where it
// goes (cmd/bunker opens a herdr pane); a nil open keeps Enter in place.
func WithExternalOpener(open func(id string) error) Option {
	return func(m *Model) { m.externalOpen = open }
}

// WithOpenItem starts on the conversation of item id instead of the
// inbox, and makes leaving that conversation quit: the pane was opened
// for it alone.
func WithOpenItem(id string) Option {
	return func(m *Model) { m.openID = strings.TrimSpace(id) }
}

// openItemLoadedMsg carries the item "bunker open" starts on.
type openItemLoadedMsg struct {
	id   string
	item core.Item
	err  error
}

// readOpenItem fetches the start item without a read receipt: opening
// the conversation marks it read the same way Enter does.
func readOpenItem(client Client, id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		item, err := client.Read(ctx, id, false)
		return openItemLoadedMsg{id: id, item: item, err: err}
	}
}

func (m Model) handleOpenItemLoaded(msg openItemLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.id != m.openID || m.detail {
		return m, nil
	}
	if msg.err != nil {
		m.openErr = msg.err
		return m, nil
	}
	if msg.item.ID == "" {
		m.openErr = fmt.Errorf("tui: open %s: %w", msg.id, core.ErrNotFound)
		return m, nil
	}
	return m.openConversation(msg.item)
}

// openPending reports whether this is a "bunker open" pane still waiting
// for its conversation, or showing why it could not open it.
func (m Model) openPending() bool {
	return m.openID != "" && !m.detail
}

// updateOpenPending handles keys before the conversation is on screen:
// only quitting means anything there.
func (m Model) updateOpenPending(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "?":
		return m.openHelp(), nil
	}
	return m, nil
}

// openPendingView is the "bunker open" pane before its conversation shows.
func (m Model) openPendingView() string {
	line := "Abriendo conversación…"
	if m.openErr != nil {
		line = "No se pudo abrir la conversación: " + humanError(m.openErr)
	}
	return wrapView(line+"\n\n"+hintLine(m.width, keyHint{"q", "salir", true}), m.width)
}

// quitOpenChat leaves the chat of a "bunker open" pane and then quits,
// in order, so the daemon hears the presence and typing go away before
// the program exits.
func (m Model) quitOpenChat() tea.Cmd {
	return tea.Sequence(leaveChatCmd(m.client, m.chatChannel, m.chatAccount, m.chatThread), tea.Quit)
}

// leaveHints relabels a view's "Esc volver" hint in a "bunker open" pane,
// where Esc closes the pane instead of going back to an inbox.
func (m Model) leaveHints(hints []keyHint) []keyHint {
	if m.openID == "" {
		return hints
	}
	out := append([]keyHint(nil), hints...)
	for i := range out {
		if out[i].key == "Esc" && out[i].label == "volver" {
			out[i].label = "cerrar"
		}
	}
	return out
}

// externalOpenDoneMsg reports how handing a conversation to the external
// opener went.
type externalOpenDoneMsg struct {
	err error
}

// openExternally runs the opener off the update loop: it starts a herdr
// process, which must not freeze the list while it runs.
func openExternally(open func(string) error, id string) tea.Cmd {
	return func() tea.Msg {
		return externalOpenDoneMsg{err: open(id)}
	}
}

func (m Model) handleExternalOpenDone(msg externalOpenDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.withFlash("no se pudo abrir: " + humanError(msg.err)), nil
	}
	return m, nil
}
