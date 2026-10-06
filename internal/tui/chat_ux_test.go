package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

var uxNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func keyAlt(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true} }

func uxPress(t *testing.T, m Model, msg tea.KeyMsg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	return updated.(Model), cmd
}

func clickAt(t *testing.T, m Model, y int) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(tea.MouseMsg{X: 5, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	return updated.(Model), cmd
}

// fakeClock is a settable model clock.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

// uxChat opens a chat holding a text message, a message with a file
// and a second message with another file, on a settable clock.
func uxChat(t *testing.T, client Client) (Model, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: uxNow}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.width, model.height = 60, 30
	model.now = clock.now
	model.mediaDir = t.TempDir()
	model, cmd := openChat(model)
	msg := cmd().(chatThreadLoadedMsg)
	msg.items = []core.Item{
		{ID: "whatsapp:personal:a", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
			From: core.Address{Name: "Alice"}, Body: "primer mensaje", Timestamp: uxNow},
		{ID: "whatsapp:personal:b", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
			From: core.Address{Name: "Alice"}, Body: "mira el informe", Timestamp: uxNow,
			Attachments: []core.Attachment{{Name: "informe.pdf", MIME: "application/pdf", Size: 2048}}},
		{ID: "whatsapp:personal:c", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
			FromMe: true, Body: "gracias", Timestamp: uxNow,
			Attachments: []core.Attachment{{Name: "otro.txt", MIME: "text/plain", Size: 10}}},
	}
	updated, _ := model.Update(msg)
	return updated.(Model), clock
}

func viewRow(t *testing.T, m Model, needle string) int {
	t.Helper()
	for i, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, needle) {
			return i
		}
	}
	t.Fatalf("%q not on screen:\n%s", needle, m.View())
	return -1
}

// recordingCopier is a clipboard with one tool installed.
type recordingCopier struct {
	tools   map[string]bool
	argv    [][]string
	stdin   []string
	runErr  error
	osc     bytes.Buffer
	haveOSC bool
}

