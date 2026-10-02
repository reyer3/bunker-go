package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
)

// Recording a voice note (Alt+V in a WhatsApp or Matrix chat). An external
// recorder writes Ogg Opus to a private temp file; Enter stops it
// gracefully (SIGINT, so ffmpeg finalizes the container), then the note
// goes through the usual dry-run preview and explicit confirm before
// anything is sent.

const (
	// voiceMaxDuration caps a recording; at the cap it stops by itself
	// and goes to the preview. The default command also passes -t, so a
	// recorder left behind ends on its own.
	voiceMaxDuration = 5 * time.Minute
	// voiceOutputPlaceholder is replaced with the temp file's path in a
	// voice_record_command.
	voiceOutputPlaceholder = "{output}"
)

// defaultVoiceRecordCommand records the default PulseAudio source (which
// PipeWire serves too) as 48 kHz mono Opus in Ogg.
var defaultVoiceRecordCommand = []string{
	"ffmpeg", "-hide_banner", "-loglevel", "error",
	"-f", "pulse", "-i", "default", "-ac", "1", "-ar", "48000",
	"-c:a", "libopus", "-b:a", "24k", "-application", "voip",
	"-t", fmt.Sprint(int(voiceMaxDuration / time.Second)),
	"-y", voiceOutputPlaceholder,
}

// voiceRecordKeyFor is the key voiceRecordCommands maps an account by.
func voiceRecordKeyFor(channel core.Channel, account string) string {
	return string(channel) + "/" + account
}

// voiceRecordCommandsFromConfig reads each WhatsApp and Matrix account's
// voice_record_command: a list (the exact command) or a string (run by
// sh -c, for pipelines), either with {output} where the file goes.
func voiceRecordCommandsFromConfig(cfg config.Config) map[string][]string {
	out := map[string][]string{}
	for _, acc := range cfg.Accounts {
		ch := core.Channel(acc.Channel)
		if ch != core.ChannelWhatsApp && ch != core.ChannelMatrix {
			continue
		}
		switch v := acc.Options["voice_record_command"].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				out[voiceRecordKeyFor(ch, acc.Name)] = []string{"sh", "-c", v}
			}
		case []interface{}:
			var argv []string
			for _, e := range v {
				if s, ok := e.(string); ok {
					argv = append(argv, s)
				}
			}
			if len(argv) > 0 {
				out[voiceRecordKeyFor(ch, acc.Name)] = argv
			}
		}
	}
	return out
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// voiceRecordArgv resolves the recorder command for the open chat's
// account, with {output} replaced by path. A command that never mentions
// {output} would record nowhere, so it is an error, not a silent guess.
func (m Model) voiceRecordArgv(path string) ([]string, error) {
	argv := defaultVoiceRecordCommand
	custom, isCustom := m.voiceRecordCmds[voiceRecordKeyFor(m.chatChannel, m.chatAccount)]
	if isCustom {
		argv = custom
	}
	if !strings.Contains(strings.Join(argv, "\x00"), voiceOutputPlaceholder) {
		return nil, fmt.Errorf("voice_record_command debe incluir %s, donde se escribe la grabación", voiceOutputPlaceholder)
	}
	out := make([]string, len(argv))
	for i, a := range argv {
		repl := path
		// In the string form (sh -c "…") the path lands inside shell
		// text, so it is quoted.
		if isCustom && len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" && i == 2 {
			repl = shellQuote(path)
		}
		out[i] = strings.ReplaceAll(a, voiceOutputPlaceholder, repl)
	}
	return out, nil
}

// errNoRecorder names what to install when the recorder is missing.
func errNoRecorder(name string) error {
	return fmt.Errorf("falta %s para grabar notas de voz (instálalo o define voice_record_command en la cuenta)", name)
}

// voiceRecording is a recorder process and the file it writes.
type voiceRecording struct {
	cmd      *exec.Cmd
	path     string
	started  time.Time
	stderr   *tailBuffer
	done     chan struct{}
	exitErr  error // set before done is closed
	stopping atomic.Bool
}

