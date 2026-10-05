package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/reyer3/bunker-go/internal/core"
)

// forwardClient is a contactsClient whose Download really writes the
// file, the way the daemon does, so a forwarded attachment exists on disk.
type forwardClient struct {
	contactsClient
	downloads   []string
	downloadErr error
}

func (c *forwardClient) Download(_ context.Context, id string, index int, destPath string, _ core.DownloadOptions) (core.DownloadResult, error) {
	c.downloads = append(c.downloads, destPath)
	if c.downloadErr != nil {
		return core.DownloadResult{}, c.downloadErr
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
		return core.DownloadResult{}, err
	}
	if err := os.WriteFile(destPath, []byte("contenido"), 0o600); err != nil {
		return core.DownloadResult{}, err
	}
	return core.DownloadResult{Path: destPath}, nil
}

func newForwardClient() *forwardClient {
	return &forwardClient{contactsClient: contactsClient{book: []core.Contact{
		{Channel: core.ChannelWhatsApp, Account: "personal", Name: "Ana Díaz", Address: "51922@s.whatsapp.net"},
		{Channel: core.ChannelWhatsApp, Account: "personal", Name: "Bruno", Address: "51933@s.whatsapp.net", Thread: "51933@s.whatsapp.net"},
	}}}
}

// collectMsgs runs cmd, unpacking batches, and returns every message.
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectMsgs(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// forwardTo presses Alt+F on the current selection, loads the picker's
// contacts and picks the first one. It returns the model and the
// command the pick produced.
func forwardTo(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	m, cmd := uxPress(t, m, keyAlt('f'))
	if m.picker == nil {
		t.Fatalf("Alt+F did not open the target picker:\n%s", m.View())
	}
	for _, msg := range collectMsgs(cmd) {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	return uxPress(t, m, tea.KeyMsg{Type: tea.KeyEnter})
}

// applyForwardMedia feeds the forward's attachment download result back.
func applyForwardMedia(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for _, msg := range collectMsgs(cmd) {
		if fm, ok := msg.(forwardMediaMsg); ok {
			updated, _ := m.Update(fm)
			return updated.(Model)
		}
	}
	t.Fatal("no attachment download for the forward")
	return m
}

func TestAltFOpensATargetPickerOnTheChatsChannel(t *testing.T) {
	client := newForwardClient()
	model, _ := uxChat(t, client)
	model, cmd := uxPress(t, model, keyAlt('f'))
	if model.picker == nil || !strings.Contains(model.picker.title, "Reenviar") {
		t.Fatalf("picker = %+v, want the forward picker", model.picker)
	}
	collectMsgs(cmd)
	if len(client.channels) != 1 || client.channels[0] != core.ChannelWhatsApp {
		t.Fatalf("contact search scope = %v, want whatsapp", client.channels)
	}
	if model.composer.Value() != "" {
		t.Fatalf("Alt+F typed into the composer: %q", model.composer.Value())
	}

	// Esc cancels: back in the same chat, nothing pending.
	model, _ = uxPress(t, model, escKey)
	if model.picker != nil || model.forwardPick != nil || !model.chatMode || model.chatForward != nil {
		t.Fatalf("Esc left forward state behind (picker=%v pick=%v chat=%v)", model.picker, model.forwardPick, model.chatMode)
	}
}

func TestForwardPrefillsTheComposerAndSendsAsAForward(t *testing.T) {
	client := newForwardClient()
	model, _ := uxChat(t, client)
	// Select "primer mensaje" (no attachment).
	for range 3 {
		model, _ = uxPress(t, model, altUp)
	}
	model, _ = forwardTo(t, model)

	if !model.chatMode || model.chatNewTo != "51922@s.whatsapp.net" {
		t.Fatalf("the target chat is not open (chat=%v to=%q)", model.chatMode, model.chatNewTo)
	}
	if got := model.composer.Value(); got != "primer mensaje" {
		t.Fatalf("composer = %q, want the forwarded text", got)
	}
	if !strings.Contains(model.View(), "↪ Reenviando") {
		t.Fatalf("no forward indicator:\n%s", model.View())
	}

	// The text can be edited before sending.
	model, _ = uxPress(t, model, runes("!"))
	client.outgoingPreview = core.Plan{Action: "send", Recipients: []string{"51922@s.whatsapp.net"}, Forward: true}
	model, cmd := uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not preview the forward")
	}
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	if len(client.outgoingCalls) != 1 {
		t.Fatalf("outgoing calls = %+v, want one dry run", client.outgoingCalls)
	}
	first := client.outgoingCalls[0]
	if !first.dryRun || !first.out.Forward || first.out.Body != "primer mensaje!" || len(first.out.To) != 1 || first.out.To[0] != "51922@s.whatsapp.net" {
		t.Fatalf("preview call = %+v, want a dry-run forward to the picked contact", first)
	}
	if len(client.calls) != 0 {
		t.Fatalf("a forward must not go out as a reply: %+v", client.calls)
	}
	// A forward always waits for the confirming Enter.
	if !model.chatConfirm || !strings.Contains(model.View(), "Reenviar") {
		t.Fatalf("no forward confirm:\n%s", model.View())
	}

	model, cmd = uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if len(client.outgoingCalls) != 2 || client.outgoingCalls[1].dryRun || !client.outgoingCalls[1].out.Forward {
		t.Fatalf("outgoing calls = %+v, want the real forward", client.outgoingCalls)
	}
	if model.chatForward != nil || strings.Contains(model.View(), "↪ Reenviando") {
		t.Fatal("the forward state outlived a successful send")
	}
}

func TestForwardCarriesTheAttachment(t *testing.T) {
	client := newForwardClient()
	model, _ := uxChat(t, client)
	// Select "mira el informe" (informe.pdf).
	for range 2 {
		model, _ = uxPress(t, model, altUp)
	}
	model, cmd := forwardTo(t, model)

	// Until the file is downloaded, Enter sends nothing.
	_, sendCmd := uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if sendCmd != nil || len(client.outgoingCalls) != 0 {
		t.Fatal("Enter sent the forward before its attachment was ready")
	}

	model = applyForwardMedia(t, model, cmd)
	if len(model.chatAttachments) != 1 || filepath.Base(model.chatAttachments[0]) != "informe.pdf" {
		t.Fatalf("attachments = %v, want the downloaded informe.pdf", model.chatAttachments)
	}
	if _, err := os.Stat(model.chatAttachments[0]); err != nil {
		t.Fatalf("forwarded file missing: %v", err)
	}

	model, cmd = uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not preview the forward")
	}
	cmd()
	out := client.outgoingCalls[0].out
	if !out.Forward || len(out.Attachments) != 1 || out.Attachments[0] != model.chatAttachments[0] {
		t.Fatalf("outgoing = %+v, want a forward carrying the file", out)
	}
}

