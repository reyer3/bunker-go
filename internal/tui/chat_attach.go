package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// Chat attachments (issue #5): the chat composer can carry files, added
// by dropping them on the terminal (which pastes their paths) or by
// pasting an image from the clipboard with Ctrl+V. Backspace on an empty
// draft removes the last one. They ride on the same Reply call as the
// text, so the send is previewed (dry-run) and confirmed like any other.

// clipboardReader reads an image from the system clipboard.
type clipboardReader interface {
	Image(ctx context.Context) (data []byte, ext string, err error)
}

// errNoClipboardImage is returned when the clipboard holds no image.
var errNoClipboardImage = errors.New("el portapapeles no tiene una imagen")

// execClipboard reads the clipboard through wl-paste (Wayland) or xclip
// (X11), as external processes: no CGO, and nothing to install beyond
// what the desktop usually has.
type execClipboard struct {
	getenv func(string) string
	run    func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func newExecClipboard() execClipboard {
	return execClipboard{getenv: os.Getenv, run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}}
}

// clipboardImageTypes are the image formats tried, most preferred first.
var clipboardImageTypes = []struct{ mime, ext string }{
	{"image/png", ".png"},
	{"image/jpeg", ".jpg"},
	{"image/webp", ".webp"},
	{"image/gif", ".gif"},
}

func (c execClipboard) Image(ctx context.Context) ([]byte, string, error) {
	var list, get func(mime string) []string
	var tool string
	switch {
	case c.getenv("WAYLAND_DISPLAY") != "":
		tool = "wl-paste"
		list = func(string) []string { return []string{"--list-types"} }
		get = func(mime string) []string { return []string{"--no-newline", "--type", mime} }
	case c.getenv("DISPLAY") != "":
		tool = "xclip"
		list = func(string) []string { return []string{"-selection", "clipboard", "-t", "TARGETS", "-o"} }
		get = func(mime string) []string { return []string{"-selection", "clipboard", "-t", mime, "-o"} }
	default:
		return nil, "", errors.New("no hay sesión gráfica (WAYLAND_DISPLAY/DISPLAY) para leer el portapapeles")
	}
	types, err := c.run(ctx, tool, list("")...)
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return nil, "", fmt.Errorf("falta %s para leer el portapapeles: %w", tool, err)
		}
		return nil, "", fmt.Errorf("%s: %w", tool, err)
	}
	offered := strings.Fields(string(types))
	for _, t := range clipboardImageTypes {
		for _, o := range offered {
			if o != t.mime {
				continue
			}
			data, err := c.run(ctx, tool, get(t.mime)...)
			if err != nil {
				return nil, "", fmt.Errorf("%s: %w", tool, err)
			}
			if len(bytes.TrimSpace(data)) == 0 {
				return nil, "", errNoClipboardImage
			}
			return data, t.ext, nil
		}
	}
	return nil, "", errNoClipboardImage
}

// clipboardImageMsg carries a pasted image, saved to a private temp
// file, back to Update.
type clipboardImageMsg struct {
	path string
	err  error
}

func pasteClipboardImageCmd(cb clipboardReader) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		data, ext, err := cb.Image(ctx)
		if err != nil {
			return clipboardImageMsg{err: err}
		}
		// CreateTemp makes the file 0600: a screenshot may be private.
		f, err := os.CreateTemp("", "bunker-captura-*"+ext)
		if err != nil {
			return clipboardImageMsg{err: err}
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			os.Remove(f.Name())
			return clipboardImageMsg{err: err}
		}
		if err := f.Close(); err != nil {
			os.Remove(f.Name())
			return clipboardImageMsg{err: err}
		}
		return clipboardImageMsg{path: f.Name()}
	}
}

// parseDroppedPaths reports whether pasted text is nothing but paths to
// existing regular files, as a terminal pastes them when files are
// dropped on it: one per line, or several on one line separated by
// spaces, each possibly a file:// URI, quoted, or backslash-escaped.
func parseDroppedPaths(text string) ([]string, bool) {
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		tokens, ok := splitShellWords(strings.TrimSpace(line))
		if !ok {
			return nil, false
		}
		for _, tok := range tokens {
			if strings.HasPrefix(tok, "file://") {
				u, err := url.Parse(tok)
				if err != nil || u.Path == "" {
					return nil, false
				}
				tok = u.Path
			}
			// Dropped files always paste absolute paths; requiring one
			// keeps ordinary words that happen to name a file in the
			// working directory from turning into attachments.
			if !filepath.IsAbs(tok) {
				return nil, false
			}
			info, err := os.Stat(tok)
			if err != nil || !info.Mode().IsRegular() {
				return nil, false
			}
			paths = append(paths, tok)
		}
	}
	return paths, len(paths) > 0
}

// splitShellWords splits a line on unquoted whitespace, honoring single
// quotes, double quotes and backslash escapes. It reports false on an
// unterminated quote.
func splitShellWords(line string) ([]string, bool) {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	escaped := false
	for _, r := range line {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote != 0:
			if r == quote {
				quote = 0
			} else if r == '\\' && quote == '"' {
				escaped = true
			} else {
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case unicode.IsSpace(r):
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return nil, false
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, true
}

// addChatAttachments appends paths, skipping ones already attached.
func (m Model) addChatAttachments(paths ...string) Model {
	have := map[string]bool{}
	for _, p := range m.chatAttachments {
		have[p] = true
	}
	next := append([]string(nil), m.chatAttachments...)
	for _, p := range paths {
		if !have[p] {
			next = append(next, p)
			have[p] = true
		}
	}
	m.chatAttachments = next
	m.chatAttachErr = nil
	return m
}

// removeLastChatAttachment drops the newest attachment, deleting it when
// it was a pasted temp file.
func (m Model) removeLastChatAttachment() Model {
	n := len(m.chatAttachments)
	if n == 0 {
		return m
	}
	last := m.chatAttachments[n-1]
	m.chatAttachments = append([]string(nil), m.chatAttachments[:n-1]...)
	m.chatTempFiles = removeTempFile(m.chatTempFiles, last)
	return m
}

// clearChatAttachments drops every attachment and deletes pasted temp
// files: after a successful send, or when leaving the chat.
func (m Model) clearChatAttachments() Model {
	for _, p := range m.chatTempFiles {
		os.Remove(p)
	}
	m.chatAttachments = nil
	m.chatTempFiles = nil
	m.chatAttachErr = nil
	return m
}

func removeTempFile(temps []string, path string) []string {
	out := temps[:0:0]
	for _, t := range temps {
		if t == path {
			os.Remove(t)
			continue
		}
		out = append(out, t)
	}
	return out
}

// chatAttachLine renders the chat's attachment chips (or the last
// attach error) for the tail below the composer.
func (m Model) chatAttachLine() (string, bool) {
	if m.chatAttachErr != nil {
		return "Adjuntar: " + safeLine(m.chatAttachErr.Error()), true
	}
	if len(m.chatAttachments) == 0 {
		return "", false
	}
	return "📎 " + attachmentChips(m.chatAttachments) + " · Retroceso quita", true
}
