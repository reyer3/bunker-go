package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/oggfixture"
)

// voiceClient is a chat client whose Download hands out a real Ogg Opus
// file and whose Reply remembers whether each call was a voice note.
type voiceClient struct {
	mediaClient
	mu     sync.Mutex
	voices []bool
}

func (c *voiceClient) Download(_ context.Context, id string, _ int, destPath string, _ core.DownloadOptions) (core.DownloadResult, error) {
	c.downloads = append(c.downloads, id)
	data := oggfixture.Bytes(12 * time.Second)
	if err := os.WriteFile(destPath, data, 0o600); err != nil {
		return core.DownloadResult{}, err
	}
	return core.DownloadResult{Path: destPath, Bytes: int64(len(data))}, nil
}

func (c *voiceClient) Reply(ctx context.Context, id, body string, cc, attachments []string, dryRun bool) (core.Plan, core.Receipt, error) {
	c.mu.Lock()
	c.voices = append(c.voices, core.IsVoice(ctx))
	c.mu.Unlock()
	return c.replyClient.Reply(ctx, id, body, cc, attachments, dryRun)
}

var voiceNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// voiceChatModel opens a WhatsApp chat holding one received voice note
// (12 s, with a waveform) and one plain audio file.
func voiceChatModel(t *testing.T, client *voiceClient) Model {
	t.Helper()
	clock := voiceNow
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.width, model.height = 60, 30
	model.mediaDir = t.TempDir()
	model.now = func() time.Time { return clock }
	model, cmd := openChat(model)
	msg := cmd().(chatThreadLoadedMsg)
	msg.items = []core.Item{
		{ID: "whatsapp:personal:song", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
			From: core.Address{Name: "Alice"}, Timestamp: voiceNow,
			Attachments: []core.Attachment{{Name: "cancion.mp3", MIME: "audio/mpeg", Size: 4096}}},
		{ID: "whatsapp:personal:voz", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
			From: core.Address{Name: "Alice"}, Timestamp: voiceNow,
			Attachments: []core.Attachment{{Name: "audio", MIME: "audio/ogg; codecs=opus", Size: 900, Voice: true, Duration: 12,
				Waveform: []byte{0, 20, 40, 60, 80, 100, 80, 60}}}},
	}
	updated, _ := model.Update(msg)
	return updated.(Model)
}

func voicePress(t *testing.T, m Model, key string) (Model, tea.Cmd) {
	t.Helper()
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		if rest, ok := strings.CutPrefix(key, "alt+"); ok {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(rest), Alt: true}
		} else {
			t.Fatalf("unknown key %q", key)
		}
	}
	updated, cmd := m.Update(msg)
	return updated.(Model), cmd
}

// script writes an executable shell script and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tool.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVoiceBubbleRendersLabelWaveformAndPlainAudio(t *testing.T) {
	model := voiceChatModel(t, &voiceClient{})
	view := model.View()
	if !strings.Contains(view, "🎤 Nota de voz · 0:12") {
		t.Fatalf("voice bubble missing:\n%s", view)
	}
	if !strings.ContainsAny(view, "▁▂▃▄▅▆▇█") {
		t.Fatalf("waveform bars missing:\n%s", view)
	}
	if !strings.Contains(view, "📎 cancion.mp3") {
		t.Fatalf("a plain audio file must keep its 📎 row:\n%s", view)
	}
	if strings.Count(view, "Nota de voz") != 1 {
		t.Fatalf("exactly one voice note expected:\n%s", view)
	}
}

func TestVoiceBubbleWithoutDurationOrWaveform(t *testing.T) {
	got := voiceBubbleTexts(core.Attachment{Voice: true}, 30)
	if len(got) != 1 || got[0] != "🎤 Nota de voz" {
		t.Fatalf("texts = %q", got)
	}
	if bars := waveformBarString([]byte{0, 100}, 8); bars != "▁█" {
		t.Fatalf("bars = %q", bars)
	}
	if bars := waveformBarString(make([]byte, 100), 10); len([]rune(bars)) != 10 {
		t.Fatalf("a long waveform must shrink to the width, got %d bars", len([]rune(bars)))
	}
}