type (
	voiceTickMsg   struct{ rec *voiceRecording }
	voiceExitedMsg struct{ rec *voiceRecording }
	// voiceStoppedMsg reports the recorder stopped after Enter: dur is the
	// note's length, err why the file is unusable.
	voiceStoppedMsg struct {
		rec *voiceRecording
		dur time.Duration
		err error
	}
)

// voiceRecordBlocked says why a recording cannot start in the open chat,
// or "" when it can.
func (m Model) voiceRecordBlocked() string {
	switch {
	case m.client == nil:
		return "sin conexión con el daemon"
	case !m.chatMode:
		return "abre un chat"
	case m.chatChannel != core.ChannelWhatsApp && m.chatChannel != core.ChannelMatrix:
		return "solo en chats de WhatsApp y Matrix"
	case m.voiceRec != nil:
		return "ya estás grabando"
	case m.chatEditID != "" || m.chatAction != nil:
		return "termina primero la acción en curso"
	case strings.TrimSpace(m.composer.Value()) != "" || len(m.chatAttachments) > 0:
		return "una nota de voz va sola: vacía el mensaje y los adjuntos"
	}
	return ""
}

// startVoiceRecording starts the recorder into a fresh private temp file.
func (m Model) startVoiceRecording() (Model, tea.Cmd) {
	if reason := m.voiceRecordBlocked(); reason != "" {
		m.mediaErr = errors.New(reason)
		return m, nil
	}
	f, err := os.CreateTemp("", "bunker-voz-*.ogg")
	if err != nil {
		m.mediaErr = fmt.Errorf("no se pudo crear el archivo de la grabación: %w", err)
		return m, nil
	}
	path := f.Name()
	_ = f.Close()
	argv, err := m.voiceRecordArgv(path)
	if err == nil {
		if _, lerr := exec.LookPath(argv[0]); lerr != nil {
			err = errNoRecorder(filepath.Base(argv[0]))
		}
	}
	if err != nil {
		_ = os.Remove(path)
		m.mediaErr = err
		return m, nil
	}
	rec := &voiceRecording{path: path, started: m.clock(), stderr: &tailBuffer{max: voiceStderrTail}, done: make(chan struct{})}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = rec.stderr
	if err := cmd.Start(); err != nil {
		_ = os.Remove(path)
		if errors.Is(err, exec.ErrNotFound) {
			m.mediaErr = errNoRecorder(filepath.Base(argv[0]))
		} else {
			m.mediaErr = fmt.Errorf("grabador %s: %w", filepath.Base(argv[0]), err)
		}
		return m, nil
	}
	rec.cmd = cmd
	trackVoiceProc(cmd)
	go func() {
		rec.exitErr = cmd.Wait()
		untrackVoiceProc(cmd)
		close(rec.done)
	}()
	m.mediaErr = nil
	m.chatTempFiles = append(m.chatTempFiles, path)
	m.voiceRec = rec
	return m, tea.Batch(voiceTick(rec), func() tea.Msg {
		<-rec.done
		return voiceExitedMsg{rec: rec}
	})
}

func voiceTick(rec *voiceRecording) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return voiceTickMsg{rec: rec} })
}

// handleVoiceTick advances the timer shown while recording, and stops the
// recording at the length cap.
func (m Model) handleVoiceTick(msg voiceTickMsg) (tea.Model, tea.Cmd) {
	if m.voiceRec != msg.rec || msg.rec.stopping.Load() {
		return m, nil
	}
	if m.clock().Sub(msg.rec.started) >= voiceMaxDuration {
		m = m.withFlash("límite de 5 min alcanzado")
		return m.finishVoiceRecording()
	}
	return m, voiceTick(msg.rec)
}

