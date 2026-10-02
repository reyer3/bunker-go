package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// Voice notes (notas de voz). Playback and recording both run external
// processes (mpv, ffmpeg by default), never audio bindings: bunker stays
// pure Go. Each runs in its own process group so that stopping it reaches
// a whole `sh -c` pipeline, and so that a Ctrl+C aimed at the TUI cannot
// leave a recorder open behind a dead interface (voiceProcs below).

const (
	// voicePlayKey plays the newest voice note of the open chat; Alt+C/E/
	// X/A/H and Alt++ are taken (calls.go, chat_actions.go, herdr.go).
	voicePlayKey = "alt+p"
	// voiceRecordKey starts recording a voice note.
	voiceRecordKey = "alt+v"

	// voiceMaxBytes bounds a downloaded voice note; WhatsApp caps audio
	// far below this.
	voiceMaxBytes = 32 << 20
	// voiceStderrTail bounds the helper stderr kept for error reports.
	voiceStderrTail = 2048
	// voiceKillGrace is how long a stopped process may take to exit
	// before it is killed.
	voiceKillGrace = 5 * time.Second
)

// voiceProcs tracks every running player and recorder so Run can kill
// them when the interface exits by any route.
var voiceProcs = struct {
	mu sync.Mutex
	m  map[*exec.Cmd]struct{}
}{m: map[*exec.Cmd]struct{}{}}

func trackVoiceProc(cmd *exec.Cmd) {
	voiceProcs.mu.Lock()
	voiceProcs.m[cmd] = struct{}{}
	voiceProcs.mu.Unlock()
}

func untrackVoiceProc(cmd *exec.Cmd) {
	voiceProcs.mu.Lock()
	delete(voiceProcs.m, cmd)
	voiceProcs.mu.Unlock()
}

// killVoiceProcs kills whatever voice process is still running.
func killVoiceProcs() {
	voiceProcs.mu.Lock()
	cmds := make([]*exec.Cmd, 0, len(voiceProcs.m))
	for c := range voiceProcs.m {
		cmds = append(cmds, c)
	}
	voiceProcs.mu.Unlock()
	for _, c := range cmds {
		signalGroup(c, syscall.SIGKILL)
	}
}

// signalGroup sends sig to cmd's whole process group.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}

// tailBuffer keeps the last max bytes written to it: a helper's stderr,
// for the report when it dies.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// stderrNote formats a helper's stderr tail for an error message.
func stderrNote(t *tailBuffer) string {
	s := t.String()
	if s == "" {
		return ""
	}
	return ": " + s
}

// formatVoiceDuration renders seconds as m:ss.
func formatVoiceDuration(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

var waveformBars = []rune("▁▂▃▄▅▆▇█")

// waveformBarString draws a 0-100 waveform as at most width block
// characters, averaging the samples that share a bar.
func waveformBarString(wave []byte, width int) string {
	if len(wave) == 0 || width < 1 {
		return ""
	}
	n := min(len(wave), width)
	var b strings.Builder
	for i := 0; i < n; i++ {
		lo, hi := i*len(wave)/n, (i+1)*len(wave)/n
		if hi <= lo {
			hi = lo + 1
		}
		sum := 0
		for _, v := range wave[lo:hi] {
			sum += int(min(v, 100))
		}
		level := sum / (hi - lo) * (len(waveformBars) - 1) / 100
		b.WriteRune(waveformBars[level])
	}
	return b.String()
}

// voiceBubbleTexts is a voice note's bubble content: its label with the
// length, then (when the channel sent one) a crude waveform.
func voiceBubbleTexts(a core.Attachment, width int) []string {
	label := "🎤 Nota de voz"
	if a.Duration > 0 {
		label += " · " + formatVoiceDuration(a.Duration)
	}
	out := []string{label}
	if bars := waveformBarString(a.Waveform, min(width, 32)); bars != "" {
		out = append(out, bars)
	}
	return out
}

// voiceLineTags marks, parallel to bubble, the lines that belong to one of
// item's voice notes with its media key and leaves the rest "". The lines
// are found by their content because chatBubbleLines returns plain
// strings; an item without an ID (the optimistic bubble) is never tagged.
func voiceLineTags(item core.Item, bubble []string) []string {
	tags := make([]string, len(bubble))
	if item.ID == "" || item.Deleted {
		return tags
	}
	next := 0
	for i, a := range item.Attachments {
		if !a.Voice {
			continue
		}
		texts := voiceBubbleTexts(a, 1<<10)
		for ; next < len(bubble); next++ {
			if !strings.Contains(bubble[next], texts[0]) {
				continue
			}
			tags[next] = mediaKey(item.ID, i)
			if len(texts) > 1 && next+1 < len(bubble) {
				tags[next+1] = mediaKey(item.ID, i)
			}
			next++
			break
		}
	}
	return tags
}

// voiceKeyAtLine returns the voice note shown on screen row y of the chat
// view, for the click that plays it.
func (m Model) voiceKeyAtLine(y int) (string, bool) {
	header := len(m.chatHeaderLines())
	_, tags := m.chatBodyTagged()
	budget := m.chatScrollBudget()
	start, end, _ := windowBounds(len(tags), m.chatScroll, budget)
	row := y - header
	if row < 0 || start+row >= end {
		return "", false
	}
	key := tags[start+row]
	return key, key != ""
}

// newestVoiceKey is the open chat's newest voice note.
func (m Model) newestVoiceKey() (string, bool) {
	for i := len(m.chatItems) - 1; i >= 0; i-- {
		it := m.chatItems[i]
		if it.Deleted {
			continue
		}
		for j := len(it.Attachments) - 1; j >= 0; j-- {
			if it.Attachments[j].Voice {
				return mediaKey(it.ID, j), true
			}
		}
	}
	return "", false
}

// audioPlayerArgv is the player command: BUNKER_AUDIO_PLAYER (the path is
// appended as the last argument), else mpv without video.
func audioPlayerArgv(getenv func(string) string) []string {
	if custom := strings.Fields(getenv("BUNKER_AUDIO_PLAYER")); len(custom) > 0 {
		return custom
	}
	return []string{"mpv", "--no-video", "--really-quiet"}
}

func (m Model) envFunc() func(string) string {
	if m.getenv != nil {
		return m.getenv
	}
	return os.Getenv
}

// errNoAudioPlayer names what to install when the player is missing.
func errNoAudioPlayer(name string) error {
	return fmt.Errorf("falta %s para reproducir notas de voz (instálalo o define BUNKER_AUDIO_PLAYER)", name)
}

// voicePlayback is a voice note being fetched or played. Update owns the
// fields it sets; done/exitErr belong to the goroutine reaping the
// player.
type voicePlayback struct {
	key     string
	cmd     *exec.Cmd
	stderr  *tailBuffer
	done    chan struct{}
	exitErr error
	stopped atomic.Bool
}

type voicePlayReadyMsg struct {
	pb   *voicePlayback
	path string
	err  error
}

type voicePlayDoneMsg struct{ pb *voicePlayback }

// startVoicePlay plays the voice note at key: it checks the player is
// installed, downloads the note through the media cache (once), and then
// plays it in the background, so the chat stays usable. A second call
// while one plays stops it.
func (m Model) startVoicePlay(key string) (Model, tea.Cmd) {
	if m.voicePlay != nil {
		return m.stopVoicePlay(), nil
	}
	itemID, index, a, ok := m.chatAttachment(key)
	if !ok || !a.Voice || m.client == nil {
		return m, nil
	}
	argv := audioPlayerArgv(m.envFunc())
	if _, err := exec.LookPath(argv[0]); err != nil {
		m.mediaErr = errNoAudioPlayer(argv[0])
		return m, nil
	}
	m.mediaErr = nil
	pb := &voicePlayback{key: key}
	m.voicePlay = pb
	client, dir := m.client, m.mediaDir
	if dir == "" {
		dir = mediaCacheDir()
	}
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), mediaFetchTimeout)
		defer cancel()
		// The extension tells players that sniff by name what it is.
		path, err := cachedMedia(ctx, client, dir, itemID, index, "voz.ogg", voiceMaxBytes)
		return voicePlayReadyMsg{pb: pb, path: path, err: err}
	}
}

