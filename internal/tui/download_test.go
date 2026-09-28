package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// fixedDownloadDir returns a Model override for downloadDefaultDirFn so
// tests never touch the real home directory (conversation-view.md's
// "~/Descargas falling back to ~/Downloads" default is exercised
// separately, as a pure function, in TestDefaultDownloadDirPrefersDescargas...).
func fixedDownloadDir(dir string) func() string {
	return func() string { return dir }
}

// TestChatDownloadKeyOnEmptyDraftOpensPathPromptForSingleAttachment pins
// K5's `d` key: with the draft empty and the newest loaded message
// carrying exactly one attachment, `d` opens the download flow straight
// to an editable destination path (no picker needed for a single
// attachment), defaulted under the injected download directory.
func TestChatDownloadKeyOnEmptyDraftOpensPathPromptForSingleAttachment(t *testing.T) {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.downloadDefaultDirFn = fixedDownloadDir("/home/alice/Descargas")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model.chatItems = []core.Item{
		{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", Body: "hola",
			Attachments: []core.Attachment{{Name: "photo.jpg", Size: 2048}}},
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("opening the download prompt must not itself launch a command")
	}
	if !model.downloadActive || model.downloadPicking {
		t.Fatalf("d on a single-attachment message must open the path prompt directly: active=%v picking=%v", model.downloadActive, model.downloadPicking)
	}
	if model.downloadPath != filepath.Join("/home/alice/Descargas", "photo.jpg") {
		t.Fatalf("downloadPath = %q, want the default suggested path", model.downloadPath)
	}
	if model.composer.Value() != "" {
		t.Fatalf("draft = %q, want it untouched", model.composer.Value())
	}
}