func TestAltPPlaysNewestVoiceNoteAndEscStops(t *testing.T) {
	client := &voiceClient{}
	model := voiceChatModel(t, client)
	args := filepath.Join(t.TempDir(), "args")
	player := script(t, `echo "$@" > `+args+`; exec sleep 60`)
	model.getenv = func(k string) string {
		if k == "BUNKER_AUDIO_PLAYER" {
			return player + " --quiet"
		}
		return ""
	}

	model, cmd := voicePress(t, model, "alt+p")
	if cmd == nil || model.voicePlay == nil {
		t.Fatal("Alt+P should start fetching the newest voice note")
	}
	if !strings.Contains(model.View(), "Descargando nota de voz") {
		t.Fatalf("download state not shown:\n%s", model.View())
	}
	ready, ok := cmd().(voicePlayReadyMsg)
	if !ok || ready.err != nil {
		t.Fatalf("download result = %+v", ready)
	}
	if len(client.downloads) != 1 || client.downloads[0] != "whatsapp:personal:voz" {
		t.Fatalf("downloads = %v, want the voice note (not the mp3)", client.downloads)
	}
	updated, waitDone := model.Update(ready)
	model = updated.(Model)
	if waitDone == nil || model.voicePlay.cmd == nil {
		t.Fatal("the player did not start")
	}
	t.Cleanup(killVoiceProcs)
	if !strings.Contains(model.View(), "▶ reproduciendo… · Esc detener") {
		t.Fatalf("playing state not shown:\n%s", model.View())
	}

	waitFor(t, func() bool { b, err := os.ReadFile(args); return err == nil && len(b) > 0 })

	// Esc stops the note first; it does not leave the chat.
	model, _ = voicePress(t, model, "esc")
	if model.voicePlay != nil || !model.chatMode {
		t.Fatalf("Esc should stop playback and stay in the chat (voicePlay=%v chatMode=%v)", model.voicePlay, model.chatMode)
	}
	if strings.Contains(model.View(), "reproduciendo") {
		t.Fatal("the playing line should be gone")
	}
	if done, ok := waitDone().(voicePlayDoneMsg); !ok {
		t.Fatalf("the stopped player should be reaped, got %T", done)
	} else {
		updated, _ := model.Update(done)
		model = updated.(Model)
		if model.mediaErr != nil {
			t.Fatalf("a player we stopped is not an error: %v", model.mediaErr)
		}
	}
	got, err := os.ReadFile(args)
	if err != nil || !strings.HasPrefix(string(got), "--quiet ") || !strings.Contains(string(got), ".ogg") {
		t.Fatalf("player args = %q, %v; want the extra flag then the downloaded file", got, err)
	}
}

func TestVoicePlayerExitsByItselfClearsPlaying(t *testing.T) {
	model := voiceChatModel(t, &voiceClient{})
	player := script(t, `exit 0`)
	model.getenv = func(string) string { return player }
	model, cmd := voicePress(t, model, "alt+p")
	ready := cmd().(voicePlayReadyMsg)
	updated, waitDone := model.Update(ready)
	model = updated.(Model)
	updated, _ = model.Update(waitDone())
	model = updated.(Model)
	if model.voicePlay != nil || model.mediaErr != nil {
		t.Fatalf("voicePlay=%v err=%v, want idle and no error", model.voicePlay, model.mediaErr)
	}
}

func TestVoicePlayerFailureShowsStderr(t *testing.T) {
	model := voiceChatModel(t, &voiceClient{})
	player := script(t, `echo "sin salida de audio" >&2; exit 4`)
	model.getenv = func(string) string { return player }
	model, cmd := voicePress(t, model, "alt+p")
	ready := cmd().(voicePlayReadyMsg)
	updated, waitDone := model.Update(ready)
	model = updated.(Model)
	updated, _ = model.Update(waitDone())
	model = updated.(Model)
	if model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "sin salida de audio") {
		t.Fatalf("mediaErr = %v, want the player's stderr", model.mediaErr)
	}
}

