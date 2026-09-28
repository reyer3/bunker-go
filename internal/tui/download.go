package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// downloadResultMsg carries the outcome of one Download RPC call, keyed
// by downloadToken the same way every other async result in this package
// is (chatThreadLoadedMsg, mailSentMsg, ...), so a stale result arriving
// after the flow was closed is silently dropped instead of resurrecting
// it.
type downloadResultMsg struct {
	token  uint64
	result core.DownloadResult
	err    error
}

// downloadAttachmentCmd performs the actual Download RPC. It never blocks
// the UI: like sendReply/sendMailSend, it is a tea.Cmd run off the
// update loop, with the same generous sendTimeout (an attachment can be
// up to the daemon's 100 MB cap).
func downloadAttachmentCmd(client Client, id string, index int, destPath string, force bool, token uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		result, err := client.Download(ctx, id, index, destPath, core.DownloadOptions{Force: force})
		return downloadResultMsg{token: token, result: result, err: err}
	}
}

// sanitizeAttachmentFilename returns a safe, single-segment base filename
// for downloading an attachment, defending against a hostile attachment
// name — message content an attacker fully controls — escaping the
// destination directory via a path separator, "..", or an embedded NUL.
// It never returns anything but a plain filename: no "/", no "\", no
// "..", no NUL.
func sanitizeAttachmentFilename(name string) string {
	name = strings.ReplaceAll(name, "\x00", "")
	name = strings.ReplaceAll(name, `\`, "/")
	// Rooting the name before Clean makes Clean itself collapse away any
	// number of leading ".." segments (Clean never lets a rooted path
	// climb above "/"), so the only thing left to extract is the final
	// path component — exactly the filename, wherever separators or ".."
	// tried to point it.
	cleaned := filepath.Clean("/" + name)
	base := filepath.Base(cleaned)
	switch base {
	case "", ".", "/":
		return "attachment"
	}
	return base
}

// downloadDirFor is the pure "~/Descargas, falling back to ~/Downloads"
// rule (conversation-view.md), separated from the filesystem check so it
// is trivially unit-testable.
func downloadDirFor(home string, descargasExists bool) string {
	if descargasExists {
		return filepath.Join(home, "Descargas")
	}
	return filepath.Join(home, "Downloads")
}

// defaultDownloadDir resolves the real default download directory: the
// production fallback behind Model.downloadDefaultDirFn, never called by
// a test (which always injects a fixed directory instead).
func defaultDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	descargas := filepath.Join(home, "Descargas")
	info, statErr := os.Stat(descargas)
	return downloadDirFor(home, statErr == nil && info.IsDir())
}

// resolveDownloadDefaultDir returns the directory a suggested download
// path is built under, using the model's injected override when a test
// set one so no test ever touches the real home directory.
func (m Model) resolveDownloadDefaultDir() string {
	if m.downloadDefaultDirFn != nil {
		return m.downloadDefaultDirFn()
	}
	return defaultDownloadDir()
}

// suggestedDownloadPath is the default, editable destination the path
// prompt opens with: the resolved download directory plus the
// attachment's sanitized filename.
func (m Model) suggestedDownloadPath(name string) string {
	return filepath.Join(m.resolveDownloadDefaultDir(), sanitizeAttachmentFilename(name))
}

// pathExists reports whether path already names something on disk (the
// no-overwrite-without-confirm guard checks this, never Force, before
// ever calling Download).
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// chatDownloadCandidate returns the newest loaded chat message carrying
// at least one attachment, searching backward from the end — "the
// message under focus" for K5, which has no separate message-selection
// cursor (see the chat view's Decisions: every key while a chat view is
// open is composer text, so `d` reaches for the most recent attachment
// rather than requiring a selection UI this task did not add).
func (m Model) chatDownloadCandidate() (core.Item, bool) {
	for i := len(m.chatItems) - 1; i >= 0; i-- {
		if len(m.chatItems[i].Attachments) > 0 {
			return m.chatItems[i], true
		}
	}
	return core.Item{}, false
}

// openDownload starts the download flow for item id's attachments: the
// path prompt directly for exactly one attachment, or the small picker
// for more than one. It never fires while there are none (callers check
// len(attachments) > 0 first).
func (m Model) openDownload(id string, attachments []core.Attachment) (tea.Model, tea.Cmd) {
	m.downloadActive = true
	m.downloadItemID = id
	m.downloadAttachments = attachments
	m.downloadErr = nil
	m.downloadResult = core.DownloadResult{}
	m.downloadSending = false
	m.downloadOverwrite = false
	if len(attachments) == 1 {
		m.downloadPicking = false
		m.downloadIndex = 0
		m.downloadPath = m.suggestedDownloadPath(attachments[0].Name)
	} else {
		m.downloadPicking = true
		m.downloadIndex = -1
		m.downloadPath = ""
	}
	return m, nil
}

// closeDownload resets every download field and returns to whichever
// view (chat or mail thread) is still underneath — chatMode/threadMode
// are never touched by the download flow, exactly like markConfirm's
// overlay leaves detail untouched.
func (m Model) closeDownload() (tea.Model, tea.Cmd) {
	m.downloadActive = false
	m.downloadItemID = ""
	m.downloadAttachments = nil
	m.downloadPicking = false
	m.downloadIndex = 0
	m.downloadPath = ""
	m.downloadOverwrite = false
	m.downloadSending = false
	m.downloadErr = nil
	m.downloadResult = core.DownloadResult{}
	return m, nil
}

// chooseDownloadAttachment picks attachment idx from the picker and
// advances to its (editable) suggested path.
func (m Model) chooseDownloadAttachment(idx int) (tea.Model, tea.Cmd) {
	m.downloadIndex = idx
	m.downloadPicking = false
	m.downloadPath = m.suggestedDownloadPath(m.downloadAttachments[idx].Name)
	m.downloadErr = nil
	return m, nil
}

// updateDownload handles keys while the download overlay is open: the
// picker (a digit selects an attachment), the editable path prompt
// (Enter confirms — asking "¿Sobrescribir?" first when the path already
// exists, never passing Force otherwise), the overwrite confirm itself,
// and the terminal result/error screen.
func (m Model) updateDownload(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.downloadSending {
		return m, nil
	}
	if m.downloadResult.Path != "" && m.downloadErr == nil {
		if msg.String() == "esc" {
			return m.closeDownload()
		}
		return m, nil
	}
	if m.downloadPicking {
		if msg.String() == "esc" {
			return m.closeDownload()
		}
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] >= '1' && msg.Runes[0] <= '9' {
			idx := int(msg.Runes[0] - '1')
			if idx < len(m.downloadAttachments) {
				return m.chooseDownloadAttachment(idx)
			}
		}
		return m, nil
	}
	if m.downloadOverwrite {
		switch msg.String() {
		case "esc":
			m.downloadOverwrite = false
			return m, nil
		case "enter":
			m.downloadOverwrite = false
			m.downloadSending = true
			m.downloadErr = nil
			m.downloadToken++
			return m, downloadAttachmentCmd(m.client, m.downloadItemID, m.downloadIndex, strings.TrimSpace(m.downloadPath), true, m.downloadToken)
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		return m.closeDownload()
	case "enter":
		path := strings.TrimSpace(m.downloadPath)
		if path == "" {
			return m, nil
		}
		if pathExists(path) {
			m.downloadOverwrite = true
			m.downloadErr = nil
			return m, nil
		}
		m.downloadSending = true
		m.downloadErr = nil
		m.downloadToken++
		return m, downloadAttachmentCmd(m.client, m.downloadItemID, m.downloadIndex, path, false, m.downloadToken)
	case "backspace":
		if m.downloadPath != "" {
			runes := []rune(m.downloadPath)
			m.downloadPath = string(runes[:len(runes)-1])
		}
		m.downloadErr = nil
	default:
		if msg.Type == tea.KeyRunes {
			m.downloadPath += string(msg.Runes)
		} else if msg.Type == tea.KeySpace {
			m.downloadPath += " "
		}
		m.downloadErr = nil
	}
	return m, nil
}