func (r *recordingCopier) copier(env map[string]string) *textCopier {
	c := &textCopier{
		getenv: func(k string) string { return env[k] },
		lookPath: func(name string) (string, error) {
			if r.tools[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		run: func(_ context.Context, argv []string, stdin string) error {
			r.argv = append(r.argv, argv)
			r.stdin = append(r.stdin, stdin)
			return r.runErr
		},
	}
	if r.haveOSC {
		c.osc = &r.osc
	}
	return c
}

func TestSelectModeReleasesAndRestoresTheMouse(t *testing.T) {
	model, _ := uxChat(t, &replyClient{})
	for _, key := range []tea.KeyMsg{{Type: tea.KeyF7}, keyAlt('s')} {
		on, cmd := uxPress(t, model, key)
		if !on.selectMode || cmd == nil || cmd() != tea.DisableMouse() {
			t.Fatalf("%v should release the mouse (mode=%v)", key, on.selectMode)
		}
		if !strings.Contains(on.View(), "Modo selección: selecciona con el ratón · Esc/F7 volver") {
			t.Fatalf("no selection banner:\n%s", on.View())
		}
		// Esc leaves the mode, not the chat.
		off, cmd := uxPress(t, on, tea.KeyMsg{Type: tea.KeyEsc})
		if off.selectMode || !off.chatMode || cmd == nil || cmd() != tea.EnableMouseCellMotion() {
			t.Fatalf("Esc should capture the mouse again and stay in the chat (mode=%v chat=%v)", off.selectMode, off.chatMode)
		}
		if strings.Contains(off.View(), "Modo selección") {
			t.Fatal("banner still shown after leaving the mode")
		}
		// F7 toggles back too.
		again, _ := uxPress(t, on, tea.KeyMsg{Type: tea.KeyF7})
		if again.selectMode {
			t.Fatal("F7 did not leave the mode")
		}
	}
}

func TestSelectModeDoesNotTouchEscOutsideTheMode(t *testing.T) {
	model, _ := uxChat(t, &replyClient{})
	left, _ := uxPress(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if left.chatMode {
		t.Fatal("Esc outside the selection mode should still leave the chat")
	}
}

func TestCopyNewestMessageUsesTheClipboardTool(t *testing.T) {
	model, _ := uxChat(t, &replyClient{})
	rc := &recordingCopier{tools: map[string]bool{"xclip": true, "xsel": true}, haveOSC: true}
	model.copier = rc.copier(map[string]string{"DISPLAY": ":0"})

	model, cmd := uxPress(t, model, keyAlt('y'))
	if cmd == nil {
		t.Fatal("Alt+Y returned no command")
	}
	msg := cmd()
	if len(rc.argv) != 1 || !reflect.DeepEqual(rc.argv[0], []string{"xclip", "-selection", "clipboard"}) || rc.stdin[0] != "gracias" {
		t.Fatalf("clipboard call = %v / %q, want xclip with the newest message", rc.argv, rc.stdin)
	}
	if rc.osc.Len() != 0 {
		t.Fatal("OSC 52 was written although a clipboard tool worked")
	}
	updated, _ := model.Update(msg)
	if flash, ok := updated.(Model).currentFlash(); !ok || flash != "Copiado" {
		t.Fatalf("flash = %q, want Copiado", flash)
	}
}

func TestCopyUsesWlCopyOnWayland(t *testing.T) {
	model, _ := uxChat(t, &replyClient{})
	rc := &recordingCopier{tools: map[string]bool{"wl-copy": true, "xclip": true}}
	model.copier = rc.copier(map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"})
	_, cmd := uxPress(t, model, keyAlt('y'))
	cmd()
	if len(rc.argv) != 1 || rc.argv[0][0] != "wl-copy" {
		t.Fatalf("argv = %v, want wl-copy", rc.argv)
	}
}

func TestClickSelectsAMessageAndAltYCopiesIt(t *testing.T) {
	model, _ := uxChat(t, &replyClient{})
	rc := &recordingCopier{tools: map[string]bool{"xclip": true}}
	model.copier = rc.copier(map[string]string{"DISPLAY": ":0"})

	model, cmd := clickAt(t, model, viewRow(t, model, "primer mensaje"))
	if cmd != nil || model.chatFocus != "whatsapp:personal:a" {
		t.Fatalf("click should select the bubble silently (focus=%q cmd=%v)", model.chatFocus, cmd != nil)
	}
	model, cmd = uxPress(t, model, keyAlt('y'))
	cmd()
	if len(rc.stdin) != 1 || rc.stdin[0] != "primer mensaje" {
		t.Fatalf("copied %q, want the selected message", rc.stdin)
	}
	// A click off any bubble drops the selection: the newest is copied.
	model, _ = clickAt(t, model, 0)
	if model.chatFocus != "" {
		t.Fatalf("focus = %q after clicking the header", model.chatFocus)
	}
}

func TestSelectedBubbleIsHighlighted(t *testing.T) {
	model, _ := uxChat(t, &replyClient{})
	model.render = nil
	plain := strings.Join(model.chatBodyLines(), "\n")
	model.chatFocus = "whatsapp:personal:a"
	lines, meta := model.chatBodyMeta()
	var found bool
	for i, l := range lines {
		if meta[i].item == "whatsapp:personal:a" && strings.Contains(l, "primer mensaje") {
			found = true
		}
	}
	if !found {
		t.Fatal("the selected message's lines are not tagged with its ID")
	}
	if strings.Join(lines, "\n") != plain && !strings.Contains(strings.Join(lines, "\n"), "primer mensaje") {
		t.Fatal("selected bubble lost its text")
	}
}

func TestCopyFallsBackToOSC52WithoutATool(t *testing.T) {
	model, _ := uxChat(t, &replyClient{})
	rc := &recordingCopier{tools: map[string]bool{}, haveOSC: true}
	model.copier = rc.copier(map[string]string{"DISPLAY": ":0"})
	model, cmd := uxPress(t, model, keyAlt('y'))
	msg := cmd()
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("gracias")) + "\a"
	if rc.osc.String() != want {
		t.Fatalf("OSC = %q, want %q", rc.osc.String(), want)
	}
	updated, _ := model.Update(msg)
	if flash, _ := updated.(Model).currentFlash(); !strings.HasPrefix(flash, "Copiado") {
		t.Fatalf("flash = %q", flash)
	}
}

func TestCopyOSC52WrapsForTmux(t *testing.T) {
	var out bytes.Buffer
	c := newTextCopier(func(string) string { return "" }, &out, true)
	c.lookPath = func(string) (string, error) { return "", errors.New("no") }
	via, err := c.Copy(context.Background(), "hola")
	if err != nil || via != "OSC 52" {
		t.Fatalf("Copy = %q, %v", via, err)
	}
	inner := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hola")) + "\a"
	if out.String() != tmuxPassthrough(inner) {
		t.Fatalf("out = %q", out.String())
	}
}

func TestCopyToolFailureFallsBackAndNoPathIsLoud(t *testing.T) {
	model, _ := uxChat(t, &replyClient{})
	rc := &recordingCopier{tools: map[string]bool{"xclip": true}, runErr: errors.New("boom"), haveOSC: true}
	model.copier = rc.copier(map[string]string{"DISPLAY": ":0"})
	_, cmd := uxPress(t, model, keyAlt('y'))
	cmd()
	if rc.osc.Len() == 0 {
		t.Fatal("a failing tool should fall back to OSC 52")
	}

	// Neither a tool nor a terminal to write to: a loud error.
	model, _ = uxChat(t, &replyClient{})
	none := &recordingCopier{tools: map[string]bool{}}
	model.copier = none.copier(nil2env)
	model, cmd = uxPress(t, model, keyAlt('y'))
	updated, _ := model.Update(cmd())
	got := updated.(Model)
	if got.mediaErr == nil || !strings.Contains(got.mediaErr.Error(), "no se pudo copiar") || !strings.Contains(got.mediaErr.Error(), "wl-copy") {
		t.Fatalf("mediaErr = %v, want a loud Spanish error naming the tools", got.mediaErr)
	}
	if !strings.Contains(got.View(), "Error: no se pudo copiar") {
		t.Fatalf("error not on screen:\n%s", got.View())
	}
}

var nil2env = map[string]string{}

func TestCopyRefusesTooLongOSC52(t *testing.T) {
	var out bytes.Buffer
	c := newTextCopier(func(string) string { return "" }, &out, false)
	c.lookPath = func(string) (string, error) { return "", errors.New("no") }
	if _, err := c.Copy(context.Background(), strings.Repeat("x", osc52MaxBytes+1)); err == nil || out.Len() != 0 {
		t.Fatalf("err = %v, wrote %d bytes", err, out.Len())
	}
}

func TestCopyEmptyChatIsAnError(t *testing.T) {
	model, _ := openedChat(t)
	model, cmd := uxPress(t, model, keyAlt('y'))
	if cmd != nil || model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "no hay mensajes") {
		t.Fatalf("cmd=%v err=%v", cmd != nil, model.mediaErr)
	}
}

func TestCopyInMailThreadCopiesTheSelectedBody(t *testing.T) {
	client := &replyClient{}
	model := mailThreadReadyModel(client, "mail:cl:1")
	model, cmd := openThread(model)
	msg := cmd().(threadLoadedMsg)
	msg.items = []core.Item{{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
		From: core.Address{Name: "Bob"}, Subject: "hola", Body: "cuerpo del correo", Timestamp: uxNow}}
	updated, _ := model.Update(msg)
	model = updated.(Model)
	rc := &recordingCopier{tools: map[string]bool{"wl-copy": true}}
	model.copier = rc.copier(map[string]string{"WAYLAND_DISPLAY": "w"})
	_, cmd = uxPress(t, model, keyAlt('y'))
	cmd()
	if len(rc.stdin) != 1 || rc.stdin[0] != "cuerpo del correo" {
		t.Fatalf("copied %q", rc.stdin)
	}
}

func TestRegisterClickDetectsDoubleClicksByTheModelClock(t *testing.T) {
	clock := &fakeClock{t: uxNow}
	m := Model{now: clock.now}

	m, double := m.registerClick("k1")
	if double {
		t.Fatal("a single click is not a double click")
	}
	clock.t = clock.t.Add(399 * time.Millisecond)
	m, double = m.registerClick("k1")
	if !double {
		t.Fatal("two presses 399ms apart on the same line should double-click")
	}
	// A double click consumes both presses.
	clock.t = clock.t.Add(10 * time.Millisecond)
	m, double = m.registerClick("k1")
	if double {
		t.Fatal("the third press starts over")
	}
	// Too slow.
	clock.t = clock.t.Add(401 * time.Millisecond)
	m, double = m.registerClick("k1")
	if double {
		t.Fatal("401ms apart is two single clicks")
	}
	// Another line.
	clock.t = clock.t.Add(50 * time.Millisecond)
	m, double = m.registerClick("k2")
	if double {
		t.Fatal("different lines are not a double click")
	}
	// A press on nothing in between breaks it.
	m, _ = m.registerClick("")
	clock.t = clock.t.Add(50 * time.Millisecond)
	if _, double = m.registerClick("k2"); double {
		t.Fatal("an empty press in between breaks the double click")
	}
}

type opened struct{ argv [][]string }

func (o *opened) open(argv []string) error {
	o.argv = append(o.argv, append([]string(nil), argv...))
	return nil
}

func TestDoubleClickOnAnAttachmentLineDownloadsAndOpensIt(t *testing.T) {
	client := &mediaClient{}
	model, clock := uxChat(t, client)
	op := &opened{}
	model.openFile = op.open
	y := viewRow(t, model, "informe.pdf")

	model, cmd := clickAt(t, model, y)
	if cmd != nil || len(client.downloads) != 0 {
		t.Fatal("a single click on an attachment line must do nothing")
	}
	clock.t = clock.t.Add(200 * time.Millisecond)
	model, cmd = clickAt(t, model, y)
	if cmd == nil {
		t.Fatal("double click returned no command")
	}
	if flash, _ := model.currentFlash(); flash != "Abriendo informe.pdf…" {
		t.Fatalf("flash = %q", flash)
	}
	msg := cmd()
	if len(client.downloads) != 1 || client.downloads[0] != "whatsapp:personal:b" {
		t.Fatalf("downloads = %v, want the clicked message's file", client.downloads)
	}
	updated, _ := model.Update(msg)
	if len(op.argv) != 1 || len(op.argv[0]) != 2 || op.argv[0][0] != "xdg-open" || !strings.HasPrefix(op.argv[0][1], model.mediaDir) || !strings.HasSuffix(op.argv[0][1], ".pdf") {
		t.Fatalf("opener argv = %v, want xdg-open <cached .pdf>", op.argv)
	}
	if updated.(Model).mediaErr != nil {
		t.Fatalf("mediaErr = %v", updated.(Model).mediaErr)
	}
}

func TestDoubleClickNeedsTheSameLineWithinTheWindow(t *testing.T) {
	client := &mediaClient{}
	model, clock := uxChat(t, client)
	op := &opened{}
	model.openFile = op.open
	a, b := viewRow(t, model, "informe.pdf"), viewRow(t, model, "otro.txt")

	model, _ = clickAt(t, model, a)
	clock.t = clock.t.Add(100 * time.Millisecond)
	model, cmd := clickAt(t, model, b)
	if cmd != nil {
		t.Fatal("clicks on two different attachments are not a double click")
	}
	clock.t = clock.t.Add(500 * time.Millisecond)
	_, cmd = clickAt(t, model, b)
	if cmd != nil || len(client.downloads) != 0 || len(op.argv) != 0 {
		t.Fatal("clicks 500ms apart are not a double click")
	}
}

func TestOpenerEnvOverrideIsUsedWithItsArguments(t *testing.T) {
	client := &mediaClient{}
	model, _ := uxChat(t, client)
	op := &opened{}
	model.openFile = op.open
	model.getenv = func(k string) string {
		if k == "BUNKER_OPEN_FILE" {
			return "mi-visor --nueva-ventana"
		}
		return ""
	}
	model, cmd := uxPress(t, model, keyAlt('o'))
	updated, _ := model.Update(cmd())
	_ = updated
	if len(op.argv) != 1 || op.argv[0][0] != "mi-visor" || op.argv[0][1] != "--nueva-ventana" || len(op.argv[0]) != 3 {
		t.Fatalf("argv = %v", op.argv)
	}
	// Alt+O opens the newest message with a file: otro.txt.
	if client.downloads[0] != "whatsapp:personal:c" || !strings.HasSuffix(op.argv[0][2], ".txt") {
		t.Fatalf("downloads = %v, argv = %v", client.downloads, op.argv)
	}
}

func TestAltOOpensTheSelectedMessagesAttachment(t *testing.T) {
	client := &mediaClient{}
	model, _ := uxChat(t, client)
	op := &opened{}
	model.openFile = op.open
	model, _ = clickAt(t, model, viewRow(t, model, "mira el informe"))
	_, cmd := uxPress(t, model, keyAlt('o'))
	cmd()
	if len(client.downloads) != 1 || client.downloads[0] != "whatsapp:personal:b" {
		t.Fatalf("downloads = %v, want the selected message's file", client.downloads)
	}
}

func TestMissingOpenerIsALoudErrorBeforeDownloading(t *testing.T) {
	client := &mediaClient{}
	model, _ := uxChat(t, client)
	model.getenv = func(k string) string {
		if k == "BUNKER_OPEN_FILE" {
			return "/nonexistent/bunker-opener"
		}
		return ""
	}
	model, cmd := uxPress(t, model, keyAlt('o'))
	if cmd != nil || len(client.downloads) != 0 {
		t.Fatal("nothing should be downloaded without an opener")
	}
	if model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "BUNKER_OPEN_FILE") || !strings.Contains(model.mediaErr.Error(), "falta") {
		t.Fatalf("mediaErr = %v", model.mediaErr)
	}
	if !strings.Contains(model.View(), "Error: falta /nonexistent/bunker-opener") {
		t.Fatalf("error not on screen:\n%s", model.View())
	}
}

func TestStartDetachedReportsAMissingOpener(t *testing.T) {
	err := startDetached([]string{"/nonexistent/bunker-opener", "x"})
	if err == nil || !strings.Contains(err.Error(), "BUNKER_OPEN_FILE") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAttachmentDownloadFailureIsReported(t *testing.T) {
	client := &replyClient{}
	client.downloadErr = errors.New("sin red")
	model, _ := uxChat(t, client)
	op := &opened{}
	model.openFile = op.open
	model, cmd := uxPress(t, model, keyAlt('o'))
	updated, _ := model.Update(cmd())
	got := updated.(Model)
	if len(op.argv) != 0 || got.mediaErr == nil || !strings.Contains(got.mediaErr.Error(), "no se pudo descargar otro.txt") {
		t.Fatalf("argv=%v err=%v", op.argv, got.mediaErr)
	}
}

func TestAltOWithoutAttachmentsIsAnError(t *testing.T) {
	model, _ := openedChat(t)
	model.openFile = (&opened{}).open
	model, cmd := uxPress(t, model, keyAlt('o'))
	if cmd != nil || model.mediaErr == nil || !strings.Contains(model.mediaErr.Error(), "no hay adjuntos") {
		t.Fatalf("cmd=%v err=%v", cmd != nil, model.mediaErr)
	}
}

func TestDoubleClickOpensAMailThreadAttachment(t *testing.T) {
	client := &mediaClient{}
	model := mailThreadReadyModel(client, "mail:cl:1")
	clock := &fakeClock{t: uxNow}
	model.now = clock.now
	model.mediaDir = t.TempDir()
	model, cmd := openThread(model)
	msg := cmd().(threadLoadedMsg)
	msg.items = []core.Item{{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
		From: core.Address{Name: "Bob"}, Subject: "hola", Body: "mira", Timestamp: uxNow,
		Attachments: []core.Attachment{{Name: "factura.pdf", MIME: "application/pdf", Size: 99}}}}
	updated, _ := model.Update(msg)
	model = updated.(Model)
	op := &opened{}
	model.openFile = op.open
	y := viewRow(t, model, "factura.pdf")

	model, cmd = clickAt(t, model, y)
	if cmd != nil {
		t.Fatal("single click should not open")
	}
	clock.t = clock.t.Add(150 * time.Millisecond)
	model, cmd = clickAt(t, model, y)
	if cmd == nil {
		t.Fatal("double click on the attachment line did not open it")
	}
	updated, _ = model.Update(cmd())
	if len(client.downloads) != 1 || client.downloads[0] != "mail:cl:1" || len(op.argv) != 1 || op.argv[0][0] != "xdg-open" {
		t.Fatalf("downloads=%v argv=%v", client.downloads, op.argv)
	}
	_ = updated

	// A click on the body text is not an attachment.
	clock.t = clock.t.Add(time.Second)
	body := viewRow(t, model, "mira")
	model, _ = clickAt(t, model, body)
	clock.t = clock.t.Add(100 * time.Millisecond)
	if _, cmd = clickAt(t, model, body); cmd != nil {
		t.Fatal("double click on body text opened something")
	}
}

func TestDoubleClickOnAnImageInTheViewerOpensItExternally(t *testing.T) {
	client := &mediaClient{}
	model, clock := uxChat(t, client)
	op := &opened{}
	model.openFile = op.open
	key := mediaKey("whatsapp:personal:b", 0)
	model.viewer = &imageViewer{keys: []string{key}, index: 0}
	model.lastClickKey, model.lastClickAt = key, clock.t // the click that opened it
	clock.t = clock.t.Add(120 * time.Millisecond)
	model, cmd := clickAt(t, model, 0)
	if model.viewer != nil || cmd == nil {
		t.Fatalf("viewer=%v cmd=%v, want it closed and the file opening", model.viewer, cmd != nil)
	}
	model.Update(cmd())
	if len(client.downloads) != 1 {
		t.Fatalf("downloads = %v", client.downloads)
	}
}

// --- sending with one Enter ---

func realSends(c *replyClient) (real, dry int) {
	for _, call := range c.calls {
		if call.dryRun {
			dry++
		} else {
			real++
		}
	}
	return
}

func plainChat(t *testing.T, client *replyClient) Model {
	t.Helper()
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	return typeRunes(updated.(Model), "hola")
}

func TestPlainTextSendsWithASingleEnter(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"alice"}}, sendRcpt: core.Receipt{ID: "whatsapp:personal:9"}}
	model := plainChat(t, client)

	model, previewCmd := uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if previewCmd == nil || !model.chatPreviewPending {
		t.Fatal("Enter should request the dry-run first")
	}
	updated, sendCmd := model.Update(previewCmd())
	model = updated.(Model)
	if sendCmd == nil || !model.chatSendBusy() || model.chatConfirm {
		t.Fatalf("a clean plan should send at once (sending=%v confirm=%v)", model.chatSendBusy(), model.chatConfirm)
	}
	if model.composer.Value() != "" || lastBubble(model) == nil || lastBubble(model).body != "hola" {
		t.Fatal("the send should show the optimistic bubble and clear the composer")
	}
	updated, _ = model.Update(sendCmd())
	model = updated.(Model)

	if real, dry := realSends(client); real != 1 || dry != 1 {
		t.Fatalf("real sends = %d, dry-runs = %d, want 1 and 1 (dry-run first)", real, dry)
	}
	if !client.calls[0].dryRun || client.calls[1].dryRun || client.calls[1].body != "hola" {
		t.Fatalf("calls = %+v, want a dry-run then the real send", client.calls)
	}
	if model.chatSendBusy() {
		t.Fatal("still sending after the receipt")
	}
}

