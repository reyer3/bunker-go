package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCmdSendHTMLFlagMarksOutgoing(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "mail", "cl", "a@x.cl", "<p>hola</p>", "--subject", "s", "--html", "--dry-run"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.sendCalls) != 1 || !backend.sendCalls[0].HTML || backend.sendCalls[0].Body != "<p>hola</p>" {
		t.Fatalf("sendCalls = %+v, want one HTML send with the body as given", backend.sendCalls)
	}
}

func TestCmdSendWithoutHTMLFlagStaysPlain(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "mail", "cl", "a@x.cl", "<p>hola</p>", "--dry-run"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 || len(backend.sendCalls) != 1 || backend.sendCalls[0].HTML {
		t.Fatalf("code=%d sendCalls=%+v, want a plain send", code, backend.sendCalls)
	}
}

func TestCmdReplyHTMLFlagMarksContext(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"reply", "mail:cl:1", "<p>re</p>", "--html", "--dry-run"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.replyHTML) != 1 || !backend.replyHTML[0] {
		t.Fatalf("replyHTML = %v, want one HTML reply", backend.replyHTML)
	}
	backend2 := newFakeBackend()
	runWithBackend(context.Background(), backend2, []string{"reply", "mail:cl:1", "x", "--dry-run"}, strings.NewReader(""), &stdout, &stderr)
	if len(backend2.replyHTML) != 1 || backend2.replyHTML[0] {
		t.Fatalf("replyHTML = %v, want a plain reply", backend2.replyHTML)
	}
}

func TestCmdHTMLRejectsVoice(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend,
		[]string{"send", "whatsapp", "wa", "+51111", "--voice", "x.ogg", "--html", "--dry-run"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 2 || len(backend.sendCalls) != 0 {
		t.Fatalf("code=%d sendCalls=%+v, want usage error and no send", code, backend.sendCalls)
	}
}