// handleVoicePlayReady starts the player on the downloaded note.
func (m Model) handleVoicePlayReady(msg voicePlayReadyMsg) (tea.Model, tea.Cmd) {
	if m.voicePlay != msg.pb {
		return m, nil // stopped while downloading
	}
	if msg.err != nil {
		m.voicePlay = nil
		m.mediaErr = fmt.Errorf("no se pudo descargar la nota de voz: %w", msg.err)
		return m, nil
	}
	pb := msg.pb
	argv := audioPlayerArgv(m.envFunc())
	cmd := exec.Command(argv[0], append(argv[1:], msg.path)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pb.stderr = &tailBuffer{max: voiceStderrTail}
	cmd.Stderr = pb.stderr
	if err := cmd.Start(); err != nil {
		m.voicePlay = nil
		if errors.Is(err, exec.ErrNotFound) {
			m.mediaErr = errNoAudioPlayer(argv[0])
		} else {
			m.mediaErr = fmt.Errorf("reproductor %s: %w", argv[0], err)
		}
		return m, nil
	}
	pb.cmd = cmd
	pb.done = make(chan struct{})
	trackVoiceProc(cmd)
	go func() {
		pb.exitErr = cmd.Wait()
		untrackVoiceProc(cmd)
		close(pb.done)
	}()
	m.mediaErr = nil
	return m, func() tea.Msg {
		<-pb.done
		return voicePlayDoneMsg{pb: pb}
	}
}

// handleVoicePlayDone clears the playing state when the player exits by
// itself, reporting a failure with the player's stderr.
func (m Model) handleVoicePlayDone(msg voicePlayDoneMsg) (tea.Model, tea.Cmd) {
	if m.voicePlay != msg.pb {
		return m, nil
	}
	m.voicePlay = nil
	if msg.pb.exitErr != nil && !msg.pb.stopped.Load() {
		m.mediaErr = fmt.Errorf("el reproductor falló (%v)%s", msg.pb.exitErr, stderrNote(msg.pb.stderr))
	}
	return m, nil
}

// stopVoicePlay stops the playing note.
func (m Model) stopVoicePlay() Model {
	if pb := m.voicePlay; pb != nil {
		pb.stopped.Store(true)
		signalGroup(pb.cmd, syscall.SIGKILL)
		m.voicePlay = nil
	}
	return m
}

// voicePlayLine is the chat tail's playback status.
func (m Model) voicePlayLine() (string, bool) {
	switch {
	case m.voicePlay == nil:
		return "", false
	case m.voicePlay.cmd == nil:
		return "Descargando nota de voz… · Esc detener", true
	}
	return "▶ reproduciendo… · Esc detener", true
}

// voicePlayBlocked says why Reproducir nota de voz cannot run now, or "".
func (m Model) voicePlayBlocked() string {
	if m.client == nil {
		return "sin conexión con el daemon"
	}
	if m.voicePlay != nil {
		return ""
	}
	if _, ok := m.newestVoiceKey(); !ok {
		return "no hay notas de voz"
	}
	if argv := audioPlayerArgv(m.envFunc()); argv != nil {
		if _, err := exec.LookPath(argv[0]); err != nil {
			return errNoAudioPlayer(argv[0]).Error()
		}
	}
	return ""
}