func TestPlanErrorShowsTheErrorAndDoesNotSend(t *testing.T) {
	client := &replyClient{previewErr: errors.New("sin permiso")}
	model := plainChat(t, client)
	model, previewCmd := uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, cmd := model.Update(previewCmd())
	model = updated.(Model)
	if cmd != nil || model.chatSendBusy() || model.chatConfirm || model.chatAutoSend {
		t.Fatalf("a failed plan must not send (cmd=%v sending=%v confirm=%v)", cmd != nil, model.chatSendBusy(), model.chatConfirm)
	}
	if model.chatSendErr == nil || !strings.Contains(model.View(), "sin permiso") {
		t.Fatalf("error not shown:\n%s", model.View())
	}
	if model.composer.Value() != "hola" {
		t.Fatal("the draft must stay")
	}
	if real, dry := realSends(client); real != 0 || dry != 1 {
		t.Fatalf("real=%d dry=%d", real, dry)
	}
	// The next Enter is a clean retry, not a stuck state.
	client.previewErr = nil
	_, cmd = uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter after the error should retry")
	}
}

func TestAttachmentsStillNeedAnExplicitConfirm(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"alice"}}}
	model := plainChat(t, client)
	file := filepath.Join(t.TempDir(), "nota.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	model.chatAttachments = []string{file}
	model, previewCmd := uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, cmd := model.Update(previewCmd())
	model = updated.(Model)
	if cmd != nil || !model.chatConfirm || model.chatSendBusy() {
		t.Fatalf("an attachment must wait for the confirm (confirm=%v sending=%v)", model.chatConfirm, model.chatSendBusy())
	}
	if real, _ := realSends(client); real != 0 {
		t.Fatal("sent without the second Enter")
	}
}

