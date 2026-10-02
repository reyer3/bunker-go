package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/channel/fake"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/oggfixture"
	"github.com/reyer3/bunker-go/internal/store"
)

func TestCmdSendVoiceNeedsNoText(t *testing.T) {
	backend := newFakeBackend()
	path := writeTempFile(t, "nota.ogg", oggfixture.Bytes(time.Second))
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "personal", "5511999", "--voice", path, "--dry-run"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.sendCalls) != 1 {
		t.Fatalf("sendCalls = %+v", backend.sendCalls)
	}
	out := backend.sendCalls[0]
	if !out.Voice || len(out.Attachments) != 1 || out.Attachments[0] != path || out.Body != "" {
		t.Fatalf("Outgoing = %+v, want one voice attachment and no text", out)
	}
	if !backend.sendDryRuns[0] {
		t.Error("--dry-run was not passed through")
	}
}

func TestCmdReplyVoiceMarksContext(t *testing.T) {
	backend := newFakeBackend()
	path := writeTempFile(t, "nota.ogg", oggfixture.Bytes(time.Second))
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"reply", "whatsapp:personal:1", "--voice", path, "--dry-run"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.replyCalls) != 1 || !backend.replyVoice[0] {
		t.Fatalf("replyCalls=%+v voice=%v, want one voice reply", backend.replyCalls, backend.replyVoice)
	}
	if got := backend.replyCalls[0]; len(got.Attachments) != 1 || got.Attachments[0] != path || got.Body != "" || !got.DryRun {
		t.Fatalf("reply call = %+v", got)
	}
}

func TestCmdSendWithoutVoiceStillNeedsText(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "personal", "5511999"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 2 || len(backend.sendCalls) != 0 {
		t.Fatalf("exit = %d, sends = %d; want usage error", code, len(backend.sendCalls))
	}
}

func TestCmdVoiceMissingFileFailsLoudly(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "personal", "5511999", "--voice", "/no/such/nota.ogg", "--json"},
		strings.NewReader(""), &stdout, &stderr)
	if code == 0 || len(backend.sendCalls) != 0 {
		t.Fatalf("exit = %d, sends = %d; want a failure before sending", code, len(backend.sendCalls))
	}
}

// TestSendVoiceDryRunJSONAgainstService drives the real Service over the
// fake adapter: the dry-run plan must say it is a voice note and how long.
func TestSendVoiceDryRunJSONAgainstService(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg := core.NewRegistry()
	reg.Register(fake.New(core.ChannelWhatsApp, "demo"))
	svc := core.NewService(st, reg)
	path := writeTempFile(t, "nota.ogg", oggfixture.Bytes(12*time.Second))

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), svc,
		[]string{"send", "whatsapp", "demo", "5511999", "--voice", path, "--dry-run", "--json"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var res struct {
		DryRun bool      `json:"dryRun"`
		Plan   core.Plan `json:"plan"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("bad JSON %q: %v", stdout.String(), err)
	}
	if !res.DryRun || !res.Plan.Voice || len(res.Plan.Attachments) != 1 || res.Plan.Attachments[0].DurationMS != 12000 {
		t.Fatalf("result = %+v, want a 12 s voice plan", res)
	}

	// Human output names it too.
	stdout.Reset()
	code = runWithBackend(context.Background(), svc,
		[]string{"send", "whatsapp", "demo", "5511999", "--voice", path, "--dry-run"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "voice note") || !strings.Contains(stdout.String(), "0:12") {
		t.Fatalf("human output = %q", stdout.String())
	}

	// A non-Ogg file is refused with the conversion hint.
	stdout.Reset()
	stderr.Reset()
	bad := writeTempFile(t, "nota.mp3", []byte("ID3 nope"))
	code = runWithBackend(context.Background(), svc,
		[]string{"send", "whatsapp", "demo", "5511999", "--voice", bad, "--dry-run"},
		strings.NewReader(""), &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "ffmpeg") {
		t.Fatalf("exit=%d stderr=%q, want a conversion hint", code, stderr.String())
	}
}
