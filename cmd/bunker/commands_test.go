package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// pngBytes is the minimal 8-byte PNG signature: enough for
// http.DetectContentType to report "image/png" without a full valid PNG.
var pngBytes = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// isZeroReceipt reports whether r carries no result at all — the zero
// Receipt a dry-run must return. Receipt now carries a Recipients slice
// (T13a), so plain struct comparison (r != core.Receipt{}) no longer
// compiles.
func isZeroReceipt(r core.Receipt) bool {
	return r.ID == "" && r.Channel == "" && r.At.IsZero() && len(r.Recipients) == 0
}

func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

func TestCmdListJSON(t *testing.T) {
	backend := newFakeBackend()
	backend.items["mail:cl:1"] = core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Subject: "hi"}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"list", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		Items []core.Item `json:"items"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", stdout.String(), err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "mail:cl:1" {
		t.Fatalf("items = %+v", got.Items)
	}
}

func TestCmdListErrorProducesJSONError(t *testing.T) {
	backend := newFakeBackend()
	backend.listErr = errTest
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"list", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit code on backend error")
	}
	var got struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", stdout.String(), err)
	}
	if got.Error == "" {
		t.Fatal("expected a non-empty error field")
	}
}

func TestCmdReadJSON(t *testing.T) {
	backend := newFakeBackend()
	backend.items["mail:cl:1"] = core.Item{ID: "mail:cl:1", Body: "the body"}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read", "mail:cl:1", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "the body") {
		t.Fatalf("stdout = %q, want it to contain the body", stdout.String())
	}
}

// TestCmdReadMarksReceiptByDefault covers T13(c): `bunker read <id>`
// marks the item read (backend.Read's markReceipt=true) unless told
// otherwise.
func TestCmdReadMarksReceiptByDefault(t *testing.T) {
	backend := newFakeBackend()
	backend.items["whatsapp:wa:1"] = core.Item{ID: "whatsapp:wa:1", Body: "hola"}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read", "whatsapp:wa:1"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.readCalls) != 1 {
		t.Fatalf("readCalls = %+v, want 1", backend.readCalls)
	}
	if backend.readCalls[0].ID != "whatsapp:wa:1" || !backend.readCalls[0].MarkReceipt {
		t.Fatalf("readCalls[0] = %+v, want ID=whatsapp:wa:1 MarkReceipt=true", backend.readCalls[0])
	}
}

// TestCmdReadNoReceiptSkipsMarking covers --no-receipt: fetch without
// marking read (WhatsApp/Matrix).
func TestCmdReadNoReceiptSkipsMarking(t *testing.T) {
	backend := newFakeBackend()
	backend.items["whatsapp:wa:1"] = core.Item{ID: "whatsapp:wa:1", Body: "hola"}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read", "whatsapp:wa:1", "--no-receipt"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.readCalls) != 1 || backend.readCalls[0].MarkReceipt {
		t.Fatalf("readCalls = %+v, want 1 entry with MarkReceipt=false", backend.readCalls)
	}
}

func TestCmdReplyDryRunPassesFlagThrough(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"reply", "mail:cl:1", "hola", "--dry-run", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.replyCalls) != 1 {
		t.Fatalf("replyCalls = %+v, want 1", backend.replyCalls)
	}
	call := backend.replyCalls[0]
	if !call.DryRun || call.Body != "hola" || call.ID != "mail:cl:1" {
		t.Fatalf("unexpected reply call: %+v", call)
	}
	var got struct {
		DryRun  bool         `json:"dryRun"`
		Plan    core.Plan    `json:"plan"`
		Receipt core.Receipt `json:"receipt"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.DryRun || !isZeroReceipt(got.Receipt) {
		t.Fatalf("expected dry-run output with empty receipt, got %+v", got)
	}
}

func TestCmdReplyReadsBodyFromStdin(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"reply", "mail:cl:1", "-"}, strings.NewReader("from stdin\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.replyCalls) != 1 || backend.replyCalls[0].Body != "from stdin" {
		t.Fatalf("replyCalls = %+v", backend.replyCalls)
	}
}

// TestCmdReplyWithAttachBuildsAttachments covers T11b(2)/(5): --attach is
// repeatable on reply, for every channel, not just WhatsApp.
func TestCmdReplyWithAttachBuildsAttachments(t *testing.T) {
	backend := newFakeBackend()
	path1 := writeTempFile(t, "one.png", pngBytes)
	path2 := writeTempFile(t, "two.png", pngBytes)

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"reply", "mail:cl:1", "mira", "--attach", path1, "--attach", path2},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.replyCalls) != 1 {
		t.Fatalf("replyCalls = %+v, want 1", backend.replyCalls)
	}
	got := backend.replyCalls[0].Attachments
	if len(got) != 2 || got[0] != path1 || got[1] != path2 {
		t.Fatalf("Attachments = %+v, want [%s %s]", got, path1, path2)
	}
}

