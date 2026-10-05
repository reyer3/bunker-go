package tui

import (
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// replyAttachKey opens the reply's attachment path prompt. Not Ctrl+A:
// that is a common multiplexer prefix (herdr, screen, tmux setups) and
// readline's line start, which the composer keeps. Ctrl+R is free in
// herdr (prefix keys only), Ghostty (no plain Ctrl+letter defaults),
// tmux and Zellij defaults, bubbles/textarea and every other bunker view.
const replyAttachKey = "ctrl+r"

// addReplyAttachments appends paths to the reply, skipping ones already
// attached.
func (m Model) addReplyAttachments(paths ...string) Model {
	next := append([]string(nil), m.attachments...)
	for _, p := range paths {
		if !slices.Contains(next, p) {
			next = append(next, p)
		}
	}
	m.attachments = next
	m.replyErr = nil
	return m
}

// addReplyClipboardImage attaches an image pasted with Ctrl+V.
func (m Model) addReplyClipboardImage(msg clipboardImageMsg) Model {
	if msg.err != nil {
		m.replyErr = msg.err
		return m
	}
	m.replyTempFiles = append(m.replyTempFiles, msg.path)
	return m.addReplyAttachments(msg.path)
}

// clearReplyAttachments drops the reply's attachments and deletes its
// pasted temp images: after a send, or when the composer closes.
func (m Model) clearReplyAttachments() Model {
	for _, p := range m.replyTempFiles {
		os.Remove(p)
	}
	m.attachments = nil
	m.replyTempFiles = nil
	return m
}

// updateCompose handles keys while drafting a reply. Every key is literal
// text except the handful of compose control keys; this is what lets "q"
// and "r" be typed into the draft instead of triggering quit/reply again.
func (m Model) updateCompose(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Paste {
		// Files dropped on the terminal arrive as a pasted list of their
		// paths, as in the chat: attach them instead of typing them.
		if paths, ok := parseDroppedPaths(string(msg.Runes)); ok {
			return m.addReplyAttachments(paths...), nil
		}
	}
	switch msg.String() {
	case "esc":
		if next, kept := m.keepDraft(replyDraftKey(m.draftID), m.composer.Value()); kept {
			m = next.withFlash("borrador guardado")
		} else {
			m = next
		}
		m.composing = false
		m.draftID = ""
		m.composer.Reset()
		m = m.clearReplyAttachments()
		m.attaching = false
		m.attachInput = ""
		m.replyErr = nil
		return m, nil
	case replyAttachKey:
		m.attaching = true
		m.attachInput = ""
		m.replyErr = nil
		return m, nil
	case "ctrl+v":
		// Like the chat (chat_attach.go): paste an image from the
		// clipboard as an attachment; text pastes arrive bracketed.
		if m.clipboard == nil {
			return m, nil
		}
		return m, pasteClipboardImageCmd(m.clipboard)
	case "ctrl+x":
		if n := len(m.attachments); n > 0 {
			last := m.attachments[n-1]
			m.attachments = append([]string(nil), m.attachments[:n-1]...)
			m.replyTempFiles = removeTempFile(m.replyTempFiles, last)
		}
		return m, nil
	case "ctrl+s":
		if err := validateAttachments(m.attachments); err != nil {
			m.replyErr = err
			return m, nil
		}
		m.replyErr = nil
		m.replyToken++
		attachments := append([]string(nil), m.attachments...)
		return m, previewReply(m.client, m.draftID, m.composer.Value(), attachments, m.replyToken)
	case "pgdown", "pgup":
		// Text-input keys win in compose (j/k and the plain arrows already
		// reach the composer below as cursor movement, which auto-scrolls
		// it): PgUp/PgDown are the only scroll keys that need explicit
		// handling here, since bubbles/textarea binds neither by default.
		// There is no public API to move its internal viewport without
		// moving the cursor, so a "page" is composerHeight CursorUp/
		// CursorDown steps — the same movement the up/down arrows already
		// do, just composerHeight of them at once.
		for i := 0; i < composerHeight; i++ {
			if msg.String() == "pgdown" {
				m.composer.CursorDown()
			} else {
				m.composer.CursorUp()
			}
		}
		return m, nil
	}
	// Every other key (including "enter" for a newline, arrows/Home/End
	// for cursor movement, backspace/delete, and paste) is handled by the
	// shared bubbles/textarea composer itself (see composer.go), which is
	// what lets "q"/"r" stay literal draft text while still supporting
	// real cursor positioning instead of only ever appending at the end.
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	return m, cmd
}

// updateAttach handles keys while typing a local attachment path. Every key
// is literal path text (spaces allowed, no shell involved) except Esc and
// Enter; Enter validates the path immediately so a bad path is visible
// before it ever reaches a preview or send.
func (m Model) updateAttach(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.attaching = false
		m.attachInput = ""
		return m, nil
	case "enter":
		path := strings.TrimSpace(m.attachInput)
		m.attaching = false
		m.attachInput = ""
		if path == "" {
			return m, nil
		}
		if _, _, err := statAttachment(path); err != nil {
			m.replyErr = err
			return m, nil
		}
		m.replyErr = nil
		m.attachments = append(m.attachments, path)
		return m, nil
	case "backspace":
		if m.attachInput != "" {
			_, size := utf8.DecodeLastRuneInString(m.attachInput)
			m.attachInput = m.attachInput[:len(m.attachInput)-size]
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.attachInput += string(msg.Runes)
		} else if msg.Type == tea.KeySpace {
			m.attachInput += " "
		}
	}
	return m, nil
}

// updatePreview handles keys once a dry-run preview is showing. It is the
// only place Enter sends for real, and it is guarded so a send in flight
// swallows every key instead of launching a second one.
func (m Model) updatePreview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sending {
		return m, nil
	}
	switch msg.String() {
	case "q":
		if m.quitConfirm {
			return m, tea.Quit
		}
		m.quitConfirm = true
		return m, nil
	case "esc":
		m.quitConfirm = false
		m.previewing = false
		m.composing = true
		return m, nil
	case "enter", "ctrl+s":
		if err := validateAttachments(m.attachments); err != nil {
			m.replyErr = err
			return m, nil
		}
		m.quitConfirm = false
		m.sending = true
		attachments := append([]string(nil), m.attachments...)
		return m, sendReply(m.client, m.draftID, m.composer.Value(), attachments, m.replyToken)
	default:
		m.quitConfirm = false
	}
	return m, nil
}

// composeWheelScroll is how many bubbles/textarea CursorUp/CursorDown
// steps a single mouse wheel tick moves in the K4 reply composer — the
// same unit PgUp/PgDown use there (composerHeight steps instead of 3),
// since the composer has no line-index scroll offset of its own to share
// chatWheelScroll's contract with (see updateCompose/updateComposeMouse).
const composeWheelScroll = 3