func TestClickOnVoiceBubblePlaysIt(t *testing.T) {
	client := &voiceClient{}
	model := voiceChatModel(t, client)
	y := -1
	for i, line := range strings.Split(model.View(), "\n") {
		if strings.Contains(line, "Nota de voz") {
			y = i
		}
	}
	if y < 0 {
		t.Fatal("no voice bubble on screen")
	}
	player := script(t, `exit 0`)
	model.getenv = func(string) string { return player }
	// The row of the plain audio file is not a voice note.
	for i, line := range strings.Split(model.View(), "\n") {
		if strings.Contains(line, "cancion.mp3") {
			if _, cmd := model.Update(tea.MouseMsg{X: 5, Y: i, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}); cmd != nil {
				t.Fatal("clicking a plain audio file must not play it")
			}
		}
	}
	updated, cmd := model.Update(tea.MouseMsg{X: 5, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	model = updated.(Model)
	if cmd == nil || model.voicePlay == nil {
		t.Fatal("a click on the voice bubble should start playing it")
	}
	if ready, ok := cmd().(voicePlayReadyMsg); !ok || ready.err != nil {
		t.Fatalf("click download = %+v", ready)
	}
	if len(client.downloads) != 1 || client.downloads[0] != "whatsapp:personal:voz" {
		t.Fatalf("downloads = %v", client.downloads)
	}
}

func TestMissingAudioPlayerNamesWhatToInstall(t *testing.T) {
	model := voiceChatModel(t, &voiceClient{})
	model.getenv = func(string) string { return "bunker-no-such-player --x" }
	model, cmd := voicePress(t, model, "alt+p")
	if cmd != nil || model.voicePlay != nil {
		t.Fatal("nothing should be downloaded or started without a player")
	}
	if model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "falta bunker-no-such-player") || !strings.Contains(model.mediaErr.Error(), "BUNKER_AUDIO_PLAYER") {
		t.Fatalf("mediaErr = %v", model.mediaErr)
	}
	if !strings.Contains(model.View(), "Error: falta bunker-no-such-player") {
		t.Fatalf("the error is not on screen:\n%s", model.View())
	}

	// With the default player (mpv) absent from PATH too.
	t.Setenv("PATH", t.TempDir())
	model.getenv = func(string) string { return "" }
	model, _ = voicePress(t, model, "alt+p")
	if model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "falta mpv") {
		t.Fatalf("mediaErr = %v, want it to name mpv", model.mediaErr)
	}
}

func TestAudioPlayerArgv(t *testing.T) {
	none := func(string) string { return "" }
	if got := strings.Join(audioPlayerArgv(none), " "); got != "mpv --no-video --really-quiet" {
		t.Errorf("default = %q", got)
	}
	custom := func(k string) string {
		if k == "BUNKER_AUDIO_PLAYER" {
			return "ffplay -nodisp -autoexit"
		}
		return ""
	}
	if got := strings.Join(audioPlayerArgv(custom), " "); got != "ffplay -nodisp -autoexit" {
		t.Errorf("custom = %q", got)
	}
}

func TestNoVoiceNotesToPlay(t *testing.T) {
	client := &voiceClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model, _ = voicePress(t, updated.(Model), "alt+p")
	if model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "no hay notas de voz") {
		t.Fatalf("mediaErr = %v", model.mediaErr)
	}
}

// recorderCommand is a stand-in recorder: it "records" by copying a valid
// Ogg Opus fixture to {output} and then keeps running until interrupted,
// like ffmpeg does.
func recorderCommand(t *testing.T, d time.Duration) []string {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "fixture.ogg")
	if err := oggfixture.Write(fixture, d); err != nil {
		t.Fatal(err)
	}
	return []string{"sh", "-c", `cp "$1" "$2"; exec sleep 60`, "sh", fixture, voiceOutputPlaceholder}
}

// recorded waits until the stand-in recorder has written its file.
func recorded(t *testing.T, m Model) {
	t.Helper()
	path := m.voiceRec.path
	waitFor(t, func() bool { info, err := os.Stat(path); return err == nil && info.Size() > 0 })
}

func recordingModel(t *testing.T, argv []string) (Model, *voiceClient) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	client := &voiceClient{}
	client.previewOut = core.Plan{Voice: true, Recipients: []string{"5511999999999@s.whatsapp.net"},
		Attachments: []core.AttachmentInfo{{Name: "x.ogg", Voice: true, DurationMS: 3000}}}
	model := voiceChatModel(t, client)
	model.voiceRecordCmds = map[string][]string{voiceRecordKeyFor(core.ChannelWhatsApp, "personal"): argv}
	t.Cleanup(killVoiceProcs)
	return model, client
}

func tempVoiceFiles(t *testing.T) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(os.TempDir(), "bunker-voz-*"))
	return files
}