func TestCmdReplyAttachFileMustExist(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"reply", "mail:cl:1", "hola", "--attach", "/no/such/file.png", "--json"},
		strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for a missing attachment")
	}
	if len(backend.replyCalls) != 0 {
		t.Fatalf("backend.Reply was called with an invalid attachment: %+v", backend.replyCalls)
	}
}

// TestCmdReplyWithCcBuildsCc covers T12(b): --cc is repeatable on reply,
// each value may also be comma-separated.
func TestCmdReplyWithCcBuildsCc(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"reply", "mail:cl:1", "hola", "--cc", "cc1@x.cl", "--cc", "cc2@x.cl, cc3@x.cl"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.replyCalls) != 1 {
		t.Fatalf("replyCalls = %+v, want 1", backend.replyCalls)
	}
	got := backend.replyCalls[0].Cc
	want := []string{"cc1@x.cl", "cc2@x.cl", "cc3@x.cl"}
	if len(got) != len(want) {
		t.Fatalf("Cc = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Cc = %+v, want %+v", got, want)
		}
	}
}

func TestCmdSendBuildsOutgoing(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"send", "whatsapp", "personal", "5511999", "hola", "--subject", "ignored"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.sendCalls) != 1 {
		t.Fatalf("sendCalls = %+v", backend.sendCalls)
	}
	out := backend.sendCalls[0]
	if out.Channel != core.ChannelWhatsApp || out.Account != "personal" || out.To[0] != "5511999" || out.Body != "hola" {
		t.Fatalf("unexpected outgoing: %+v", out)
	}
}

// TestCmdSendAcceptsCommaSeparatedTo covers T12(a): <to> is a
// comma-separated list, trimmed and with empties dropped.
func TestCmdSendAcceptsCommaSeparatedTo(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "mail", "cl", "a@b.cl, c@d.cl ,, e@f.cl", "hola"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.sendCalls) != 1 {
		t.Fatalf("sendCalls = %+v, want 1", backend.sendCalls)
	}
	got := backend.sendCalls[0].To
	want := []string{"a@b.cl", "c@d.cl", "e@f.cl"}
	if len(got) != len(want) {
		t.Fatalf("To = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("To = %+v, want %+v", got, want)
		}
	}
}

// TestCmdSendRejectsAllEmptyRecipients covers T12(a): a <to> that is only
// commas/spaces must be rejected, never sent as an empty To.
func TestCmdSendRejectsAllEmptyRecipients(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "mail", "cl", " , ,", "hola", "--json"},
		strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for an empty recipient list")
	}
	if len(backend.sendCalls) != 0 {
		t.Fatalf("backend.Send was called with no recipients: %+v", backend.sendCalls)
	}
}

// TestCmdSendWithCcBuildsCc covers T12(b): --cc is repeatable, each value
// may also be comma-separated.
func TestCmdSendWithCcBuildsCc(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "mail", "cl", "a@b.cl", "hola", "--cc", "cc1@x.cl", "--cc", "cc2@x.cl,cc3@x.cl"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.sendCalls) != 1 {
		t.Fatalf("sendCalls = %+v, want 1", backend.sendCalls)
	}
	got := backend.sendCalls[0].Cc
	want := []string{"cc1@x.cl", "cc2@x.cl", "cc3@x.cl"}
	if len(got) != len(want) {
		t.Fatalf("Cc = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Cc = %+v, want %+v", got, want)
		}
	}
}

func TestCmdSendWithMediaBuildsAttachmentsInOrder(t *testing.T) {
	backend := newFakeBackend()
	path1 := writeTempFile(t, "one.png", pngBytes)
	path2 := writeTempFile(t, "two.png", pngBytes)

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "personal", "5511999", "mira", "--media", path1, "--media", path2},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.sendCalls) != 1 {
		t.Fatalf("sendCalls = %+v", backend.sendCalls)
	}
	out := backend.sendCalls[0]
	if len(out.Attachments) != 2 || out.Attachments[0] != path1 || out.Attachments[1] != path2 {
		t.Fatalf("Attachments = %+v, want [%s %s]", out.Attachments, path1, path2)
	}
}

