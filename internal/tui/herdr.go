package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
)

// herdr integration (issue #82). The TUI knows nothing about herdr: the
// caller (cmd/bunker) injects plain functions that talk to it, so this
// package never runs a process and tests pass fakes.

// PollInterval is how often the inbox polls the daemon. It is exported so
// the caller can size what outlives one poll, such as the TTL of the
// unread count it reports to herdr.
const PollInterval = pollInterval

// ErrAgentBlocked is what an agent asker returns (wrapped) when the agent
// is waiting for the user's answer to a question of its own: a new prompt
// would not reach it, so the panel says where to look instead.
var ErrAgentBlocked = errors.New("tui: agent is waiting for an answer")

const (
	// askTimeout bounds one "ask the agent" round trip: it lists, prompts
	// and focuses through herdr, and a wedged herdr must not leave the
	// key stuck on "asking".
	askTimeout = 15 * time.Second
	// messageNotifyInterval is the least time between two new-message
	// notifications handed to the injected notifier. herdr shows them as
	// toasts with a sound, so a burst of messages coalesces into one
	// "N mensajes nuevos" instead of a toast per poll.
	messageNotifyInterval = 30 * time.Second
	// askHintLabel is the "a" hint's label, short for the narrow sidebar.
	askHintLabel = "Claude"
	// chatAskKey is the ask key in a chat view, where "a" is typed text.
	chatAskKey = "alt+a"
)

// WithAgentAsker enables "a" (Alt+A in a chat, where "a" is text): it
// hands the conversation's item id to ask, which cmd/bunker implements
// by prompting a Claude Code agent running in herdr. A nil ask leaves
// the key and its hints out.
func WithAgentAsker(ask func(ctx context.Context, itemID string) error) Option {
	return func(m *Model) { m.agentAsk = ask }
}

// WithUnreadReporter calls report with the inbox's unread total after
// every successful poll, off the update loop. The caller throttles and
// does the I/O; an error is shown once, until a report succeeds again,
// so a broken herdr does not flash on every poll.
func WithUnreadReporter(report func(n int) error) Option {
	return func(m *Model) { m.unreadReport = report }
}

// WithMessageNotifier routes new-message notifications to notify (a
// herdr toast) instead of the terminal's OSC 777, with body
// "<remitente>: <texto>" or "N mensajes nuevos", at most one every
// messageNotifyInterval. Unlike OSC 777 it fires whether or not the
// pane has focus: herdr panes are small and usually unfocused, and
// whether herdr forwards focus events to a pane is not something bunker
// can rely on.
func WithMessageNotifier(notify func(body string) error) Option {
	return func(m *Model) { m.messageNotify = notify }
}

// agentAskDoneMsg reports how asking the agent went.
type agentAskDoneMsg struct {
	err error
}

// askItemID is the item "a" asks about: the conversation a "bunker open"
// pane was opened for, the selected message of a mail thread, the newest
// message of a chat, else what "r" would reply to.
func (m Model) askItemID() (string, bool) {
	if m.openID != "" {
		return m.openID, true
	}
	if m.threadMode && m.threadSelected >= 0 && m.threadSelected < len(m.threadItems) {
		return m.threadItems[m.threadSelected].ID, true
	}
	if m.chatMode && len(m.chatItems) > 0 {
		return m.chatItems[len(m.chatItems)-1].ID, true
	}
	return m.selectedItemID()
}

// askAgent starts asking the agent about the current conversation. It is
// a no-op without an asker, without an item, or while a previous ask is
// still running.
func (m Model) askAgent() (tea.Model, tea.Cmd) {
	if m.agentAsk == nil || m.asking {
		return m, nil
	}
	id, ok := m.askItemID()
	if !ok || id == "" {
		return m, nil
	}
	m.asking = true
	ask := m.agentAsk
	return m.withFlash("preguntando a Claude…"), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), askTimeout)
		defer cancel()
		return agentAskDoneMsg{err: ask(ctx, id)}
	}
}