func TestRecordStopPreviewConfirmSendsVoiceNote(t *testing.T) {
	model, client := recordingModel(t, recorderCommand(t, 3*time.Second))

	model, cmd := voicePress(t, model, "alt+v")
	if model.voiceRec == nil || cmd == nil {
		t.Fatalf("Alt+V did not start recording (mediaErr=%v)", model.mediaErr)
	}
	rec := model.voiceRec
	if !strings.Contains(model.View(), "● Grabando 0:00 · ↵ enviar · Esc cancelar") {
		t.Fatalf("recording line missing:\n%s", model.View())
	}

	// The timer follows the model clock: no real waiting.
	clock := voiceNow.Add(7 * time.Second)
	model.now = func() time.Time { return clock }
	updated, _ := model.Update(voiceTickMsg{rec: rec})
	model = updated.(Model)
	if !strings.Contains(model.View(), "● Grabando 0:07") {
		t.Fatalf("timer not advanced:\n%s", model.View())
	}
	// Typing does not leak into the draft while recording.
	model, _ = voicePress(t, model, "alt+z")
	if model.composer.Value() != "" {
		t.Fatalf("composer = %q", model.composer.Value())
	}

	// Enter stops the recorder and goes on to the dry-run preview.
	recorded(t, model)
	model, stop := voicePress(t, model, "enter")
	stopped, ok := stop().(voiceStoppedMsg)
	if !ok || stopped.err != nil || stopped.dur != 3*time.Second {
		t.Fatalf("stop result = %+v", stopped)
	}
	updated, preview := model.Update(stopped)
	model = updated.(Model)
	if model.voiceRec != nil || !model.chatVoice || !model.chatPreviewPending || len(model.chatAttachments) != 1 {
		t.Fatalf("after stop: rec=%v voice=%v pending=%v attachments=%v", model.voiceRec, model.chatVoice, model.chatPreviewPending, model.chatAttachments)
	}
	path := model.chatAttachments[0]
	if path != rec.path {
		t.Fatalf("attachment = %q, want the recording %q", path, rec.path)
	}
	updated, _ = model.Update(preview())
	model = updated.(Model)
	if !model.chatConfirm || len(client.calls) != 1 || !client.calls[0].dryRun || !client.voices[0] {
		t.Fatalf("expected one voice dry-run preview, confirm=%v calls=%+v voices=%v", model.chatConfirm, client.calls, client.voices)
	}
	if client.calls[0].body != "" || len(client.calls[0].attach) != 1 || client.calls[0].attach[0] != path {
		t.Fatalf("preview call = %+v", client.calls[0])
	}
	if view := model.View(); !strings.Contains(view, "🎤 Nota de voz · 0:03") || strings.Contains(view, "bunker-voz") {
		t.Fatalf("the pending note should show as a voice note, not a temp file:\n%s", view)
	}
	unwrapped := strings.Join(strings.Fields(model.View()), " ")
	if !strings.Contains(unwrapped, "¿Enviar nota de voz (0:03) a 5511999999999@s.whatsapp.net? ↵ enviar · Esc cancelar") {
		t.Fatalf("confirm line missing:\n%s", model.View())
	}
	if len(client.calls) != 1 {
		t.Fatal("nothing may be sent before the confirm")
	}

	// Confirm: the real send carries the voice flag, the bubble reads as a
	// voice note, and the temp file goes away once it is sent.
	model, send := voicePress(t, model, "enter")
	if !strings.Contains(model.View(), "🎤 Nota de voz · 0:03") {
		t.Fatalf("optimistic bubble should read as a voice note:\n%s", model.View())
	}
	sent := send().(chatReplySentMsg)
	updated, _ = model.Update(sent)
	model = updated.(Model)
	if len(client.calls) != 2 || client.calls[1].dryRun || !client.voices[1] {
		t.Fatalf("calls = %+v voices = %v, want a real voice send", client.calls, client.voices)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the temp recording was not deleted after sending: %v", err)
	}
	if model.chatVoice || len(model.chatAttachments) != 0 {
		t.Fatal("voice state should be cleared after the send")
	}
}

func TestEscDuringConfirmDiscardsRecording(t *testing.T) {
	model, client := recordingModel(t, recorderCommand(t, 2*time.Second))
	model, _ = voicePress(t, model, "alt+v")
	recorded(t, model)
	model, stop := voicePress(t, model, "enter")
	updated, preview := model.Update(stop())
	model = updated.(Model)
	path := model.chatAttachments[0]
	updated, _ = model.Update(preview())
	model = updated.(Model)
	model, _ = voicePress(t, model, "esc")
	if model.chatConfirm || model.chatVoice || len(model.chatAttachments) != 0 {
		t.Fatal("Esc at the confirm must drop the note")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temp file survived: %v", err)
	}
	if client.sendCalls() != 0 {
		t.Fatal("nothing may be sent")
	}
}