func TestForwardAttachmentDownloadFailureIsLoud(t *testing.T) {
	client := newForwardClient()
	client.downloadErr = errors.New("sin conexión")
	model, _ := uxChat(t, client)
	for range 2 {
		model, _ = uxPress(t, model, altUp)
	}
	model, cmd := forwardTo(t, model)
	model = applyForwardMedia(t, model, cmd)
	if model.chatForward != nil || len(model.chatAttachments) != 0 || model.composer.Value() != "" {
		t.Fatalf("a failed download left a half forward (forward=%v attach=%v text=%q)", model.chatForward, model.chatAttachments, model.composer.Value())
	}
	if !strings.Contains(model.View(), "no se pudo descargar el adjunto para reenviar") {
		t.Fatalf("the failure is not shown:\n%s", model.View())
	}
}

func TestForwardVoiceNoteGoesAsAVoiceNote(t *testing.T) {
	client := newForwardClient()
	model, _ := uxChat(t, client)
	msg := chatThreadLoadedMsg{token: model.chatToken, items: []core.Item{
		{ID: "whatsapp:personal:v", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
			From: core.Address{Name: "Alice"}, Timestamp: uxNow,
			Attachments: []core.Attachment{{Name: "nota.ogg", MIME: "audio/ogg", Size: 400, Voice: true, Duration: 7}}},
	}}
	updated, _ := model.Update(msg)
	model = updated.(Model)
	model, cmd := forwardTo(t, model)
	model = applyForwardMedia(t, model, cmd)
	if !model.chatVoice || len(model.chatAttachments) != 1 {
		t.Fatalf("voice=%v attachments=%v, want one voice note", model.chatVoice, model.chatAttachments)
	}
}

