package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/secfile"
)

// Forwarding a chat message (Alt+F): the selected message (or the newest,
// as with Alt+Y) is forwarded to one chat or contact picked from the
// contact picker, scoped to the same channel. The target chat opens with
// the composer prefilled with the message's text and its attachments
// downloaded to local files; the user may edit the text, then the usual
// dry-run preview and confirming Enter send it with core.Outgoing.Forward,
// which WhatsApp shows as "Reenviado". One target per forward: never a
// bulk send.

// chatForwardKey forwards the selected message.
const chatForwardKey = "alt+f"

// chatForwardState is the forward being composed in the open chat.
type chatForwardState struct {
	// prevDraft is the target chat's own draft, given back when the
	// forward is cancelled.
	prevDraft string
	// loading is true while the attachments are being downloaded; the
	// forward cannot be sent until they are on disk.
	loading bool
}

// forwardMediaMsg carries a forward's downloaded attachments: one local
// path per attachment of the original, in order.
type forwardMediaMsg struct {
	token    uint64
	paths    []string
	voice    bool
	voiceDur time.Duration
	err      error
}

// startChatForward is Alt+F in a chat: it opens the target picker for the
// selected message.
func (m Model) startChatForward() (tea.Model, tea.Cmd) {
	it, ok := m.chatCopyTarget()
	switch {
	case !ok:
		return m.noteUIError(errors.New("no hay mensajes que reenviar")), nil
	case it.Deleted:
		return m.noteUIError(errors.New("no se puede reenviar un mensaje eliminado")), nil
	case strings.TrimSpace(it.Body) == "" && len(it.Attachments) == 0:
		return m.noteUIError(errors.New("el mensaje no tiene nada que reenviar")), nil
	case m.client == nil:
		return m.noteUIError(errors.New("sin conexión con el daemon")), nil
	}
	next, cmd := m.openPickerFor(m.chatChannel, "Reenviar a…")
	next.forwardPick = &it
	return next, cmd
}

// pickForwardTarget leaves the source chat (keeping its draft), opens the
// picked contact's chat and starts the forward there.
func (m Model) pickForwardTarget(c core.Contact) (tea.Model, tea.Cmd) {
	src := *m.forwardPick
	m.forwardPick = nil
	m.picker = nil
	m, leave := m.leaveChat()
	next, open := m.pickContact(c)
	m = next.(Model)
	m, fetch := m.beginChatForward(src)
	return m, tea.Batch(leave, open, fetch)
}

// beginChatForward fills the open chat's composer with src and starts
// downloading its attachments.
func (m Model) beginChatForward(src core.Item) (Model, tea.Cmd) {
	m.chatForwardToken++
	m = m.clearChatAttachments()
	m.chatForward = &chatForwardState{prevDraft: m.composer.Value(), loading: len(src.Attachments) > 0}
	m.composer.SetValue(src.Body)
	m = m.resizeChatComposer()
	if len(src.Attachments) == 0 {
		return m, nil
	}
	m.mediaErr = nil
	m = m.withFlash("Descargando el adjunto para reenviar…")
	dir := m.mediaDir
	if dir == "" {
		dir = mediaCacheDir()
	}
	return m, forwardMediaCmd(m.client, dir, src, m.chatForwardToken)
}

// forwardMediaCmd downloads every attachment of src to a local file the
// send can carry. A single voice note stays a voice note.
func forwardMediaCmd(client Client, dir string, src core.Item, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		paths := make([]string, 0, len(src.Attachments))
		for i, a := range src.Attachments {
			path, err := forwardMediaPath(ctx, client, dir, src.ID, i, a.Name)
			if err != nil {
				return forwardMediaMsg{token: token, err: err}
			}
			paths = append(paths, path)
		}
		msg := forwardMediaMsg{token: token, paths: paths}
		if len(src.Attachments) == 1 && src.Attachments[0].Voice {
			msg.voice = true
			msg.voiceDur = time.Duration(src.Attachments[0].Duration) * time.Second
		}
		return msg
	}
}

// forwardMediaPath downloads one attachment under the media cache with
// its own (sanitized) file name, unlike cachedMedia's hashed names: the
// recipient of a forwarded document sees that name.
func forwardMediaPath(ctx context.Context, client Client, dir, itemID string, index int, name string) (string, error) {
	sum := sha256.Sum256([]byte(mediaKey(itemID, index)))
	sub := filepath.Join(dir, "forward", hex.EncodeToString(sum[:12]))
	if err := secfile.EnsureDir(sub); err != nil {
		return "", fmt.Errorf("media cache: %w", err)
	}
	path := filepath.Join(sub, sanitizeAttachmentFilename(name))
	if _, err := client.Download(ctx, itemID, index, path, core.DownloadOptions{Force: true, MaxBytes: openMaxBytes}); err != nil {
		return "", err
	}
	return path, nil
}

// handleForwardMedia attaches a forward's downloaded files. A failed
// download cancels the whole forward and says so: sending the text alone
// would silently drop the attachment.
func (m Model) handleForwardMedia(msg forwardMediaMsg) (tea.Model, tea.Cmd) {
	if m.chatForward == nil || msg.token != m.chatForwardToken {
		return m, nil
	}
	if msg.err != nil {
		m = m.cancelChatForward()
		return m.noteUIError(fmt.Errorf("no se pudo descargar el adjunto para reenviar: %w", msg.err)), nil
	}
	f := *m.chatForward
	f.loading = false
	m.chatForward = &f
	m.chatAttachments = msg.paths
	m.chatVoice, m.chatVoiceDur = msg.voice, msg.voiceDur
	return m, nil
}

// cancelChatForward drops the forward being composed: its text and files
// go, and the chat's own draft comes back.
func (m Model) cancelChatForward() Model {
	if m.chatForward == nil {
		return m
	}
	prev := m.chatForward.prevDraft
	m.chatForward = nil
	m.chatForwardToken++
	m = m.clearChatAttachments()
	m.composer.SetValue(prev)
	return m.resizeChatComposer()
}

// chatForwardLine is the indicator under the composer while a forward is
// being composed.
func (m Model) chatForwardLine() (string, bool) {
	if m.chatForward == nil {
		return "", false
	}
	if m.chatForward.loading {
		return "↪ Reenviando · descargando el adjunto…", true
	}
	return "↪ Reenviando · Esc cancela", true
}
