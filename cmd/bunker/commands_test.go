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

// TestCmdReadDoesNotMarkByDefault covers issue #68: `bunker read <id>`
// is a pure read (backend.Read's markReceipt=false).
func TestCmdReadDoesNotMarkByDefault(t *testing.T) {
	backend := newFakeBackend()
	backend.items["whatsapp:wa:1"] = core.Item{ID: "whatsapp:wa:1", Body: "hola"}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read", "whatsapp:wa:1"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.readCalls) != 1 || backend.readCalls[0].MarkReceipt {
		t.Fatalf("readCalls = %+v, want 1 entry with MarkReceipt=false", backend.readCalls)
	}
}

// TestCmdReadMarkReadOptsIn covers --mark-read: the only way read marks
// the item read (WhatsApp/Matrix).
func TestCmdReadMarkReadOptsIn(t *testing.T) {
	backend := newFakeBackend()
	backend.items["whatsapp:wa:1"] = core.Item{ID: "whatsapp:wa:1", Body: "hola"}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read", "whatsapp:wa:1", "--mark-read"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.readCalls) != 1 || backend.readCalls[0].ID != "whatsapp:wa:1" || !backend.readCalls[0].MarkReceipt {
		t.Fatalf("readCalls = %+v, want 1 entry with MarkReceipt=true", backend.readCalls)
	}
}

// TestCmdReadNoReceiptIsDeprecatedNoOp: the old opt-out still parses and
// still does not mark, but cannot be combined with --mark-read.
func TestCmdReadNoReceiptIsDeprecatedNoOp(t *testing.T) {
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

	backend = newFakeBackend()
	stderr.Reset()
	code = runWithBackend(context.Background(), backend, []string{"read", "whatsapp:wa:1", "--no-receipt", "--mark-read"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 || len(backend.readCalls) != 0 {
		t.Fatalf("code = %d readCalls = %+v, want usage error and no read", code, backend.readCalls)
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

func TestCmdDownloadBuildsCall(t *testing.T) {
	backend := newFakeBackend()
	backend.downloadResult = core.DownloadResult{Path: "/tmp/out.bin", Bytes: 42, Name: "out.bin", MIME: "application/octet-stream"}
	dest := filepath.Join(t.TempDir(), "out.bin")

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"download", "mail:cl:1", "-n", "1", "-o", dest, "--force"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.downloadCalls) != 1 {
		t.Fatalf("downloadCalls = %+v, want 1 call", backend.downloadCalls)
	}
	call := backend.downloadCalls[0]
	if call.ID != "mail:cl:1" || call.Index != 1 || call.DestPath != dest || !call.Opts.Force {
		t.Fatalf("call = %+v, unexpected", call)
	}
	if !strings.Contains(stdout.String(), "out.bin") {
		t.Fatalf("stdout = %q, want it to mention the saved file", stdout.String())
	}
}

func TestCmdDownloadDefaultsIndexToZero(t *testing.T) {
	backend := newFakeBackend()
	dest := filepath.Join(t.TempDir(), "out.bin")

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"download", "mail:cl:1", "-o", dest}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.downloadCalls) != 1 || backend.downloadCalls[0].Index != 0 {
		t.Fatalf("downloadCalls = %+v, want index 0", backend.downloadCalls)
	}
	if backend.downloadCalls[0].Opts.Force {
		t.Fatalf("Force = true, want false by default")
	}
}

// TestCmdDownloadResolvesRelativePathAgainstCallerCwd guards the
// daemon-written design: the daemon runs in its own working directory,
// so a relative -o must be made absolute against the CLI's cwd before
// it crosses the RPC boundary, or the file lands next to the daemon.
func TestCmdDownloadResolvesRelativePathAgainstCallerCwd(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	backend := newFakeBackend()

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"download", "mail:cl:1", "-o", "manual.pdf"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.downloadCalls) != 1 {
		t.Fatalf("downloadCalls = %+v, want 1 call", backend.downloadCalls)
	}
	want := filepath.Join(dir, "manual.pdf")
	if got := backend.downloadCalls[0].DestPath; got != want {
		t.Fatalf("DestPath = %q, want %q (absolute, against the caller's cwd)", got, want)
	}
}

func TestCmdDownloadRequiresOutputPath(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"download", "mail:cl:1"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for missing -o", code)
	}
	if len(backend.downloadCalls) != 0 {
		t.Fatalf("downloadCalls = %+v, want 0", backend.downloadCalls)
	}
}

