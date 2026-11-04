package tui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// Answering a chat message (Alt+R): a message typed in a chat is a plain
// message, quoting nothing. Alt+R marks the selected message (or the
// newest, as with Alt+Y and Alt+F) as the one the next send answers: the
// send goes out as a reply to it and the mark is dropped afterwards, so
// the message after that is plain again.

// chatReplyKey marks the selected message as the one to answer.
const chatReplyKey = "alt+r"

// chatReplySnippetRunes bounds the quoted text shown in the indicator.
const chatReplySnippetRunes = 40

// chatReplyBlocked says why Responder cannot run now, or "".
func (m Model) chatReplyBlocked() string {
	it, ok := m.chatCopyTarget()
	switch {
	case !ok:
		return "no hay mensajes a los que responder"
	case it.Deleted:
		return "no se puede responder a un mensaje eliminado"
	case m.chatEditID != "":
		return "termina o cancela la edición antes de responder"
	case m.chatForward != nil:
		return "cancela el reenvío antes de responder"
	}
	return ""
}

// startChatReply is Alt+R in a chat.
func (m Model) startChatReply() (tea.Model, tea.Cmd) {
	if why := m.chatReplyBlocked(); why != "" {
		return m.noteUIError(errors.New(why)), nil
	}
	it, _ := m.chatCopyTarget()
	m.chatReplyTo = &it
	m.mediaErr = nil
	// The target now shows in the indicator, so the highlight goes:
	// Esc drops the reply, and a further Alt+Up starts a fresh selection.
	m.chatFocus = ""
	return m, nil
}

// cancelChatReply drops the reply target, if any.
func (m Model) cancelChatReply() Model {
	m.chatReplyTo = nil
	return m
}

// chatReplyLine is the indicator under the composer while a reply target
// is set.
func (m Model) chatReplyLine() (string, bool) {
	if m.chatReplyTo == nil {
		return "", false
	}
	return "↪ Respondiendo a " + chatReplyLabel(*m.chatReplyTo) + " · Esc cancela", true
}

// chatReplyLabel names whom a reply answers and quotes the start of the
// message, so the user sees what they picked.
func chatReplyLabel(it core.Item) string {
	who := "tu mensaje"
	if !it.FromMe {
		who = it.From.Name
		if who == "" {
			who = it.From.ID
		}
	}
	text := strings.TrimSpace(it.Body)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if text == "" && len(it.Attachments) > 0 {
		text = "adjunto: " + it.Attachments[0].Name
	}
	if text == "" {
		return who
	}
	return who + ": «" + truncate(text, chatReplySnippetRunes) + "»"
}