// TestCmdSendWithAttachBuildsAttachmentsInOrder covers T11b(2): --attach
// is the new, channel-agnostic flag name; it behaves exactly like --media
// did.
func TestCmdSendWithAttachBuildsAttachmentsInOrder(t *testing.T) {
	backend := newFakeBackend()
	path1 := writeTempFile(t, "one.png", pngBytes)
	path2 := writeTempFile(t, "two.png", pngBytes)

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "personal", "5511999", "mira", "--attach", path1, "--attach", path2},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	out := backend.sendCalls[0]
	if len(out.Attachments) != 2 || out.Attachments[0] != path1 || out.Attachments[1] != path2 {
		t.Fatalf("Attachments = %+v, want [%s %s]", out.Attachments, path1, path2)
	}
}

// TestCmdSendMediaAndAttachCanBeCombined proves --media still works
// exactly as before (the scheduled send depends on it) and that both
// flags may be given together.
func TestCmdSendMediaAndAttachCanBeCombined(t *testing.T) {
	backend := newFakeBackend()
	mediaPath := writeTempFile(t, "media.png", pngBytes)
	attachPath := writeTempFile(t, "attach.png", pngBytes)

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "personal", "5511999", "mira", "--media", mediaPath, "--attach", attachPath},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	out := backend.sendCalls[0]
	if len(out.Attachments) != 2 || out.Attachments[0] != mediaPath || out.Attachments[1] != attachPath {
		t.Fatalf("Attachments = %+v, want [%s %s]", out.Attachments, mediaPath, attachPath)
	}
}

// TestPrintPlanResultListsAttachmentsInHumanOutput covers T11b(4): the
// human (non-JSON) send/reply output lists each attachment as
// "name (mime, size)".
func TestPrintPlanResultListsAttachmentsInHumanOutput(t *testing.T) {
	plan := core.Plan{
		Action: "send", Channel: core.ChannelWhatsApp, Account: "personal",
		Attachments: []core.AttachmentInfo{{Name: "pic.png", MIME: "image/png", Size: 1234}},
	}
	var stdout bytes.Buffer
	printPlanResult(false, true, plan, core.Receipt{}, &stdout)
	if !strings.Contains(stdout.String(), "pic.png (image/png, 1234 bytes)") {
		t.Fatalf("stdout = %q, want it to list the attachment as name (mime, size)", stdout.String())
	}
}

// TestPrintPlanResultShowsCcInHumanOutput covers T12(e): Cc recipients
// must be visible in the human (non-JSON) output, on dry-run and on a
// real send/reply.
func TestPrintPlanResultShowsCcInHumanOutput(t *testing.T) {
	plan := core.Plan{
		Action: "send", Channel: core.ChannelMail, Account: "cl",
		Target: "[a@b.cl c@d.cl]", Cc: []string{"e@f.cl"}, Preview: "hi",
	}

	var dryStdout bytes.Buffer
	printPlanResult(false, true, plan, core.Receipt{}, &dryStdout)
	if !strings.Contains(dryStdout.String(), "cc [e@f.cl]") {
		t.Fatalf("dry-run stdout = %q, want it to show cc [e@f.cl]", dryStdout.String())
	}

	var realStdout bytes.Buffer
	printPlanResult(false, false, plan, core.Receipt{ID: "r1"}, &realStdout)
	if !strings.Contains(realStdout.String(), "cc [e@f.cl]") {
		t.Fatalf("real-run stdout = %q, want it to show cc [e@f.cl]", realStdout.String())
	}
}

// TestPrintPlanResultDryRunShowsToRecipients covers T13(g): the dry-run
// line omitted To recipients entirely (it showed only cc). It must show
// both "to [...]" and "cc [...]".
func TestPrintPlanResultDryRunShowsToRecipients(t *testing.T) {
	plan := core.Plan{
		Action: "send", Channel: core.ChannelWhatsApp, Account: "wa",
		Recipients: []string{"+51999999999"}, Preview: "hola",
	}
	var stdout bytes.Buffer
	printPlanResult(false, true, plan, core.Receipt{}, &stdout)
	if !strings.Contains(stdout.String(), "to [+51999999999]") {
		t.Fatalf("dry-run stdout = %q, want it to show to [+51999999999]", stdout.String())
	}
}