func TestCmdDownloadJSON(t *testing.T) {
	backend := newFakeBackend()
	backend.downloadResult = core.DownloadResult{Path: "/tmp/out.bin", Bytes: 7, Name: "out.bin", MIME: "text/plain"}
	dest := filepath.Join(t.TempDir(), "out.bin")

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"download", "mail:cl:1", "-o", dest, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		Result core.DownloadResult `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", stdout.String(), err)
	}
	if got.Result.Bytes != 7 || got.Result.Name != "out.bin" {
		t.Fatalf("Result = %+v", got.Result)
	}
}

func TestCmdDownloadErrorProducesJSONError(t *testing.T) {
	backend := newFakeBackend()
	backend.downloadErr = errTest
	dest := filepath.Join(t.TempDir(), "out.bin")

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"download", "mail:cl:1", "-o", dest, "--json"}, strings.NewReader(""), &stdout, &stderr)
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

func TestCmdAvatarBuildsCallAndPrintsPath(t *testing.T) {
	backend := newFakeBackend()
	backend.avatarResult = core.AvatarResult{Path: "/tmp/avatars/abc.png", Generated: true}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"avatar", "whatsapp", "personal", "5511@s.whatsapp.net"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.avatarCalls) != 1 {
		t.Fatalf("avatarCalls = %+v, want 1 call", backend.avatarCalls)
	}
	call := backend.avatarCalls[0]
	if call.Channel != core.ChannelWhatsApp || call.Account != "personal" || call.Thread != "5511@s.whatsapp.net" {
		t.Fatalf("call = %+v, unexpected", call)
	}
	if !strings.Contains(stdout.String(), "/tmp/avatars/abc.png") {
		t.Fatalf("stdout = %q, want it to mention the avatar path", stdout.String())
	}
}

func TestCmdAvatarRequiresThreePositionals(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"avatar", "whatsapp", "personal"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if len(backend.avatarCalls) != 0 {
		t.Fatalf("avatarCalls = %+v, want 0", backend.avatarCalls)
	}
}

func TestCmdAvatarJSON(t *testing.T) {
	backend := newFakeBackend()
	backend.avatarResult = core.AvatarResult{Path: "/tmp/avatars/abc.png", Generated: false}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"avatar", "matrix", "work", "!room:example.com", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		Result core.AvatarResult `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", stdout.String(), err)
	}
	if got.Result.Path != "/tmp/avatars/abc.png" || got.Result.Generated {
		t.Fatalf("Result = %+v", got.Result)
	}
}

func TestCmdAvatarErrorProducesJSONError(t *testing.T) {
	backend := newFakeBackend()
	backend.avatarErr = errTest

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"avatar", "whatsapp", "personal", "thread-1", "--json"}, strings.NewReader(""), &stdout, &stderr)
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

func TestCmdThreadBuildsCallAndPrintsItems(t *testing.T) {
	backend := newFakeBackend()
	backend.threadItems = []core.Item{
		{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Body: "hola"},
		{ID: "whatsapp:personal:2", Channel: core.ChannelWhatsApp, Account: "personal", Body: "buenas"},
	}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"thread", "whatsapp", "personal", "5511999999999@s.whatsapp.net"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.threadCalls) != 1 {
		t.Fatalf("threadCalls = %+v, want 1 call", backend.threadCalls)
	}
	call := backend.threadCalls[0]
	if call.Channel != "whatsapp" || call.Account != "personal" || call.Thread != "5511999999999@s.whatsapp.net" {
		t.Fatalf("call = %+v, unexpected", call)
	}
	if !call.Before.IsZero() {
		t.Fatalf("Before = %v, want zero (no --before given)", call.Before)
	}
	if call.Limit != 0 {
		t.Fatalf("Limit = %d, want 0 (no --limit given; the daemon applies the default)", call.Limit)
	}
	if !strings.Contains(stdout.String(), "hola") || !strings.Contains(stdout.String(), "buenas") {
		t.Fatalf("stdout = %q, want it to mention both items' bodies", stdout.String())
	}
}

func TestCmdThreadParsesBeforeAndLimit(t *testing.T) {
	backend := newFakeBackend()

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{
		"thread", "whatsapp", "personal", "5511999999999@s.whatsapp.net",
		"--before", "2026-09-01T12:00:00Z", "--limit", "10",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.threadCalls) != 1 {
		t.Fatalf("threadCalls = %+v, want 1 call", backend.threadCalls)
	}
	call := backend.threadCalls[0]
	wantBefore := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if !call.Before.Equal(wantBefore) {
		t.Fatalf("Before = %v, want %v", call.Before, wantBefore)
	}
	if call.Limit != 10 {
		t.Fatalf("Limit = %d, want 10", call.Limit)
	}
}

func TestCmdThreadRequiresThreePositionals(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"thread", "whatsapp", "personal"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if len(backend.threadCalls) != 0 {
		t.Fatalf("threadCalls = %+v, want 0", backend.threadCalls)
	}
}