// TestChatDownloadKeyWithoutAttachmentsTypesLiteralD pins the fallback:
// when no loaded message carries an attachment, `d` is just a literal
// draft character, exactly like any other letter.
func TestChatDownloadKeyWithoutAttachmentsTypesLiteralD(t *testing.T) {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model.chatItems = []core.Item{{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", Body: "hola"}}

	model = typeRunes(model, "d")
	if model.downloadActive {
		t.Fatal("d must not open the download flow when nothing has an attachment")
	}
	if model.composer.Value() != "d" {
		t.Fatalf("draft = %q, want the literal letter", model.composer.Value())
	}
}

// TestChatDownloadKeyIgnoredOnceDraftHasText pins that `d` only reaches
// for an attachment while the draft is still empty; once the user has
// started typing, every key (including "d") is literal draft text again
// — otherwise a message like "de acuerdo" could never be typed.
func TestChatDownloadKeyIgnoredOnceDraftHasText(t *testing.T) {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model.chatItems = []core.Item{
		{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", Body: "hola",
			Attachments: []core.Attachment{{Name: "photo.jpg", Size: 2048}}},
	}

	model = typeRunes(model, "e")
	model = typeRunes(model, "d")
	if model.downloadActive {
		t.Fatal("d must stay literal once the draft already has text")
	}
	if model.composer.Value() != "ed" {
		t.Fatalf("draft = %q, want both letters kept as text", model.composer.Value())
	}
}

// TestChatDownloadKeyFindsNewestMessageWithAttachmentsAmongOlderOnes pins
// that "the message under focus" searches backward from the newest
// loaded message for the first one carrying an attachment, not just the
// literal last item (which may have none).
func TestChatDownloadKeyFindsNewestMessageWithAttachmentsAmongOlderOnes(t *testing.T) {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.downloadDefaultDirFn = fixedDownloadDir("/home/alice/Descargas")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model.chatItems = []core.Item{
		{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", Body: "con foto",
			Attachments: []core.Attachment{{Name: "old.jpg", Size: 10}}},
		{ID: "whatsapp:personal:2", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", Body: "sin adjunto"},
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	model = updated.(Model)
	if !model.downloadActive || model.downloadItemID != "whatsapp:personal:1" {
		t.Fatalf("want the older message with an attachment picked: active=%v itemID=%q", model.downloadActive, model.downloadItemID)
	}
}

// TestChatDownloadPickerForMultipleAttachmentsThenSelect pins the small
// picker for a message with more than one attachment: `d` opens the
// picker, and a digit key selects one, moving on to its path prompt.
func TestChatDownloadPickerForMultipleAttachmentsThenSelect(t *testing.T) {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.downloadDefaultDirFn = fixedDownloadDir("/home/alice/Descargas")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model.chatItems = []core.Item{
		{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", Body: "dos fotos",
			Attachments: []core.Attachment{{Name: "one.jpg", Size: 10}, {Name: "two.jpg", Size: 20}}},
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	model = updated.(Model)
	if !model.downloadActive || !model.downloadPicking {
		t.Fatalf("want the picker open for 2 attachments: active=%v picking=%v", model.downloadActive, model.downloadPicking)
	}
	view := model.View()
	if !strings.Contains(view, "one.jpg") || !strings.Contains(view, "two.jpg") {
		t.Fatalf("picker view = %q, want both attachment names listed", view)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	model = updated.(Model)
	if model.downloadPicking || model.downloadIndex != 1 {
		t.Fatalf("picking 2 must select index 1: picking=%v index=%d", model.downloadPicking, model.downloadIndex)
	}
	if model.downloadPath != filepath.Join("/home/alice/Descargas", "two.jpg") {
		t.Fatalf("downloadPath = %q, want the second attachment's suggested path", model.downloadPath)
	}
}

// TestMailThreadDownloadKeyOpensPathPromptForSelectedMessage pins K6's
// `d` key: it acts on the currently selected stacked message (there is a
// real selection cursor in the thread view, unlike chat's empty-draft
// heuristic).
func TestMailThreadDownloadKeyOpensPathPromptForSelectedMessage(t *testing.T) {
	client := &replyClient{}
	items := []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
		From: core.Address{ID: "bob@example.com", Name: "Bob"}, Subject: "hola", Body: "hi",
		Attachments: []core.Attachment{{Name: "plan.pdf", Size: 100}},
	}}
	model := threadReadyWithItems(client, "mail:cl:1", items)
	model.downloadDefaultDirFn = fixedDownloadDir("/home/alice/Descargas")

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	model = updated.(Model)
	if !model.downloadActive || model.downloadPicking {
		t.Fatalf("want the path prompt open directly for a single attachment: active=%v picking=%v", model.downloadActive, model.downloadPicking)
	}
	if model.downloadItemID != "mail:cl:1" {
		t.Fatalf("downloadItemID = %q, want the selected message's id", model.downloadItemID)
	}
	if model.downloadPath != filepath.Join("/home/alice/Descargas", "plan.pdf") {
		t.Fatalf("downloadPath = %q, want the default suggested path", model.downloadPath)
	}
	// The thread view's own navigation must survive underneath.
	if !model.threadMode || !model.detail {
		t.Fatal("the download overlay must not tear down the thread view under it")
	}
}

// TestMailThreadDownloadKeyWithoutAttachmentsDoesNothing pins that `d` on
// a selected message with no attachment is a no-op, not a crash or an
// empty download flow.
func TestMailThreadDownloadKeyWithoutAttachmentsDoesNothing(t *testing.T) {
	client := &replyClient{}
	items := []core.Item{{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{ID: "bob@example.com"}, Body: "hi"}}
	model := threadReadyWithItems(client, "mail:cl:1", items)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	model = updated.(Model)
	if cmd != nil || model.downloadActive {
		t.Fatalf("d without an attachment must do nothing: active=%v cmd=%v", model.downloadActive, cmd)
	}
}

// TestDownloadEscCancelsWithoutCallingDownload pins that leaving the
// download flow (Esc, from the path prompt) never calls Download and
// returns to the underlying view.
func TestDownloadEscCancelsWithoutCallingDownload(t *testing.T) {
	client := &replyClient{}
	items := []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{ID: "bob@example.com"}, Body: "hi",
		Attachments: []core.Attachment{{Name: "plan.pdf", Size: 100}},
	}}
	model := threadReadyWithItems(client, "mail:cl:1", items)
	model.downloadDefaultDirFn = fixedDownloadDir("/home/alice/Descargas")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	model = updated.(Model)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("Esc from the path prompt must not launch a command")
	}
	if model.downloadActive {
		t.Fatal("Esc must close the download flow")
	}
	if len(client.downloadCalls) != 0 {
		t.Fatalf("download calls = %+v, want none", client.downloadCalls)
	}
	if !model.threadMode {
		t.Fatal("Esc from the download flow must return to the thread view, not close it")
	}
}

// TestDownloadConfirmSendsAndShowsResult pins the happy path: confirming
// a destination that does not exist yet downloads immediately (no
// overwrite confirm needed) and the result (path + size) is shown.
func TestDownloadConfirmSendsAndShowsResult(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "plan.pdf")
	client := &replyClient{}
	client.downloadResult = core.DownloadResult{Path: dest, Bytes: 100, Name: "plan.pdf", MIME: "application/pdf"}
	items := []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{ID: "bob@example.com"}, Body: "hi",
		Attachments: []core.Attachment{{Name: "plan.pdf", Size: 100}},
	}}
	model := threadReadyWithItems(client, "mail:cl:1", items)
	model.downloadDefaultDirFn = fixedDownloadDir(dir)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	model = updated.(Model)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("confirming a fresh path must start the download")
	}
	if !model.downloadSending {
		t.Fatal("want downloadSending while the command is in flight")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	if len(client.downloadCalls) != 1 {
		t.Fatalf("download calls = %+v, want exactly 1", client.downloadCalls)
	}
	call := client.downloadCalls[0]
	if call.id != "mail:cl:1" || call.index != 0 || call.destPath != dest || call.opts.Force {
		t.Fatalf("download call = %+v, want id=mail:cl:1 index=0 destPath=%q force=false", call, dest)
	}
	// A long temp path can wrap across lines at this model's width; undo
	// that purely visual wrap (wrapView only ever inserts a bare "\n", it
	// never drops or adds other characters) before checking the path is
	// there in full.
	unwrapped := strings.ReplaceAll(model.View(), "\n", "")
	if !strings.Contains(unwrapped, dest) || !strings.Contains(unwrapped, "100") {
		t.Fatalf("view = %q, want the saved path and size", model.View())
	}
}