// TestPrintPlanResultShowsSubjectInHumanOutput covers the Plan.Subject
// addendum: the human dry-run line shows the subject when set, and omits
// it entirely when empty (WhatsApp/Matrix).
func TestPrintPlanResultShowsSubjectInHumanOutput(t *testing.T) {
	withSubject := core.Plan{Action: "reply", Channel: core.ChannelMail, Account: "cl", Subject: "Re: hi", Preview: "ack"}
	var stdout bytes.Buffer
	printPlanResult(false, true, withSubject, core.Receipt{}, &stdout)
	if !strings.Contains(stdout.String(), `subject "Re: hi"`) {
		t.Fatalf("stdout = %q, want it to show subject %q", stdout.String(), "Re: hi")
	}

	noSubject := core.Plan{Action: "send", Channel: core.ChannelWhatsApp, Account: "wa", Preview: "hola"}
	var stdout2 bytes.Buffer
	printPlanResult(false, true, noSubject, core.Receipt{}, &stdout2)
	if strings.Contains(stdout2.String(), "subject") {
		t.Fatalf("stdout = %q, want no subject clause when Plan.Subject is empty", stdout2.String())
	}
}

// TestPrintFanoutResultDryRunShowsEveryRecipientAndEstimatedPause covers
// T13(a): a fan-out dry-run shows the full per-recipient plan and the
// estimated pause budget, sending/sleeping nothing.
func TestPrintFanoutResultDryRunShowsEveryRecipientAndEstimatedPause(t *testing.T) {
	plan := core.Plan{
		Action: "send", Channel: core.ChannelWhatsApp, Account: "wa",
		Recipients:     []string{"+51111", "+51222", "+51333"},
		Preview:        "hola",
		FanoutPauseMin: 6 * time.Second,
		FanoutPauseMax: 16 * time.Second,
	}
	var stdout bytes.Buffer
	code := printPlanResult(false, true, plan, core.Receipt{}, &stdout)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 for a dry-run", code)
	}
	out := stdout.String()
	for _, want := range []string{"+51111", "+51222", "+51333", "6s", "16s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want it to contain %q", out, want)
		}
	}
}

// TestPrintFanoutResultRealShowsPerRecipientChecksAndExitsNonZero covers
// T13(a): a real fan-out send lists each recipient's outcome and exits
// non-zero when any recipient failed, so a partial failure never looks
// like a clean success.
func TestPrintFanoutResultRealShowsPerRecipientChecksAndExitsNonZero(t *testing.T) {
	plan := core.Plan{Action: "send", Channel: core.ChannelWhatsApp, Account: "wa", Recipients: []string{"+51111", "+51222"}}
	receipt := core.Receipt{
		ID: "r-+51111", Channel: core.ChannelWhatsApp,
		Recipients: []core.RecipientResult{
			{To: "+51111", Receipt: core.Receipt{ID: "r-+51111"}},
			{To: "+51222", Error: "not on whatsapp"},
		},
	}
	var stdout bytes.Buffer
	code := printPlanResult(false, false, plan, receipt, &stdout)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 when a recipient failed", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "+51111") || !strings.Contains(out, "+51222") || !strings.Contains(out, "not on whatsapp") {
		t.Fatalf("stdout = %q, want it to list both recipients and the failure reason", out)
	}
}

// TestPrintFanoutResultRealAllSucceedExitsZero is the success-path
// counterpart: no failures means exit code 0.
func TestPrintFanoutResultRealAllSucceedExitsZero(t *testing.T) {
	plan := core.Plan{Action: "send", Channel: core.ChannelWhatsApp, Account: "wa", Recipients: []string{"+51111", "+51222"}}
	receipt := core.Receipt{
		ID: "r-+51111", Channel: core.ChannelWhatsApp,
		Recipients: []core.RecipientResult{
			{To: "+51111", Receipt: core.Receipt{ID: "r-+51111"}},
			{To: "+51222", Receipt: core.Receipt{ID: "r-+51222"}},
		},
	}
	var stdout bytes.Buffer
	code := printPlanResult(false, false, plan, receipt, &stdout)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 when every recipient succeeded", code)
	}
}

// TestCmdSendFanoutExitsNonZeroWhenAnyRecipientFails covers T13(a) end to
// end through cmdSend: the CLI process exit code reflects a partial
// fan-out failure, not the (nil) top-level backend.Send error.
func TestCmdSendFanoutExitsNonZeroWhenAnyRecipientFails(t *testing.T) {
	backend := newFakeBackend()
	backend.sendPlan = core.Plan{Action: "send", Channel: core.ChannelWhatsApp, Account: "personal", Recipients: []string{"+51111", "+51222"}}
	backend.receipt = core.Receipt{
		ID: "r-1", Recipients: []core.RecipientResult{
			{To: "+51111", Receipt: core.Receipt{ID: "r-1"}},
			{To: "+51222", Error: "boom"},
		},
	}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "personal", "+51111,+51222", "hola"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, stdout=%s stderr=%s, want 1", code, stdout.String(), stderr.String())
	}
}

