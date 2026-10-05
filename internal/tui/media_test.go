package tui

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/kittygfx"
)

// mediaClient serves every Download with a small PNG written to the
// requested path, the way the daemon writes an attachment.
type mediaClient struct {
	replyClient
	downloads []string
}

func (c *mediaClient) Download(_ context.Context, id string, index int, destPath string, _ core.DownloadOptions) (core.DownloadResult, error) {
	c.downloads = append(c.downloads, id)
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 200, 100)))
	if err := os.WriteFile(destPath, buf.Bytes(), 0o600); err != nil {
		return core.DownloadResult{}, err
	}
	return core.DownloadResult{Path: destPath, Bytes: int64(buf.Len())}, nil
}

// runCmds executes cmd and every command it batches, returning the
// non-batch messages they produce. Timers (the chat keepalive and typing
// ticks) are abandoned after a short wait instead of slept through.
func runCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(500 * time.Millisecond):
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runCmds(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func imageChatModel(t *testing.T, gfx kittygfx.Mode) (Model, *mediaClient, *bytes.Buffer) {
	t.Helper()
	return imageChatModelWith(t, gfx, core.Attachment{Name: "foto.jpg", MIME: "image/jpeg", Size: 1234})
}

// imageChatModelWith opens a chat holding one item with attachments and
// runs every media fetch the open triggers.
func imageChatModelWith(t *testing.T, gfx kittygfx.Mode, attachments ...core.Attachment) (Model, *mediaClient, *bytes.Buffer) {
	t.Helper()
	client := &mediaClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.width, model.height = 60, 30
	var out bytes.Buffer
	model.gfx = gfx
	if gfx == kittygfx.Kitty {
		model.gfxOut = &out
		model.media = newMediaCache()
		model.mediaDir = t.TempDir()
	}
	model, cmd := openChat(model)
	msg := cmd().(chatThreadLoadedMsg)
	msg.items = []core.Item{{
		ID: "whatsapp:personal:img", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
		From: core.Address{Name: "Alice"}, Body: "mira", Timestamp: time.Now(),
		Attachments: attachments,
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
	return model, client, &out
}

func TestChatImageThumbnailWithKittyGraphics(t *testing.T) {
	model, client, out := imageChatModel(t, kittygfx.Kitty)
	if len(client.downloads) != 1 {
		t.Fatalf("downloads = %v, want one", client.downloads)
	}
	if !strings.Contains(out.String(), "\x1b_Ga=T,U=1") {
		t.Fatal("thumbnail was not uploaded to the terminal")
	}
	view := model.View()
	if !strings.ContainsRune(view, kittygfx.Placeholder) {
		t.Fatal("chat view shows no image placeholder")
	}
	thumb, _ := model.readyThumb(mediaKey("whatsapp:personal:img", 0))
	for _, line := range strings.Split(view, "\n") {
		if n := strings.Count(line, string(kittygfx.Placeholder)); n != 0 && n != thumb.cols {
			t.Errorf("a thumbnail row was split: %d of %d cells on one line", n, thumb.cols)
		}
	}
	if strings.Contains(view, "foto.jpg") {
		t.Error("an image shown as a thumbnail should not also be listed as a text attachment")
	}
	// A second pass must not refetch what is cached.
	if cmd := model.requestChatMedia(); cmd != nil {
		t.Error("requestChatMedia refetched a cached thumbnail")
	}
}

func TestChatImageWithoutGraphicsIsUnchanged(t *testing.T) {
	model, client, _ := imageChatModel(t, kittygfx.None)
	if len(client.downloads) != 0 {
		t.Fatalf("downloaded %v without graphics support", client.downloads)
	}
	view := model.View()
	if strings.ContainsRune(view, kittygfx.Placeholder) {
		t.Fatal("placeholder rendered without graphics support")
	}
	if !strings.Contains(view, "foto.jpg") {
		t.Error("attachment text row missing")
	}
}

func TestImageViewerKeysAndClose(t *testing.T) {
	model, _, out := imageChatModel(t, kittygfx.Kitty)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	model = updated.(Model)
	if model.viewer == nil {
		t.Fatal("Ctrl+O did not open the viewer")
	}
	if !strings.Contains(model.View(), "Imagen 1/1") {
		t.Fatalf("viewer header missing: %q", model.View())
	}
	if header := strings.Split(model.View(), "\n")[0]; !strings.Contains(header, "Esc/q/v cerrar") || strings.Contains(header, "Enter") {
		t.Errorf("viewer header %q should name every close key in the ↵ notation", header)
	}
	for _, m := range runCmds(cmd) {
		updated, upload := model.Update(m)
		model = updated.(Model)
		runCmds(upload)
	}
	if !strings.ContainsRune(model.View(), kittygfx.Placeholder) {
		t.Fatal("viewer shows no full-size image")
	}
	out.Reset()
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	runCmds(cmd)
	if model.viewer != nil || !model.chatMode {
		t.Fatal("Esc should close the viewer and stay in the chat")
	}
	if !strings.Contains(out.String(), "a=d,d=I") {
		t.Error("closing the viewer did not free the full-size image")
	}
}

func TestClickOnThumbnailOpensViewer(t *testing.T) {
	model, _, _ := imageChatModel(t, kittygfx.Kitty)
	y := -1
	for i, line := range strings.Split(model.View(), "\n") {
		if strings.ContainsRune(line, kittygfx.Placeholder) {
			y = i
			break
		}
	}
	if y < 0 {
		t.Fatal("no thumbnail line in the view")
	}
	updated, _ := model.Update(tea.MouseMsg{X: 5, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if updated.(Model).viewer == nil {
		t.Fatal("click on the thumbnail did not open the viewer")
	}
	updated, _ = model.Update(tea.MouseMsg{X: 5, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if updated.(Model).viewer != nil {
		t.Fatal("click off the thumbnail opened the viewer")
	}
}

func TestLockOutputKeepsTerminalFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, ok := lockOutput(f).(terminalFile); !ok {
		t.Error("locked *os.File no longer exposes Fd/Read/Close")
	}
	var buf bytes.Buffer
	w := lockOutput(&buf)
	_, _ = w.Write([]byte("x"))
	if buf.String() != "x" {
		t.Error("locked writer lost a write")
	}
}

// TestResizeClearsScreen pins the fix for a resize leaving the previous
// frame on screen: every size change asks for a full clear.
func TestResizeClearsScreen(t *testing.T) {
	model := NewModel(&mediaClient{})
	_, cmd := model.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	for _, msg := range runCmds(cmd) {
		if msg == tea.ClearScreen() {
			return
		}
	}
	t.Fatal("a resize should clear the screen before the next frame")
}
