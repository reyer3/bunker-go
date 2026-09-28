package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
)

// composerHeight is the fixed number of visible draft rows; bubbles
// textarea scrolls internally once the draft grows past it.
const composerHeight = 6

// composerFallbackWidth is used before the terminal has reported its real
// size (tea.WindowSizeMsg arrives after Init, so the very first "r"/chat
// compose could otherwise see width 0).
const composerFallbackWidth = 40

// newComposer returns a bubbles/textarea configured the same way for every
// bunker composer (K4): the mail reply composer and the chat composer both
// build one from here, so cursor movement, word-wrap and paste behave
// identically everywhere text is drafted.
//
// It disables the widget's own chrome (line numbers, the "┃ " prompt) and
// coloring: every other bunker view stays plain text piped through
// m.renderer()/wrapView, and the composer matches that instead of adding
// always-on colors of its own. The cursor is pinned to cursor.CursorStatic
// so it never blinks on its own tea.Tick — a blinking cursor would make
// teatest golden captures depend on scheduler timing instead of key input,
// the same discipline the tui-ghostty doc pins the clock and color profile
// for.
func newComposer(width int, renderer *lipgloss.Renderer) textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.FocusedStyle = textarea.Style{}
	ta.BlurredStyle = textarea.Style{}
	ta.Cursor.SetMode(cursor.CursorStatic)
	if renderer == nil {
		renderer = lipgloss.DefaultRenderer()
	}
	// Reverse video (SGR 7) marks the cursor cell without depending on any
	// color, so it survives NO_COLOR the same way the rest of bunker's
	// plain-text views do. Binding it to the model's own renderer (instead
	// of the package-default one) keeps it consistent with every other
	// styled element in this package and deterministic in tests, which
	// build their renderer over io.Discard.
	ta.Cursor.Style = renderer.NewStyle().Reverse(true)
	ta.Cursor.TextStyle = renderer.NewStyle()
	if width <= 0 {
		width = composerFallbackWidth
	}
	ta.SetWidth(width)
	ta.SetHeight(composerHeight)
	ta.Focus()
	return ta
}

// chatComposerMaxHeight is the K7 chat composer's max grown height (in
// visible rows) before bubbles/textarea starts scrolling internally
// instead of growing further; it starts at 1 line and grows with the
// draft's own line count (see Model.resizeChatComposer).
const chatComposerMaxHeight = 3

// chatComposerPlaceholder is shown when the chat draft is empty (K7).
const chatComposerPlaceholder = "Escribe un mensaje…"

// newChatComposer returns the K7 chat view's docked composer: the same
// shared textarea newComposer configures (cursor, focus, no built-in
// chrome), but starting at a single visible row instead of K4's fixed
// composerHeight — the chat composer grows up to chatComposerMaxHeight as
// the draft gains lines (Model.resizeChatComposer) instead of always
// reserving room for composerHeight lines the doc never asked for here —
// plus a placeholder, since an empty docked composer with no draft yet
// must still read as "type here". width is the docked box's OWN content
// width, already reduced by the caller for the rounded border chat_view.go
// draws around it (2 cells), so the border's total footprint never
// exceeds the terminal width.
func newChatComposer(width int, renderer *lipgloss.Renderer) textarea.Model {
	ta := newComposer(width, renderer)
	ta.Placeholder = chatComposerPlaceholder
	ta.SetHeight(1)
	return ta
}

// attachmentChips renders every draft attachment as one bracketed inline
// tag on a single line (the "attachments as chips" decision in
// conversation-view.md), instead of one bulleted "- name (size)" row per
// attachment. Names and stat errors are sanitized with safeLine, same as
// every other rendered path/error in this package.
func attachmentChips(paths []string) string {
	chips := make([]string, 0, len(paths))
	for _, path := range paths {
		name, size, err := statAttachment(path)
		if err != nil {
			chips = append(chips, fmt.Sprintf("[%s: %s]", safeLine(path), safeLine(err.Error())))
			continue
		}
		chips = append(chips, fmt.Sprintf("[%s (%d bytes)]", safeLine(name), size))
	}
	return strings.Join(chips, " ")
}