// TestDownloadExistingFileAsksOverwriteBeforeForcing is the no-overwrite-
// without-confirm guard: confirming a path that already exists on disk
// must show "¿Sobrescribir?" and NOT call Download; only a second Enter
// (the explicit confirm) calls Download with Force=true. Esc on that
// confirm must return to editing the path without ever calling Download.
func TestDownloadExistingFileAsksOverwriteBeforeForcing(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "plan.pdf")
	if err := os.WriteFile(dest, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &replyClient{}
	client.downloadResult = core.DownloadResult{Path: dest, Bytes: 100}
	items := []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{ID: "bob@example.com"}, Body: "hi",
		Attachments: []core.Attachment{{Name: "plan.pdf", Size: 100}},
	}}
	model := threadReadyWithItems(client, "mail:cl:1", items)
	model.downloadDefaultDirFn = fixedDownloadDir(dir)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	model = updated.(Model)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("confirming a path that already exists must not download yet")
	}
	if !model.downloadOverwrite {
		t.Fatal("want the overwrite confirm showing")
	}
	if len(client.downloadCalls) != 0 {
		t.Fatalf("download calls = %+v, want none before the overwrite confirm", client.downloadCalls)
	}
	if !strings.Contains(model.View(), "Sobrescribir") {
		t.Fatalf("view = %q, want the overwrite confirm text", model.View())
	}

	// Esc backs out to editing the path, no call made.
	updated, escCmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if escCmd != nil || model.downloadOverwrite || !model.downloadActive {
		t.Fatalf("esc on overwrite confirm must return to editing: overwrite=%v active=%v cmd=%v", model.downloadOverwrite, model.downloadActive, escCmd)
	}
	if len(client.downloadCalls) != 0 {
		t.Fatalf("download calls = %+v, want none after esc", client.downloadCalls)
	}

	// Re-confirm and this time accept the overwrite.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("confirming the overwrite must start the download")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if len(client.downloadCalls) != 1 || !client.downloadCalls[0].opts.Force {
		t.Fatalf("download calls = %+v, want exactly 1 with Force=true", client.downloadCalls)
	}
}

// TestDownloadErrorIsShownAndDoesNotCloseTheFlow pins that a Download
// error (e.g. the daemon's size cap or a missing media key) is shown
// clearly and lets the user retry rather than silently closing.
func TestDownloadErrorIsShownAndDoesNotCloseTheFlow(t *testing.T) {
	dir := t.TempDir()
	client := &replyClient{}
	client.downloadErr = errAttachmentTooLargeForTest
	items := []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{ID: "bob@example.com"}, Body: "hi",
		Attachments: []core.Attachment{{Name: "plan.pdf", Size: 100}},
	}}
	model := threadReadyWithItems(client, "mail:cl:1", items)
	model.downloadDefaultDirFn = fixedDownloadDir(dir)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	if model.downloadSending {
		t.Fatal("a returned error must clear downloadSending")
	}
	if !strings.Contains(model.View(), errAttachmentTooLargeForTest.Error()) {
		t.Fatalf("view = %q, want the error shown", model.View())
	}
	if !model.downloadActive {
		t.Fatal("an error must keep the flow open for a retry, not close it")
	}
}

