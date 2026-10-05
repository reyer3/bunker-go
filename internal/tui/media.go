package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/kittygfx"
	"github.com/reyer3/bunker-go/internal/secfile"
)

// Inline images (issue #4): when the terminal speaks the kitty graphics
// protocol (see kittygfx.Detect), an image attachment in the chat view
// renders as a thumbnail inside its bubble, and "v" or a click opens it
// full size. Everywhere else the chat renders exactly as before: nothing
// here runs unless m.gfx is kittygfx.Kitty.

const (
	// thumbMaxRows caps a thumbnail's height so one image never takes
	// over the conversation.
	thumbMaxRows = 10
	// mediaMaxBytes bounds what the TUI downloads just to preview an
	// image; videoMaxBytes bounds a video downloaded for its thumbnail or
	// to play.
	mediaMaxBytes = 25 << 20
	videoMaxBytes = 64 << 20
	// mediaFetchTimeout bounds one download+decode.
	mediaFetchTimeout = 60 * time.Second
)

type thumbState int

const (
	thumbLoading thumbState = iota
	thumbReady
	thumbFailed
)

// mediaThumb is one uploaded image: its kitty image id and the cell box
// its placement occupies.
type mediaThumb struct {
	state thumbState
	id    uint32
	cols  int
	rows  int
}

// mediaCache holds every thumbnail and full-size image this session has
// uploaded to the terminal. It is shared by pointer across Model copies:
// it is a cache of what the terminal already holds, not view state.
type mediaCache struct {
	thumbs map[string]*mediaThumb
	byID   map[uint32]string
	nextID uint32
}

func newMediaCache() *mediaCache {
	return &mediaCache{thumbs: map[string]*mediaThumb{}, byID: map[uint32]string{}, nextID: 1}
}

func (c *mediaCache) allocID() uint32 {
	id := c.nextID
	c.nextID++
	if c.nextID > kittygfx.MaxID {
		c.nextID = 1
	}
	return id
}

// imageViewer is the full-size overlay: the image keys of the open
// conversation, in order, and which one is shown.
type imageViewer struct {
	keys  []string
	index int
}

// mediaKey names one attachment of one item.
func mediaKey(itemID string, index int) string {
	return fmt.Sprintf("%s#%d", itemID, index)
}

func fullKey(key string) string { return key + "#full" }

func isImageAttachment(a core.Attachment) bool {
	return strings.HasPrefix(strings.ToLower(a.MIME), "image/")
}

// isVideoAttachment reports a video (issue #6): it previews as a frame
// grabbed with ffmpeg and plays with mpv.
func isVideoAttachment(a core.Attachment) bool {
	return strings.HasPrefix(strings.ToLower(a.MIME), "video/")
}

// isPreviewable reports an attachment the chat can draw inline: an image
// or video, or any other file (a WhatsApp document) whose message carried
// its own thumbnail.
func isPreviewable(a core.Attachment) bool {
	return isImageAttachment(a) || isVideoAttachment(a) || len(a.Thumbnail) > 0
}

// embeddedThumbnail returns the preview the message itself carried
// (issue #18), when key's rendering should use it instead of downloading
// the media: always for the inline thumbnail, and for the full-size view
// only when there is no real image to show (a document), since the
// embedded preview is too small to stand in for a photo opened on
// purpose. An oversized one is ignored like a missing one: the store
// never holds such a value from the adapters, so it is corrupt.
func embeddedThumbnail(a core.Attachment, full bool) ([]byte, bool) {
	if len(a.Thumbnail) == 0 || len(a.Thumbnail) > core.MaxThumbnailBytes {
		return nil, false
	}
	if full && (isImageAttachment(a) || isVideoAttachment(a)) {
		return nil, false
	}
	return a.Thumbnail, true
}

// chatImageKeys lists the open conversation's image and video
// attachments, oldest first, matching the order they render in.
func (m Model) chatImageKeys() []string {
	var keys []string
	for _, item := range m.chatItems {
		if item.Deleted {
			continue
		}
		for i, a := range item.Attachments {
			if isPreviewable(a) {
				keys = append(keys, mediaKey(item.ID, i))
			}
		}
	}
	return keys
}