func TestVoiceNoteStillNeedsAnExplicitConfirm(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"alice"}, Voice: true}}
	model := plainChat(t, client)
	file := filepath.Join(t.TempDir(), "voz.ogg")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	model.chatAttachments, model.chatVoice = []string{file}, true
	model, previewCmd := uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, cmd := model.Update(previewCmd())
	if cmd != nil || !updated.(Model).chatConfirm {
		t.Fatal("a voice note must wait for the confirm")
	}
}

func TestNewConversationStillNeedsAnExplicitConfirm(t *testing.T) {
	client := &replyClient{outgoingPreview: core.Plan{Recipients: []string{"5511999999999"}}}
	model := chatReadyModel(client, "whatsapp:personal:1")
	next, _ := model.pickContact(core.Contact{Channel: core.ChannelWhatsApp, Account: "personal", Address: "5511999999999", Name: "Nueva"})
	model = next.(Model)
	model = typeRunes(model, "hola")
	if !model.chatIsNewConversation() {
		t.Fatal("fixture should be a new conversation")
	}
	model, previewCmd := uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, cmd := model.Update(previewCmd())
	model = updated.(Model)
	if cmd != nil || !model.chatConfirm || model.chatSendBusy() {
		t.Fatalf("a new conversation must wait for the confirm (confirm=%v sending=%v)", model.chatConfirm, model.chatSendBusy())
	}
	if len(client.outgoingCalls) != 1 || !client.outgoingCalls[0].dryRun {
		t.Fatalf("outgoing calls = %+v, want only the dry-run", client.outgoingCalls)
	}
}