func TestCmdThreadRejectsMalformedBefore(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"thread", "whatsapp", "personal", "t1", "--before", "not-a-time"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for a malformed --before", code)
	}
	if len(backend.threadCalls) != 0 {
		t.Fatalf("threadCalls = %+v, want 0", backend.threadCalls)
	}
}

func TestCmdThreadJSON(t *testing.T) {
	backend := newFakeBackend()
	backend.threadItems = []core.Item{{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp}}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"thread", "whatsapp", "personal", "t1", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		Items []core.Item `json:"items"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", stdout.String(), err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "whatsapp:personal:1" {
		t.Fatalf("Items = %+v", got.Items)
	}
}

func TestCmdThreadErrorProducesJSONError(t *testing.T) {
	backend := newFakeBackend()
	backend.threadErr = errTest

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"thread", "whatsapp", "personal", "t1", "--json"}, strings.NewReader(""), &stdout, &stderr)
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

// TestCmdReadThreadBuildsCallAndPrintsCount covers K9's CLI
// (conversation-view.md's read-thread fix): default receipt=true, and the
// count the daemon returns is printed.
func TestCmdReadThreadBuildsCallAndPrintsCount(t *testing.T) {
	backend := newFakeBackend()
	backend.readThreadCount = 3

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read-thread", "whatsapp", "personal", "5511999999999@s.whatsapp.net"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.readThreadCalls) != 1 {
		t.Fatalf("readThreadCalls = %+v, want 1 call", backend.readThreadCalls)
	}
	call := backend.readThreadCalls[0]
	if call.Channel != "whatsapp" || call.Account != "personal" || call.Thread != "5511999999999@s.whatsapp.net" || !call.Receipt {
		t.Fatalf("call = %+v, unexpected", call)
	}
	if !strings.Contains(stdout.String(), "3") {
		t.Fatalf("stdout = %q, want it to mention the count 3", stdout.String())
	}
}

// TestCmdReadThreadNoReceiptSetsReceiptFalse covers --no-receipt.
func TestCmdReadThreadNoReceiptSetsReceiptFalse(t *testing.T) {
	backend := newFakeBackend()

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read-thread", "whatsapp", "personal", "t1", "--no-receipt"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.readThreadCalls) != 1 || backend.readThreadCalls[0].Receipt {
		t.Fatalf("readThreadCalls = %+v, want one call with Receipt=false", backend.readThreadCalls)
	}
}

func TestCmdReadThreadRequiresThreePositionals(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read-thread", "whatsapp", "personal"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if len(backend.readThreadCalls) != 0 {
		t.Fatalf("readThreadCalls = %+v, want 0", backend.readThreadCalls)
	}
}

func TestCmdReadThreadJSON(t *testing.T) {
	backend := newFakeBackend()
	backend.readThreadCount = 2

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read-thread", "whatsapp", "personal", "t1", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", stdout.String(), err)
	}
	if got.Count != 2 {
		t.Fatalf("Count = %d, want 2", got.Count)
	}
}

func TestCmdReadThreadErrorProducesJSONError(t *testing.T) {
	backend := newFakeBackend()
	backend.readThreadErr = errTest

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"read-thread", "whatsapp", "personal", "t1", "--json"}, strings.NewReader(""), &stdout, &stderr)
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

// TestCmdSendJSONUsesSnakeCasePlanAndReceipt covers issue #68: the plan
// and receipt a --json send prints use snake_case keys, never the Go
// field names.
func TestCmdSendJSONUsesSnakeCasePlanAndReceipt(t *testing.T) {
	backend := newFakeBackend()
	backend.sendPlan = core.Plan{Action: "send", Channel: core.ChannelMail, Account: "cl", Target: "a@b.cl", Recipients: []string{"a@b.cl"}, Preview: "hola"}
	backend.receipt = core.Receipt{ID: "R1", Channel: core.ChannelMail}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"send", "mail", "cl", "a@b.cl", "hola", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		Plan    map[string]any `json:"plan"`
		Receipt map[string]any `json:"receipt"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout.String(), err)
	}
	if got.Plan["action"] != "send" || got.Plan["target"] != "a@b.cl" || got.Plan["preview"] != "hola" {
		t.Errorf("plan = %v, want snake_case action/target/preview", got.Plan)
	}
	if got.Receipt["id"] != "R1" || got.Receipt["channel"] != "mail" {
		t.Errorf("receipt = %v, want snake_case id/channel", got.Receipt)
	}
	for section, m := range map[string]map[string]any{"plan": got.Plan, "receipt": got.Receipt} {
		for key := range m {
			if key != strings.ToLower(key) {
				t.Errorf("%s key %q is not lower case", section, key)
			}
		}
	}
}