// chatAttachment returns the attachment key names in the open chat.
func (m Model) chatAttachment(key string) (itemID string, index int, a core.Attachment, ok bool) {
	itemID, index, ok = splitMediaKey(strings.TrimSuffix(key, "#full"))
	if !ok {
		return "", 0, core.Attachment{}, false
	}
	for _, item := range m.chatItems {
		if item.ID == itemID && index < len(item.Attachments) {
			return itemID, index, item.Attachments[index], true
		}
	}
	return "", 0, core.Attachment{}, false
}

// mediaReadyMsg carries one fetched, re-encoded image back to Update.
type mediaReadyMsg struct {
	key  string
	png  []byte
	cols int
	rows int
	err  error
}

// fetchMediaCmd fits item's attachment at index into maxCols×maxRows
// cells. It prefers the thumbnail embedded in the message (no network, no
// ffmpeg); only when there is none, or it does not decode, does it
// download the media into the media cache (once: a cached file is
// reused) through the daemon's own Download.
func fetchMediaCmd(client Client, dir, key, itemID string, index int, a core.Attachment, maxCols, maxRows int) tea.Cmd {
	video := isVideoAttachment(a)
	embedded, hasEmbedded := embeddedThumbnail(a, strings.HasSuffix(key, "#full"))
	return func() tea.Msg {
		if hasEmbedded {
			if png, cols, rows, err := kittygfx.Fit(embedded, maxCols, maxRows); err == nil {
				return mediaReadyMsg{key: key, png: png, cols: cols, rows: rows}
			}
		}
		if !isImageAttachment(a) && !video {
			// A document is only previewable through its embedded
			// thumbnail: downloading it would not produce an image.
			return mediaReadyMsg{key: key, err: fmt.Errorf("tui: %s has no usable thumbnail", key)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), mediaFetchTimeout)
		defer cancel()
		limit := int64(mediaMaxBytes)
		if video {
			limit = videoMaxBytes
		}
		path, err := cachedMedia(ctx, client, dir, itemID, index, a.Name, limit)
		if err != nil {
			return mediaReadyMsg{key: key, err: err}
		}
		var data []byte
		if video {
			data, err = videoFrame(ctx, path)
		} else {
			data, err = os.ReadFile(path)
		}
		if err != nil {
			return mediaReadyMsg{key: key, err: err}
		}
		png, cols, rows, err := kittygfx.Fit(data, maxCols, maxRows)
		return mediaReadyMsg{key: key, png: png, cols: cols, rows: rows, err: err}
	}
}

// cachedMedia returns the local path of an attachment's bytes,
// downloading it first when it is not cached yet. The file name is a
// hash of the item and index (never a remote-controlled name), keeping
// only a sanitized extension.
func cachedMedia(ctx context.Context, client Client, dir, itemID string, index int, name string, maxBytes int64) (string, error) {
	if err := secfile.EnsureDir(dir); err != nil {
		return "", fmt.Errorf("media cache: %w", err)
	}
	sum := sha256.Sum256([]byte(mediaKey(itemID, index)))
	ext := strings.ToLower(filepath.Ext(sanitizeAttachmentFilename(name)))
	if len(ext) > 8 {
		ext = ""
	}
	path := filepath.Join(dir, hex.EncodeToString(sum[:12])+ext)
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return path, nil
	}
	if _, err := client.Download(ctx, itemID, index, path, core.DownloadOptions{Force: true, MaxBytes: maxBytes}); err != nil {
		return "", err
	}
	return path, nil
}

// transmitCmd uploads an image to the terminal. It writes through the
// same locked writer the renderer uses, so an upload never interleaves
// with a frame.
func transmitCmd(w io.Writer, escape string) tea.Cmd {
	if w == nil {
		return nil
	}
	return func() tea.Msg {
		_, _ = io.WriteString(w, escape)
		return nil
	}
}

// requestChatMedia starts fetching every image thumbnail of the open
// conversation not yet cached or in flight.
func (m Model) requestChatMedia() tea.Cmd {
	if m.gfx != kittygfx.Kitty || m.media == nil || !m.chatMode || m.client == nil {
		return nil
	}
	maxCols := min(chatBubbleWidth(m.width), 32)
	if maxCols < 4 {
		return nil
	}
	var cmds []tea.Cmd
	for _, item := range m.chatItems {
		if item.Deleted {
			continue
		}
		for i, a := range item.Attachments {
			key := mediaKey(item.ID, i)
			if !isPreviewable(a) || m.media.thumbs[key] != nil {
				continue
			}
			m.media.thumbs[key] = &mediaThumb{state: thumbLoading}
			cmds = append(cmds, fetchMediaCmd(m.client, m.mediaDir, key, item.ID, i, a, maxCols, thumbMaxRows))
		}
	}
	return tea.Batch(cmds...)
}