func TestEditStillNeedsAnExplicitConfirm(t *testing.T) {
	model := plainChat(t, &replyClient{})
	model.chatEditID = "whatsapp:personal:1"
	if model.chatSendsOnOneEnter() {
		t.Fatal("an edit goes through its own preview and confirm")
	}
}

func TestConfirmChatSendOptionRestoresTheTwoStepFlow(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"alice"}}}
	model := plainChat(t, client)
	model.confirmChatSend = true
	model, previewCmd := uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, cmd := model.Update(previewCmd())
	model = updated.(Model)
	if cmd != nil || !model.chatConfirm || model.chatSendBusy() {
		t.Fatal("confirm_chat_send = true must wait for a second Enter")
	}
	if !strings.Contains(model.View(), "¿Enviar a alice?") {
		t.Fatalf("confirm line missing:\n%s", model.View())
	}
	model, sendCmd := uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if sendCmd == nil || !model.chatSendBusy() {
		t.Fatal("the second Enter should send")
	}
	sendCmd()
	if real, dry := realSends(client); real != 1 || dry != 1 {
		t.Fatalf("real=%d dry=%d", real, dry)
	}
}

// TestFastDoubleEnterSendsOnce pins that the one-Enter flow does not bring
// back the "tengo que dar como 4 enters" bug in reverse: an impatient
// second Enter, while the preview or the send is in flight, is ignored.
func TestFastDoubleEnterSendsOnce(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"alice"}}, sendRcpt: core.Receipt{ID: "whatsapp:personal:9"}}
	model := plainChat(t, client)

	model, previewCmd := uxPress(t, model, keyEnter)
	token := model.chatReplyToken
	model, again := uxPress(t, model, keyEnter) // while the preview is in flight
	if again != nil || model.chatReplyToken != token {
		t.Fatal("an Enter during the preview must be a no-op")
	}
	updated, sendCmd := model.Update(previewCmd())
	model = updated.(Model)
	model, again = uxPress(t, model, keyEnter) // while the send is in flight
	if again != nil {
		t.Fatal("an Enter during the send must be a no-op")
	}
	updated, _ = model.Update(sendCmd())
	model = updated.(Model)
	// And once it is done, an Enter on the now empty composer does nothing.
	if _, again = uxPress(t, model, keyEnter); again != nil {
		t.Fatal("Enter on an empty composer sent something")
	}
	if real, dry := realSends(client); real != 1 || dry != 1 {
		t.Fatalf("real sends = %d, dry-runs = %d, want exactly 1 and 1", real, dry)
	}
}

var keyEnter = tea.KeyMsg{Type: tea.KeyEnter}

func TestAutoSendDoesNotSurviveLeavingTheChat(t *testing.T) {
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"alice"}}}
	model := plainChat(t, client)
	model, previewCmd := uxPress(t, model, keyEnter)
	if !model.chatAutoSend {
		t.Fatal("a plain text Enter should arm the auto send")
	}
	// A stale preview from a chat that was left must not send.
	model.chatAutoSend = false
	model.chatReplyToken++
	updated, cmd := model.Update(previewCmd())
	if cmd != nil || updated.(Model).chatSendBusy() {
		t.Fatal("a stale preview reply sent a message")
	}
}