var errAttachmentTooLargeForTest = &downloadTestError{"104857601 bytes exceeds the 104857600 byte cap"}

type downloadTestError struct{ msg string }

func (e *downloadTestError) Error() string { return e.msg }

// TestSanitizeAttachmentFilenameRejectsPathEscape is the path-escape
// mutation-checked guard: a hostile attachment name (attacker-controlled
// message content) must never be able to escape the destination
// directory via a path separator, "..", or an embedded NUL.
func TestSanitizeAttachmentFilenameRejectsPathEscape(t *testing.T) {
	cases := map[string]string{
		"photo.jpg":       "photo.jpg",
		"../../etc/hosts": "hosts",
		"a/b/c.txt":       "c.txt",
		`a\b\c.txt`:       "c.txt",
		"..":              "attachment",
		"/":               "attachment",
		"":                "attachment",
		"evil\x00.jpg":    "evil.jpg",
	}
	for input, want := range cases {
		got := sanitizeAttachmentFilename(input)
		if got != want {
			t.Fatalf("sanitizeAttachmentFilename(%q) = %q, want %q", input, got, want)
		}
		if strings.ContainsAny(got, `/\`) || strings.Contains(got, "..") || strings.Contains(got, "\x00") {
			t.Fatalf("sanitizeAttachmentFilename(%q) = %q, still escapes the directory", input, got)
		}
	}
}

// TestDefaultDownloadDirPrefersDescargasThenFallsBackToDownloads pins the
// destination-directory default rule, as a pure function so it never
// touches the real filesystem or home directory.
func TestDefaultDownloadDirPrefersDescargasThenFallsBackToDownloads(t *testing.T) {
	if got := downloadDirFor("/home/alice", true); got != filepath.Join("/home/alice", "Descargas") {
		t.Fatalf("downloadDirFor(existing Descargas) = %q, want Descargas", got)
	}
	if got := downloadDirFor("/home/alice", false); got != filepath.Join("/home/alice", "Downloads") {
		t.Fatalf("downloadDirFor(no Descargas) = %q, want the Downloads fallback", got)
	}
}

// TestChatDownloadRoundTripsThroughRealTeaUpdate is a light end-to-end
// smoke test using time.Now-free wiring, confirming the whole flow is a
// non-blocking tea.Cmd (never a direct blocking call inside Update).
func TestChatDownloadRoundTripsThroughRealTeaUpdate(t *testing.T) {
	dir := t.TempDir()
	client := &replyClient{}
	client.downloadResult = core.DownloadResult{Path: filepath.Join(dir, "photo.jpg"), Bytes: 2048}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.downloadDefaultDirFn = fixedDownloadDir(dir)
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model.chatItems = []core.Item{{
		ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", Body: "hola",
		Timestamp:   time.Now(),
		Attachments: []core.Attachment{{Name: "photo.jpg", Size: 2048}},
	}}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	model = updated.(Model)
	updated, downloadCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if downloadCmd == nil {
		t.Fatal("Enter on a fresh path must return a command, never block inline")
	}
	msg := downloadCmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	if model.downloadResult.Path != filepath.Join(dir, "photo.jpg") {
		t.Fatalf("downloadResult = %+v, want the fake's result", model.downloadResult)
	}
}

// TestChatTypingDStartsADraftAndCtrlDDownloads: in the chat view the
// composer always has focus, so a plain "d" must type (a message like
// "de acuerdo" starts with it); download lives on Ctrl+D instead.
func TestChatTypingDStartsADraftAndCtrlDDownloads(t *testing.T) {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)
	model.chatItems = []core.Item{
		{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", Body: "hola",
			Attachments: []core.Attachment{{Name: "photo.jpg", Size: 2048}}},
	}

	model = typeRunes(model, "d")
	if model.downloadActive {
		t.Fatal("a plain d on an empty draft opened a download; it must type")
	}
	if model.composer.Value() != "d" {
		t.Fatalf("draft = %q, want \"d\"", model.composer.Value())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if !updated.(Model).downloadActive {
		t.Fatal("Ctrl+D did not open the download for the newest attachment")
	}
}