// handleMediaReady stores a fetched image and uploads it.
func (m Model) handleMediaReady(msg mediaReadyMsg) (tea.Model, tea.Cmd) {
	if m.media == nil {
		return m, nil
	}
	t := m.media.thumbs[msg.key]
	if t == nil {
		t = &mediaThumb{}
		m.media.thumbs[msg.key] = t
	}
	if msg.err != nil {
		t.state = thumbFailed
		return m, nil
	}
	if t.id != 0 {
		delete(m.media.byID, t.id)
	}
	t.id = m.media.allocID()
	t.cols, t.rows = msg.cols, msg.rows
	t.state = thumbReady
	m.media.byID[t.id] = msg.key
	return m, transmitCmd(m.gfxOut, kittygfx.Transmit(t.id, msg.png, t.cols, t.rows))
}

// readyThumb returns key's uploaded image, if any.
func (m Model) readyThumb(key string) (*mediaThumb, bool) {
	if m.gfx != kittygfx.Kitty || m.media == nil {
		return nil, false
	}
	t := m.media.thumbs[key]
	if t == nil || t.state != thumbReady {
		return nil, false
	}
	return t, true
}

// thumbBubbleLines renders a ready thumbnail as bubble lines: the
// placeholder cells, then bubble-colored padding to the bubble width.
func thumbBubbleLines(t *mediaThumb, bodyStyle lipgloss.Style, bubbleWidth, width int, right bool, caption string) []string {
	cols := t.cols
	if cols > bubbleWidth {
		return nil
	}
	pad := bodyStyle.Render(strings.Repeat(" ", bubbleWidth-cols))
	lines := kittygfx.Lines(t.id, cols, t.rows)
	for i, line := range lines {
		lines[i] = alignBubbleLine(line+pad, bubbleWidth, width, right)
	}
	if caption != "" {
		text := padTo(runewidth.Truncate(caption, bubbleWidth, "…"), bubbleWidth)
		lines = append(lines, alignBubbleLine(bodyStyle.Render(text), bubbleWidth, width, right))
	}
	return lines
}

// videoCaption is the line under a video thumbnail.
func videoCaption(a core.Attachment) string {
	return "▶ " + safeLine(a.Name) + " · clic para reproducir"
}

// openViewer opens the full-size overlay on key (or the newest image
// when key is empty) and starts fetching its full-size version.
func (m Model) openViewer(key string) (tea.Model, tea.Cmd) {
	keys := m.chatImageKeys()
	if len(keys) == 0 || m.gfx != kittygfx.Kitty || m.media == nil {
		return m, nil
	}
	index := len(keys) - 1
	for i, k := range keys {
		if k == key {
			index = i
		}
	}
	m.viewer = &imageViewer{keys: keys, index: index}
	return m, m.requestFull(keys[index])
}

// requestFull fetches key's full-size version for the overlay.
func (m Model) requestFull(key string) tea.Cmd {
	fk := fullKey(key)
	if m.media.thumbs[fk] != nil {
		return nil
	}
	itemID, index, ok := splitMediaKey(key)
	if !ok {
		return nil
	}
	_, _, a, found := m.chatAttachment(key)
	if !found {
		a = core.Attachment{Name: "", MIME: "image/"}
	}
	m.media.thumbs[fk] = &mediaThumb{state: thumbLoading}
	return fetchMediaCmd(m.client, m.mediaDir, fk, itemID, index, a, max(m.width-2, 4), max(m.height-3, 2))
}

func splitMediaKey(key string) (itemID string, index int, ok bool) {
	at := strings.LastIndexByte(key, '#')
	if at < 0 {
		return "", 0, false
	}
	if _, err := fmt.Sscanf(key[at+1:], "%d", &index); err != nil {
		return "", 0, false
	}
	return key[:at], index, true
}