func TestEscCancelsRecordingAndDeletesTempFile(t *testing.T) {
	model, client := recordingModel(t, recorderCommand(t, 2*time.Second))
	model, _ = voicePress(t, model, "alt+v")
	path := model.voiceRec.path
	// Wait for the stand-in recorder to have written its file.
	waitFor(t, func() bool { info, err := os.Stat(path); return err == nil && info.Size() > 0 })

	model, cmd := voicePress(t, model, "esc")
	if model.voiceRec != nil || cmd != nil {
		t.Fatalf("Esc should cancel recording (rec=%v)", model.voiceRec)
	}
	if !model.chatMode {
		t.Fatal("Esc while recording must not leave the chat")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temp file survived the cancel: %v", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("a cancelled recording must never reach the daemon: %+v", client.calls)
	}
	if len(model.chatTempFiles) != 0 {
		t.Fatalf("chatTempFiles = %v", model.chatTempFiles)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRecorderEarlyExitShowsStderr(t *testing.T) {
	model, _ := recordingModel(t, []string{"sh", "-c", `echo "no se pudo abrir el micrófono" >&2; exit 3 # ` + voiceOutputPlaceholder})
	model, cmd := voicePress(t, model, "alt+v")
	if model.voiceRec == nil {
		t.Fatalf("recording did not start: %v", model.mediaErr)
	}
	rec := model.voiceRec
	<-rec.done
	var exited tea.Msg
	for _, msg := range runCmds(cmd) {
		if _, ok := msg.(voiceExitedMsg); ok {
			exited = msg
		}
	}
	if exited == nil {
		t.Fatal("the early exit was not reported")
	}
	updated, _ := model.Update(exited)
	model = updated.(Model)
	if model.voiceRec != nil {
		t.Fatal("a dead recorder must not keep the recording state")
	}
	if model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "no se pudo abrir el micrófono") || !strings.Contains(model.mediaErr.Error(), "exit status 3") {
		t.Fatalf("mediaErr = %v, want the exit status and the stderr tail", model.mediaErr)
	}
	if !strings.Contains(model.View(), "Error: el grabador terminó antes de tiempo") {
		t.Fatalf("error not on screen:\n%s", model.View())
	}
	if files := tempVoiceFiles(t); len(files) != 0 {
		t.Fatalf("temp files left behind: %v", files)
	}
}

func TestRecorderThatWritesGarbageIsRejected(t *testing.T) {
	model, client := recordingModel(t, []string{"sh", "-c", `echo basura > "$1"; exec sleep 60`, "sh", voiceOutputPlaceholder})
	model, _ = voicePress(t, model, "alt+v")
	path := model.voiceRec.path
	waitFor(t, func() bool { info, err := os.Stat(path); return err == nil && info.Size() > 0 })
	model, stop := voicePress(t, model, "enter")
	updated, next := model.Update(stop())
	model = updated.(Model)
	if next != nil || model.chatVoice || model.chatPreviewPending {
		t.Fatal("an invalid recording must not reach the preview")
	}
	if model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "no se guardó bien") {
		t.Fatalf("mediaErr = %v", model.mediaErr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the bad file should be deleted")
	}
	if len(client.calls) != 0 {
		t.Fatal("nothing may be sent")
	}
}

func TestMissingRecorderNamesWhatToInstall(t *testing.T) {
	model, _ := recordingModel(t, []string{"bunker-no-such-recorder", voiceOutputPlaceholder})
	model, cmd := voicePress(t, model, "alt+v")
	if model.voiceRec != nil || cmd != nil {
		t.Fatal("no recording without its tool")
	}
	if model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "falta bunker-no-such-recorder") || !strings.Contains(model.mediaErr.Error(), "voice_record_command") {
		t.Fatalf("mediaErr = %v", model.mediaErr)
	}
	if files := tempVoiceFiles(t); len(files) != 0 {
		t.Fatalf("temp files left behind: %v", files)
	}

	// The default command needs ffmpeg.
	t.Setenv("PATH", t.TempDir())
	model.voiceRecordCmds = nil
	model, _ = voicePress(t, model, "alt+v")
	if model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "falta ffmpeg") {
		t.Fatalf("mediaErr = %v, want it to name ffmpeg", model.mediaErr)
	}
}