// handleVoiceExited reports a recorder that died by itself (not
// installed devices, a bad command): the stderr tail says why.
func (m Model) handleVoiceExited(msg voiceExitedMsg) (tea.Model, tea.Cmd) {
	if m.voiceRec != msg.rec || msg.rec.stopping.Load() {
		return m, nil
	}
	m.voiceRec = nil
	m.chatTempFiles = removeTempFile(m.chatTempFiles, msg.rec.path)
	status := "salió sin error"
	if msg.rec.exitErr != nil {
		status = msg.rec.exitErr.Error()
	}
	m.mediaErr = fmt.Errorf("el grabador terminó antes de tiempo (%s)%s", status, stderrNote(msg.rec.stderr))
	return m, nil
}

// updateVoiceRecording handles keys while recording: Enter stops and goes
// to the preview, Esc cancels and deletes the file; nothing else acts.
func (m Model) updateVoiceRecording(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.voiceRec.stopping.Load() {
		return m, nil
	}
	switch msg.String() {
	case "enter", "ctrl+s":
		return m.finishVoiceRecording()
	case "esc":
		return m.cancelVoiceRecording(), nil
	}
	return m, nil
}

// finishVoiceRecording stops the recorder gracefully off the UI thread
// and reports voiceStoppedMsg.
func (m Model) finishVoiceRecording() (Model, tea.Cmd) {
	rec := m.voiceRec
	rec.stopping.Store(true)
	return m, func() tea.Msg {
		signalGroup(rec.cmd, syscall.SIGINT)
		select {
		case <-rec.done:
		case <-time.After(voiceKillGrace):
			signalGroup(rec.cmd, syscall.SIGKILL)
			<-rec.done
		}
		// A recorder interrupted on purpose exits non-zero (ffmpeg: 255),
		// so the judge is the file, not the status.
		d, err := core.OggOpusDuration(rec.path)
		if err != nil {
			return voiceStoppedMsg{rec: rec, err: fmt.Errorf("la grabación no se guardó bien%s", stderrNote(rec.stderr))}
		}
		return voiceStoppedMsg{rec: rec, dur: d}
	}
}

// cancelVoiceRecording kills the recorder and deletes the file.
func (m Model) cancelVoiceRecording() Model {
	if rec := m.voiceRec; rec != nil {
		rec.stopping.Store(true)
		signalGroup(rec.cmd, syscall.SIGKILL)
		m.chatTempFiles = removeTempFile(m.chatTempFiles, rec.path)
		m.voiceRec = nil
	}
	return m
}

// handleVoiceStopped attaches the finished note and asks for the dry-run
// preview, exactly like a typed message's Enter.
func (m Model) handleVoiceStopped(msg voiceStoppedMsg) (tea.Model, tea.Cmd) {
	if m.voiceRec != msg.rec {
		return m, nil // cancelled meanwhile
	}
	m.voiceRec = nil
	if msg.err != nil {
		m.chatTempFiles = removeTempFile(m.chatTempFiles, msg.rec.path)
		m.mediaErr = msg.err
		return m, nil
	}
	m.chatAttachments = []string{msg.rec.path}
	m.chatVoice = true
	m.chatVoiceDur = msg.dur
	m.chatSendErr = nil
	m.chatReplyToken++
	m.chatPreviewPending = true
	return m, m.chatSendCmd("", true)
}

// dropChatVoice forgets a voice note awaiting send and deletes its file.
func (m Model) dropChatVoice() Model {
	if m.chatVoice {
		m = m.clearChatAttachments()
	}
	return m
}

// voiceRecordLine is the composer's replacement while recording.
func (m Model) voiceRecordLine() string {
	if m.voiceRec.stopping.Load() {
		return "Terminando la grabación…"
	}
	elapsed := int(m.clock().Sub(m.voiceRec.started) / time.Second)
	return fmt.Sprintf("● Grabando %s · ↵ enviar · Esc cancelar", formatVoiceDuration(elapsed))
}
