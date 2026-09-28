package tui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/kittygfx"
)

// fakeFrame stands in for ffmpeg: a small PNG, or err.
func fakeFrame(t *testing.T, err error) {
	t.Helper()
	old := videoFrame
	videoFrame = func(context.Context, string) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 160, 90)))
		return buf.Bytes(), nil
	}
	t.Cleanup(func() { videoFrame = old })
}

func videoChatModel(t *testing.T, gfx kittygfx.Mode) (Model, *mediaClient) {
	t.Helper()
	client := &mediaClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.width, model.height = 60, 30
	model.gfx = gfx
	model.mediaDir = t.TempDir()
	if gfx == kittygfx.Kitty {
		model.gfxOut = &bytes.Buffer{}
		model.media = newMediaCache()
	}
	model, cmd := openChat(model)
	msg := cmd().(chatThreadLoadedMsg)
	msg.items = []core.Item{{
		ID: "whatsapp:personal:vid", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
		From: core.Address{Name: "Alice"}, Timestamp: time.Now(),
		Attachments: []core.Attachment{{Name: "clip.mp4", MIME: "video/mp4", Size: 4096}},
	}}
	updated, cmd := model.Update(msg)
	model = updated.(Model)
	for _, m := range runCmds(cmd) {
		if ready, ok := m.(mediaReadyMsg); ok {
			updated, upload := model.Update(ready)
			model = updated.(Model)
			runCmds(upload)
		}
	}
	return model, client
}

func TestVideoThumbnailAndClickToPlay(t *testing.T) {
	fakeFrame(t, nil)
	model, _ := videoChatModel(t, kittygfx.Kitty)
	view := model.View()
	if !strings.ContainsRune(view, kittygfx.Placeholder) || !strings.Contains(view, "▶ clip.mp4") {
		t.Fatalf("video bubble lacks its frame and ▶ caption:\n%s", view)
	}
	y := -1
	for i, line := range strings.Split(view, "\n") {
		if strings.ContainsRune(line, kittygfx.Placeholder) {
			y = i
			break
		}
	}
	updated, cmd := model.Update(tea.MouseMsg{X: 5, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	model = updated.(Model)
	if model.viewer != nil || cmd == nil {
		t.Fatal("a click on a video should play it, not open the image viewer")
	}
	ready, ok := cmd().(videoReadyMsg)
	if !ok || ready.err != nil || ready.path == "" {
		t.Fatalf("play command = %+v", ready)
	}
	if _, play := model.Update(ready); play == nil {
		t.Fatal("a downloaded video should start the player")
	}
}

func TestVideoWithoutFFmpegKeepsTextRow(t *testing.T) {
	fakeFrame(t, errors.New("falta ffmpeg"))
	model, _ := videoChatModel(t, kittygfx.Kitty)
	view := model.View()
	if strings.ContainsRune(view, kittygfx.Placeholder) {
		t.Fatal("no frame should be drawn when ffmpeg fails")
	}
	if !strings.Contains(view, "clip.mp4") {
		t.Fatal("the video's text row disappeared")
	}
}

func TestCtrlOPlaysNewestVideoWithoutGraphics(t *testing.T) {
	model, client := videoChatModel(t, kittygfx.None)
	if len(client.downloads) != 0 {
		t.Fatal("nothing should download without graphics until asked to play")
	}
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if cmd == nil {
		t.Fatal("Ctrl+O should play the newest video even inside tmux")
	}
	if ready, ok := cmd().(videoReadyMsg); !ok || ready.err != nil {
		t.Fatalf("play command = %+v", ready)
	}
}

func TestVideoPlayerCommand(t *testing.T) {
	noEnv := func(string) string { return "" }
	kitty := videoPlayerCommand(kittygfx.Kitty, noEnv, "/v.mp4").Args
	if kitty[0] != "mpv" || !strings.Contains(strings.Join(kitty, " "), "--vo=kitty") || kitty[len(kitty)-1] != "/v.mp4" {
		t.Errorf("kitty player = %v", kitty)
	}
	plain := videoPlayerCommand(kittygfx.None, noEnv, "/v.mp4").Args
	if strings.Contains(strings.Join(plain, " "), "--vo=") {
		t.Errorf("plain player forces a video output: %v", plain)
	}
	custom := videoPlayerCommand(kittygfx.Kitty, func(k string) string {
		if k == "BUNKER_VIDEO_PLAYER" {
			return "vlc --play-and-exit"
		}
		return ""
	}, "/v.mp4").Args
	if strings.Join(custom, " ") != "vlc --play-and-exit /v.mp4" {
		t.Errorf("custom player = %v", custom)
	}
}

func TestMissingPlayerIsReported(t *testing.T) {
	model, _ := videoChatModel(t, kittygfx.None)
	updated, _ := model.Update(videoDoneMsg{err: &exec.Error{Name: "mpv", Err: exec.ErrNotFound}})
	if !strings.Contains(updated.(Model).View(), "falta mpv") {
		t.Fatal("a missing mpv should be reported in the chat")
	}
}
