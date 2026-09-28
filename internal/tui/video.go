package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/kittygfx"
)

// Video playback (issue #6): a video attachment previews as a frame
// grabbed with ffmpeg and plays with mpv, both external processes. The
// TUI suspends while the player runs (tea.ExecProcess) and resumes when
// it exits. Under a kitty-graphics terminal mpv draws inside the
// terminal (--vo=kitty); elsewhere it opens its own window.

// videoFrame returns one PNG frame of the video at path. It is a
// variable so tests can stand in for ffmpeg.
var videoFrame = ffmpegFrame

func ffmpegFrame(ctx context.Context, path string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-ss", "1", "-i", path, "-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "-").Output()
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return nil, fmt.Errorf("falta ffmpeg para la miniatura del video: %w", err)
		}
		// A clip shorter than one second has no frame at 1s: retry at 0.
		out, err = exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
			"-i", path, "-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "-").Output()
		if err != nil {
			return nil, fmt.Errorf("ffmpeg: %w", err)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("ffmpeg: el video no tiene cuadros")
	}
	return out, nil
}

// videoPlayerCommand builds the player command for path.
// BUNKER_VIDEO_PLAYER overrides it (the path is appended as the last
// argument).
func videoPlayerCommand(gfx kittygfx.Mode, getenv func(string) string, path string) *exec.Cmd {
	if custom := strings.Fields(getenv("BUNKER_VIDEO_PLAYER")); len(custom) > 0 {
		return exec.Command(custom[0], append(custom[1:], path)...)
	}
	args := []string{"--really-quiet"}
	if gfx == kittygfx.Kitty {
		args = append(args, "--vo=kitty", "--vo-kitty-use-shm=no")
	}
	return exec.Command("mpv", append(args, path)...)
}

// videoReadyMsg carries a downloaded video, ready to play.
type videoReadyMsg struct {
	path string
	err  error
}

// videoDoneMsg reports the player's exit.
type videoDoneMsg struct{ err error }

// playVideo downloads key's video (once: it shares the media cache) and
// then plays it.
func (m Model) playVideo(key string) tea.Cmd {
	itemID, index, a, ok := m.chatAttachment(key)
	if !ok || !isVideoAttachment(a) || m.client == nil {
		return nil
	}
	client, dir := m.client, m.mediaDir
	if dir == "" {
		dir = mediaCacheDir()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), mediaFetchTimeout)
		defer cancel()
		path, err := cachedMedia(ctx, client, dir, itemID, index, a.Name, videoMaxBytes)
		return videoReadyMsg{path: path, err: err}
	}
}

// handleVideoReady starts the player on a downloaded video.
func (m Model) handleVideoReady(msg videoReadyMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.mediaErr = msg.err
		return m, nil
	}
	m.mediaErr = nil
	getenv := m.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	cmd := videoPlayerCommand(m.gfx, getenv, msg.path)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return videoDoneMsg{err: err} })
}

func (m Model) handleVideoDone(msg videoDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		var execErr *exec.Error
		if errors.As(msg.err, &execErr) {
			m.mediaErr = fmt.Errorf("falta mpv para reproducir el video (o define BUNKER_VIDEO_PLAYER): %w", msg.err)
		} else {
			m.mediaErr = fmt.Errorf("reproductor: %w", msg.err)
		}
		return m, nil
	}
	m.mediaErr = nil
	return m, nil
}

// newestVideoKey returns the open chat's newest video, if any.
func (m Model) newestVideoKey() (string, bool) {
	keys := m.chatImageKeys()
	for i := len(keys) - 1; i >= 0; i-- {
		if _, _, a, ok := m.chatAttachment(keys[i]); ok && isVideoAttachment(a) {
			return keys[i], true
		}
	}
	return "", false
}