// closeViewer closes the overlay and frees the full-size images it
// uploaded (thumbnails stay: the chat keeps showing them).
func (m Model) closeViewer() (tea.Model, tea.Cmd) {
	var escapes strings.Builder
	if m.viewer != nil && m.media != nil {
		for _, key := range m.viewer.keys {
			fk := fullKey(key)
			if t := m.media.thumbs[fk]; t != nil {
				if t.id != 0 {
					escapes.WriteString(kittygfx.Delete(t.id))
					delete(m.media.byID, t.id)
				}
				delete(m.media.thumbs, fk)
			}
		}
	}
	m.viewer = nil
	if escapes.Len() == 0 {
		return m, nil
	}
	return m, transmitCmd(m.gfxOut, escapes.String())
}

func (m Model) updateViewer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "v":
		return m.closeViewer()
	case "enter", " ":
		if _, _, a, ok := m.chatAttachment(m.viewer.keys[m.viewer.index]); ok && isVideoAttachment(a) {
			return m, m.playVideo(m.viewer.keys[m.viewer.index])
		}
	case "left", "h", "up", "k":
		if m.viewer.index > 0 {
			m.viewer = &imageViewer{keys: m.viewer.keys, index: m.viewer.index - 1}
			return m, m.requestFull(m.viewer.keys[m.viewer.index])
		}
	case "right", "l", "down", "j":
		if m.viewer.index < len(m.viewer.keys)-1 {
			m.viewer = &imageViewer{keys: m.viewer.keys, index: m.viewer.index + 1}
			return m, m.requestFull(m.viewer.keys[m.viewer.index])
		}
	}
	return m, nil
}

// viewerView renders the overlay: a one-line header, then the image
// centered in the remaining space.
func (m Model) viewerView() string {
	v := m.viewer
	header := fmt.Sprintf("Imagen %d/%d · ←/→ anterior/siguiente · Esc/q/v cerrar", v.index+1, len(v.keys))
	if _, _, a, ok := m.chatAttachment(v.keys[v.index]); ok && isVideoAttachment(a) {
		header = fmt.Sprintf("Video %d/%d · ↵ reproducir · ←/→ anterior/siguiente · Esc/q/v cerrar", v.index+1, len(v.keys))
	}
	lines := []string{safeLine(header)}
	if m.mediaErr != nil {
		lines = append(lines, "Error: "+humanError(m.mediaErr))
	}
	t := m.media.thumbs[fullKey(v.keys[v.index])]
	switch {
	case t == nil || t.state == thumbLoading:
		lines = append(lines, "", "Cargando imagen…")
	case t.state == thumbFailed:
		lines = append(lines, "", "No se pudo cargar la imagen.")
	default:
		left := strings.Repeat(" ", max((m.width-t.cols)/2, 0))
		for _, line := range kittygfx.Lines(t.id, t.cols, t.rows) {
			lines = append(lines, left+line)
		}
	}
	return strings.Join(lines, "\n")
}

// imageKeyAt maps a rendered line (e.g. under a mouse click) to the
// thumbnail drawn on it.
func (m Model) imageKeyAt(line string) (string, bool) {
	if m.media == nil {
		return "", false
	}
	id, ok := kittygfx.ImageID(line)
	if !ok {
		return "", false
	}
	key, ok := m.media.byID[id]
	return key, ok && !strings.HasSuffix(key, "#full")
}

// lockedOutput serializes every write to the terminal: bubbletea's
// renderer writes each frame in one Write, and image uploads and
// notifications go through the same lock, so none can land in the middle
// of another. When the output is a terminal it keeps exposing Fd/Read/
// Close, so bubbletea still detects it as a TTY.
type lockedOutput struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedOutput) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

type terminalFile interface {
	io.ReadWriteCloser
	Fd() uintptr
}

type lockedFile struct {
	lockedOutput
	f terminalFile
}

func (l lockedFile) Read(p []byte) (int, error) { return l.f.Read(p) }
func (l lockedFile) Close() error               { return l.f.Close() }
func (l lockedFile) Fd() uintptr                { return l.f.Fd() }

// lockOutput wraps output in a lock shared by every writer.
func lockOutput(output io.Writer) io.Writer {
	lo := lockedOutput{mu: &sync.Mutex{}, w: output}
	if f, ok := output.(terminalFile); ok {
		return lockedFile{lockedOutput: lo, f: f}
	}
	return lo
}

// mediaCacheDir is where previewed attachments are cached.
func mediaCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "bunker-go", "media")
}