func TestRecordCommandMustMentionOutput(t *testing.T) {
	model, _ := recordingModel(t, []string{"sh", "-c", "true"})
	model, _ = voicePress(t, model, "alt+v")
	if model.voiceRec != nil || model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "{output}") {
		t.Fatalf("rec=%v err=%v, want a clear {output} error", model.voiceRec, model.mediaErr)
	}
	if files := tempVoiceFiles(t); len(files) != 0 {
		t.Fatalf("temp files left behind: %v", files)
	}
}

func TestRecordingStopsAtTheLengthCap(t *testing.T) {
	model, _ := recordingModel(t, recorderCommand(t, 2*time.Second))
	model, _ = voicePress(t, model, "alt+v")
	rec := model.voiceRec
	clock := voiceNow.Add(voiceMaxDuration)
	model.now = func() time.Time { return clock }
	updated, stop := model.Update(voiceTickMsg{rec: rec})
	model = updated.(Model)
	if stop == nil || !rec.stopping.Load() {
		t.Fatal("reaching the cap must stop the recorder")
	}
	if _, ok := stop().(voiceStoppedMsg); !ok {
		t.Fatal("the stop should report the finished recording")
	}
	if flash, ok := model.currentFlash(); !ok || !strings.Contains(flash, "5 min") {
		t.Fatalf("flash = %q", flash)
	}
}

func TestRecordingBlockedWhileDraftingAndInPalette(t *testing.T) {
	model, _ := recordingModel(t, recorderCommand(t, time.Second))
	model = typeRunes(model, "hola")
	model, cmd := voicePress(t, model, "alt+v")
	if model.voiceRec != nil || cmd != nil || model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "vacía el mensaje") {
		t.Fatalf("rec=%v err=%v", model.voiceRec, model.mediaErr)
	}

	// The palette lists both commands, with why one cannot run.
	var rec, play *paletteEntry
	entries := model.paletteCommands()
	for i := range entries {
		switch entries[i].label {
		case "Grabar nota de voz":
			rec = &entries[i]
		case "Reproducir nota de voz":
			play = &entries[i]
		}
	}
	if rec == nil || play == nil {
		t.Fatalf("palette lacks the voice commands: %+v", entries)
	}
	if rec.key != "Alt+V" || rec.reason == "" {
		t.Errorf("record entry = %+v, want Alt+V blocked by the draft", rec)
	}
	if play.key != "Alt+P" {
		t.Errorf("play entry = %+v", play)
	}
}

func TestVoiceCommandsFromConfig(t *testing.T) {
	cfg := config.Config{Accounts: []config.Account{
		{Channel: "whatsapp", Name: "a", Options: map[string]interface{}{"voice_record_command": []interface{}{"pw-record", "{output}"}}},
		{Channel: "matrix", Name: "b", Options: map[string]interface{}{"voice_record_command": "pw-record - | opusenc - {output}"}},
		{Channel: "mail", Name: "c", Options: map[string]interface{}{"voice_record_command": "ignored"}},
		{Channel: "whatsapp", Name: "d", Options: map[string]interface{}{}},
	}}
	got := voiceRecordCommandsFromConfig(cfg)
	if len(got) != 2 || strings.Join(got["whatsapp/a"], " ") != "pw-record {output}" || got["matrix/b"][0] != "sh" || got["matrix/b"][1] != "-c" {
		t.Fatalf("commands = %v", got)
	}

	// The string form lands inside shell text, so the path is quoted.
	m := Model{chatChannel: core.ChannelMatrix, chatAccount: "b", voiceRecordCmds: got}
	argv, err := m.voiceRecordArgv("/tmp/a b.ogg")
	if err != nil || argv[2] != "pw-record - | opusenc - '/tmp/a b.ogg'" {
		t.Fatalf("argv = %q, %v", argv, err)
	}
}

func TestDefaultRecordCommandIsOggOpusFFmpeg(t *testing.T) {
	m := Model{chatChannel: core.ChannelWhatsApp, chatAccount: "x"}
	argv, err := m.voiceRecordArgv("/tmp/v.ogg")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"ffmpeg", "-f pulse -i default", "-ac 1", "-ar 48000", "-c:a libopus", "-b:a 24k", "-application voip", "-y /tmp/v.ogg"} {
		if !strings.Contains(joined, want) {
			t.Errorf("default recorder %q lacks %q", joined, want)
		}
	}
}
