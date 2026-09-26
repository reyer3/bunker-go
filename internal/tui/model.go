package tui

import (
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/style"
)

// Model is the interactive shell. Its inbox is a bounded snapshot, not a
// complete conversation history.
type Model struct {
	client          Client
	groups          []inboxGroup
	counts          map[core.Channel]map[string]int
	selected        int
	loaded          bool
	loadErr         error
	detail          bool
	reading         bool
	readID          string
	readItem        core.Item
	readErr         error
	readToken       uint64
	width           int
	height          int
	polling         bool
	refreshPending  bool
	pollToken       uint64
	composing       bool
	previewing      bool
	sending         bool
	quitConfirm     bool
	draftID         string
	draftBody       string
	attachments     []string
	attaching       bool
	attachInput     string
	previewPlan     core.Plan
	replyErr        error
	replyToken      uint64
	marking         bool
	markLoading     bool
	markConfirm     bool
	markSending     bool
	markID          string
	markPlan        core.Plan
	markErr         error
	markToken       uint64
	activeTab       int
	tabSelected     [numTabs]int
	helpOpen        bool
	glyphs          map[core.Channel]string
	render          *lipgloss.Renderer
	now             func() time.Time
	blurred         bool
	notifyEnabled   bool
	notifyWriter    io.Writer
	tmuxPassthrough bool
	lastNotifyAt    time.Time
	pendingNotify   int
}

// numTabs is "Todo" plus one tab per channelOrder entry.
const numTabs = 1 + 3

func NewModel(client Client) Model {
	return Model{client: client, polling: client != nil, pollToken: 1}
}

// withGlyphs returns a copy of m using the given resolved channel glyphs
// (see internal/style.ResolveGlyphs) instead of the package defaults.
func (m Model) withGlyphs(glyphs map[core.Channel]string) Model {
	m.glyphs = glyphs
	return m
}

// clock returns the model's injectable clock, defaulting to time.Now so
// production code needs no wiring while tests pin a fixed time.
func (m Model) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// resolvedGlyphs returns the model's configured channel glyphs, falling
// back to the package defaults.
func (m Model) resolvedGlyphs() map[core.Channel]string {
	if m.glyphs != nil {
		return m.glyphs
	}
	return style.Glyphs
}

// renderer returns the model's lipgloss renderer, defaulting to one over
// io.Discard (which is never a *os.File, so termenv detects no color
// capability — a safe, deterministic default for anything that renders a
// Model without wiring a real terminal output, including most tests).
func (m Model) renderer() *lipgloss.Renderer {
	if m.render != nil {
		return m.render
	}
	return lipgloss.NewRenderer(io.Discard)
}

func (m Model) Init() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return loadInbox(m.client, m.pollToken)
}

// Run owns the Bubble Tea program but not the RPC connection; the caller
// closes that connection whether the terminal exits cleanly or with an
// error. It builds its lipgloss renderer from output (so color detection,
// including NO_COLOR, matches the real terminal Bubble Tea writes to) and
// loads [render.glyphs] config overrides the same way `bunker render`
// does; a missing/unreadable config keeps the package default glyphs.
func Run(client Client, input io.Reader, output io.Writer) error {
	glyphs := style.Glyphs
	var notify *bool
	if cfg, err := config.LoadDefault(); err == nil {
		glyphs = style.ResolveGlyphs(cfg.Render.Glyphs)
		notify = cfg.Tui.Notify
	}
	model := NewModel(client).withGlyphs(glyphs)
	model.render = lipgloss.NewRenderer(output)
	model.notifyEnabled = resolveNotifyEnabled(notify, os.Getenv)
	model.notifyWriter = output
	model.tmuxPassthrough = os.Getenv("TMUX") != ""
	_, err := tea.NewProgram(model, tea.WithInput(input), tea.WithOutput(output), tea.WithMouseCellMotion(), tea.WithReportFocus()).Run()
	return err
}