func TestEscDropsTheForwardButStaysInTheChat(t *testing.T) {
	client := newForwardClient()
	model, _ := uxChat(t, client)
	for range 2 {
		model, _ = uxPress(t, model, altUp)
	}
	model, cmd := forwardTo(t, model)
	model = applyForwardMedia(t, model, cmd)

	model, _ = uxPress(t, model, escKey)
	if model.chatForward != nil || len(model.chatAttachments) != 0 || model.composer.Value() != "" {
		t.Fatalf("Esc kept the forward (forward=%v attach=%v text=%q)", model.chatForward, model.chatAttachments, model.composer.Value())
	}
	if !model.chatMode || model.chatNewTo != "51922@s.whatsapp.net" {
		t.Fatal("Esc on a forward should stay in the target chat")
	}
}

func TestHelpListsAltF(t *testing.T) {
	model, _ := openedChat(t)
	model.height = 0
	if help := model.openHelp().helpView(); !strings.Contains(help, "Alt+F") {
		t.Fatalf("full help does not list Alt+F:\n%s", help)
	}
	model.width = 400
	if !strings.Contains(strings.Join(model.chatTailLines(), "\n"), "Alt+F") {
		t.Fatal("the chat footer does not list Alt+F")
	}
}

func TestChatPaletteListsForward(t *testing.T) {
	model, _ := uxChat(t, newForwardClient())
	model = model.openPalette()
	if !hasLabel(model, "Reenviar mensaje") {
		t.Fatalf("the chat palette does not list the forward: %v", paletteLabels(model))
	}
}

// TestEscAtTheConfirmKeepsAForwardedVoiceNote: backing out of the confirm
// keeps the forward (a recorded voice note is dropped there, a forwarded
// one is the forward's content); a second Esc drops the forward.
func TestEscAtTheConfirmKeepsAForwardedVoiceNote(t *testing.T) {
	client := newForwardClient()
	model, _ := uxChat(t, client)
	updated, _ := model.Update(chatThreadLoadedMsg{token: model.chatToken, items: []core.Item{
		{ID: "whatsapp:personal:v", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
			From: core.Address{Name: "Alice"}, Timestamp: uxNow,
			Attachments: []core.Attachment{{Name: "nota.ogg", MIME: "audio/ogg", Size: 400, Voice: true, Duration: 7}}},
	}})
	model = updated.(Model)
	model, cmd := forwardTo(t, model)
	model = applyForwardMedia(t, model, cmd)
	client.outgoingPreview = core.Plan{Action: "send", Forward: true, Voice: true}
	model, cmd = uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if !model.chatConfirm {
		t.Fatal("no confirm for the forwarded voice note")
	}
	model, _ = uxPress(t, model, escKey)
	if model.chatConfirm || model.chatForward == nil || len(model.chatAttachments) != 1 || !model.chatVoice {
		t.Fatalf("Esc at the confirm lost the forward (forward=%v attach=%v voice=%v)", model.chatForward, model.chatAttachments, model.chatVoice)
	}
	model, _ = uxPress(t, model, escKey)
	if model.chatForward != nil || len(model.chatAttachments) != 0 {
		t.Fatal("a second Esc should drop the forward")
	}
}
