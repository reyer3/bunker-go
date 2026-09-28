package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// Undo and drafts (issue #37). Marking read stays previewed and confirmed:
// on WhatsApp it sends the other side a read receipt, an outbound action.
// What changes is that it can be undone: every mark-read (m, or opening a
// chat or mail thread) is remembered, and u puts the last one back in the
// unread inbox. Where the channel cannot mark unread, that happens in
// bunker only, and the notice says so. Drafts survive leaving a chat or
// a composer, and come back when it is reopened.

// UnreadMarker is the optional capability u needs; the RPC client and the
// TUI's query client implement it.
type UnreadMarker interface {
	MarkUnread(ctx context.Context, id string) (localOnly bool, err error)
}

// readUndoLimit bounds how many mark-reads u can walk back.
const readUndoLimit = 20

// flashDuration is how long a notice stays on the status line.
const flashDuration = 6 * time.Second

type unreadDoneMsg struct {
	id    string
	local bool
	err   error
}

func markUnreadCmd(client Client, id string) tea.Cmd {
	return func() tea.Msg {
		marker, ok := client.(UnreadMarker)
		if !ok {
			return unreadDoneMsg{id: id, err: fmt.Errorf("el daemon no permite marcar como no leído: %w", core.ErrUnsupported)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), previewTimeout)
		defer cancel()
		local, err := marker.MarkUnread(ctx, id)
		return unreadDoneMsg{id: id, local: local, err: err}
	}
}

// rememberRead records a mark-read so u can undo it.
func (m Model) rememberRead(id string) Model {
	if id == "" {
		return m
	}
	undo := append(append([]string(nil), m.readUndo...), id)
	if len(undo) > readUndoLimit {
		undo = undo[len(undo)-readUndoLimit:]
	}
	m.readUndo = undo
	return m
}

// undoRead marks the last mark-read unread again.
func (m Model) undoRead() (Model, tea.Cmd) {
	if len(m.readUndo) == 0 || m.client == nil {
		return m.withFlash("no hay nada que deshacer"), nil
	}
	id := m.readUndo[len(m.readUndo)-1]
	m.readUndo = m.readUndo[:len(m.readUndo)-1]
	return m, markUnreadCmd(m.client, id)
}

func (m Model) handleUnreadDone(msg unreadDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.withFlash("no se pudo marcar como no leído: " + humanError(msg.err)), nil
	}
	notice := "vuelve a estar sin leer"
	if msg.local {
		notice += " (solo en bunker: el canal no lo permite y la confirmación de lectura ya se envió)"
	}
	m = m.withFlash(notice)
	if m.polling {
		m.refreshPending = true
		return m, nil
	}
	return m.startPollModel()
}

// startPollModel is startPoll returning the concrete Model.
func (m Model) startPollModel() (Model, tea.Cmd) {
	next, cmd := m.startPoll()
	return next.(Model), cmd
}

// withFlash shows a short notice on the status line.
func (m Model) withFlash(text string) Model {
	m.flash = text
	m.flashAt = m.clock()
	return m
}

// currentFlash is the notice still worth showing, if any.
func (m Model) currentFlash() (string, bool) {
	if m.flash == "" || m.clock().Sub(m.flashAt) > flashDuration {
		return "", false
	}
	return m.flash, true
}

// Drafts are kept per target for the session.

func chatDraftKey(channel core.Channel, account, thread string) string {
	return "chat\x00" + string(channel) + "\x00" + account + "\x00" + thread
}

func replyDraftKey(id string) string { return "reply\x00" + id }

// mailDraft is the full mail editor's state worth keeping.
type mailDraft struct {
	to, cc, subject, body string
}

func mailDraftKey(action, targetID string) string { return action + "\x00" + targetID }

// keepDraft stores text under key, or forgets key when text is blank.
// It reports whether a draft was kept.
func (m Model) keepDraft(key, text string) (Model, bool) {
	if strings.TrimSpace(text) == "" {
		if m.drafts != nil {
			delete(m.drafts, key)
		}
		return m, false
	}
	if m.drafts == nil {
		m.drafts = map[string]string{}
	}
	m.drafts[key] = text
	return m, true
}

// restoreMailDraft refills the mail editor from a draft kept for the same
// action and target, if any.
func (m Model) restoreMailDraft() Model {
	d, ok := m.mailDrafts[mailDraftKey(m.mailAction, m.mailTargetID)]
	if !ok {
		return m
	}
	m.mailTo.SetValue(d.to)
	m.mailCc.SetValue(d.cc)
	m.mailSubject.SetValue(d.subject)
	m.composer.SetValue(d.body)
	return m
}