func (m Model) handleAgentAskDone(msg agentAskDoneMsg) (tea.Model, tea.Cmd) {
	m.asking = false
	switch {
	case errors.Is(msg.err, ErrAgentBlocked):
		return m.withFlash("Claude está esperando tu respuesta en su panel"), nil
	case msg.err != nil:
		return m.withFlash("no se pudo preguntar a Claude: " + humanError(msg.err)), nil
	}
	return m.withFlash("enviado a Claude"), nil
}

// withAskHint adds the ask key's hint after the view's Enter hint (or
// first, when it has none), only when an asker is wired. It is unpinned,
// so a narrow line drops it before the ways to leave and get help.
func (m Model) withAskHint(hints []keyHint, key string) []keyHint {
	if m.agentAsk == nil {
		return hints
	}
	at := 0
	for i, h := range hints {
		if h.key == "↵" {
			at = i + 1
			break
		}
	}
	out := make([]keyHint, 0, len(hints)+1)
	out = append(out, hints[:at]...)
	out = append(out, keyHint{key, askHintLabel, false})
	return append(out, hints[at:]...)
}

// unreadReportedMsg reports how handing the unread total to the
// reporter went.
type unreadReportedMsg struct {
	err error
}

// reportUnread hands the unread total of the latest poll to the
// reporter, off the update loop (it runs herdr).
func (m Model) reportUnread() tea.Cmd {
	if m.unreadReport == nil {
		return nil
	}
	report, n := m.unreadReport, m.sidebarTabCount(0)
	return func() tea.Msg {
		return unreadReportedMsg{err: report(n)}
	}
}

func (m Model) handleUnreadReported(msg unreadReportedMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil {
		m.unreadReportFailed = false
		return m, nil
	}
	if m.unreadReportFailed {
		return m, nil
	}
	m.unreadReportFailed = true
	return m.withFlash("no se pudo avisar a herdr de los no leídos: " + humanError(msg.err)), nil
}

// messageNotifiedMsg reports how handing a notification to the injected
// notifier went.
type messageNotifiedMsg struct {
	err error
}

// messageNotifyBody is a notification's text: "<remitente>: <texto>" for
// one message (the sender resolved as for OSC 777, never a raw id; the
// text cut to notifyBodyWidth cells), or a count for several.
func messageNotifyBody(fresh []core.Item, pendingExtra int) string {
	if total := len(fresh) + pendingExtra; total > 1 {
		return fmt.Sprintf("%d mensajes nuevos", total)
	}
	sender, body := notificationText(fresh, pendingExtra)
	body = runewidth.Truncate(strings.TrimSpace(safeLine(body)), notifyBodyWidth, "…")
	if body == "" {
		return safeLine(sender)
	}
	return safeLine(sender) + ": " + body
}

// maybeNotifyMessage is maybeNotify's path when a notifier is injected:
// the same new-message detection (newUnreadItems) and coalescing
// (shouldNotifyEvery), a slower rate, and no focus requirement.
func (m Model) maybeNotifyMessage(wasLoaded bool, oldGroups []inboxGroup, newItems []core.Item) (Model, tea.Cmd) {
	if !wasLoaded {
		return m, nil
	}
	fresh := newUnreadItems(oldGroups, newItems)
	if len(fresh) == 0 {
		return m, nil
	}
	now := m.clock()
	pendingBefore := m.pendingMessageNotify
	fire, pending := shouldNotifyEvery(messageNotifyInterval, m.lastMessageNotifyAt, pendingBefore, len(fresh), now)
	m.pendingMessageNotify = pending
	if !fire {
		return m, nil
	}
	m.lastMessageNotifyAt = now
	notify, body := m.messageNotify, messageNotifyBody(fresh, pendingBefore)
	return m, func() tea.Msg {
		return messageNotifiedMsg{err: notify(body)}
	}
}

func (m Model) handleMessageNotified(msg messageNotifiedMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil {
		m.messageNotifyFailed = false
		return m, nil
	}
	if m.messageNotifyFailed {
		return m, nil
	}
	m.messageNotifyFailed = true
	return m.withFlash("no se pudo notificar en herdr: " + humanError(msg.err)), nil
}