func TestCmdSendAttachmentMustNotBeADirectory(t *testing.T) {
	backend := newFakeBackend()
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "personal", "5511999", "hola", "--attach", dir},
		strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for a directory attachment")
	}
	if len(backend.sendCalls) != 0 {
		t.Fatalf("backend.Send was called with a directory attachment: %+v", backend.sendCalls)
	}
}

func TestCmdSendWithoutMediaLeavesAttachmentsEmpty(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "personal", "5511999", "hola"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.sendCalls[0].Attachments) != 0 {
		t.Fatalf("Attachments = %+v, want none", backend.sendCalls[0].Attachments)
	}
}

func TestCmdSendMediaFileMustExist(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "personal", "5511999", "hola", "--media", "/no/such/file.png", "--json"},
		strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for a missing media file")
	}
	if len(backend.sendCalls) != 0 {
		t.Fatalf("backend.Send was called with an invalid media file: %+v", backend.sendCalls)
	}
	var got struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", stdout.String(), err)
	}
	if got.Error == "" {
		t.Fatal("expected a non-empty error field")
	}
}

// TestCmdSendAcceptsAnyReadableFileTypeCheckMovedToCore covers T11b(1):
// the CLI only checks that a --media/--attach path exists and is a
// readable regular file. The MIME/size policy that used to live here
// (imageMediaTypes, validateMediaFiles) now lives in core.Service,
// validated against the adapter's own core.AttachmentPolicy — see
// TestServiceSendAttachmentPolicyRejectsUnsupportedType in
// internal/core/service_test.go. This fake backend does not replicate
// that check, so a non-image file reaches it unmodified.
func TestCmdSendAcceptsAnyReadableFileTypeCheckMovedToCore(t *testing.T) {
	backend := newFakeBackend()
	path := writeTempFile(t, "notes.txt", []byte("plain text, not an image"))

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "personal", "5511999", "hola", "--media", path},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.sendCalls) != 1 || backend.sendCalls[0].Attachments[0] != path {
		t.Fatalf("sendCalls = %+v, want the non-image file passed through to the backend", backend.sendCalls)
	}
}

func TestCmdSendMediaDryRunStillValidatesFile(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "personal", "5511999", "hola", "--media", "/no/such/file.png", "--dry-run"},
		strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for a missing media file even on --dry-run")
	}
	if len(backend.sendCalls) != 0 {
		t.Fatalf("backend.Send was called on an invalid dry-run: %+v", backend.sendCalls)
	}
}

func TestCmdOrganizeBuildsOp(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"organize", "mail:cl:1", "--label", "vip", "--label", "urgent", "--unlabel", "inbox", "--move", "Archive", "--seen"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.organizeCalls) != 1 {
		t.Fatalf("organizeCalls = %+v", backend.organizeCalls)
	}
	op := backend.organizeCalls[0].Op
	if len(op.AddLabels) != 2 || op.AddLabels[0] != "vip" || op.AddLabels[1] != "urgent" {
		t.Fatalf("AddLabels = %+v", op.AddLabels)
	}
	if len(op.RemoveLabels) != 1 || op.RemoveLabels[0] != "inbox" {
		t.Fatalf("RemoveLabels = %+v", op.RemoveLabels)
	}
	if op.MoveTo != "Archive" {
		t.Fatalf("MoveTo = %q", op.MoveTo)
	}
	if op.Seen == nil || !*op.Seen {
		t.Fatalf("Seen = %v, want true", op.Seen)
	}
}

func TestCmdOrganizeSeenAndUnseenAreMutuallyExclusive(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"organize", "mail:cl:1", "--seen", "--unseen"}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for --seen and --unseen together")
	}
}

func TestCmdStatusPost(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"status", "post", "whatsapp", "personal", "buenos dias", "--dry-run"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.statusCalls) != 1 || backend.statusCalls[0].Status.Text != "buenos dias" || !backend.statusCalls[0].DryRun {
		t.Fatalf("statusCalls = %+v", backend.statusCalls)
	}
}

func TestCmdCountsJSON(t *testing.T) {
	backend := newFakeBackend()
	backend.counts = map[core.Channel]map[string]int{core.ChannelMail: {"cl": 3}}
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"counts", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		Counts map[core.Channel]map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Counts[core.ChannelMail]["cl"] != 3 {
		t.Fatalf("counts = %+v", got.Counts)
	}
}

func TestUnknownCommandReturnsUsageExitCode(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"bogus"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

var errTest = &testError{"boom"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
