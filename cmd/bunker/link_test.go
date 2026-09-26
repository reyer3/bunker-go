package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/channel/matrix"
	"github.com/reyer3/bunker-go/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{Accounts: []config.Account{
		{Channel: "whatsapp", Name: "personal"},
		{Channel: "matrix", Name: "work"},
	}}
}

// closedStdin returns a *os.File that is never a terminal (term.IsTerminal
// reports false for a pipe) and reads as EOF immediately, standing in for
// "no interactive terminal, nothing piped in either" in tests that do not
// exercise --recovery-key/--recovery-key-stdin/--passphrase-stdin.
func closedStdin(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close write end: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// pipedStdin returns a *os.File (never a terminal) that reads content
// back out, standing in for a piped `--recovery-key-stdin`/
// `--passphrase-stdin` input.
func pipedStdin(t *testing.T, content string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	go func() {
		io.WriteString(w, content)
		w.Close()
	}()
	t.Cleanup(func() { r.Close() })
	return r
}

func TestCmdLinkWhatsAppCallsAdapterLinkFunc(t *testing.T) {
	origLink := whatsappLinkFunc
	defer func() { whatsappLinkFunc = origLink }()

	var gotAccount string
	whatsappLinkFunc = func(_ context.Context, acc config.Account, out io.Writer) error {
		gotAccount = acc.Name
		io.WriteString(out, "scan this QR\n")
		return nil
	}

	var stdout, stderr bytes.Buffer
	code := cmdLink(context.Background(), testConfig(), []string{"whatsapp", "personal"}, closedStdin(t), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cmdLink exit code = %d, stderr = %s", code, stderr.String())
	}
	if gotAccount != "personal" {
		t.Fatalf("whatsappLinkFunc called with account %q, want %q", gotAccount, "personal")
	}
	if !strings.Contains(stdout.String(), "scan this QR") {
		t.Fatalf("stdout = %q, want the adapter's own output relayed", stdout.String())
	}
}

func TestCmdLinkWhatsAppPropagatesAdapterError(t *testing.T) {
	origLink := whatsappLinkFunc
	defer func() { whatsappLinkFunc = origLink }()
	whatsappLinkFunc = func(context.Context, config.Account, io.Writer) error {
		return errors.New("boom")
	}

	var stdout, stderr bytes.Buffer
	code := cmdLink(context.Background(), testConfig(), []string{"whatsapp", "personal"}, closedStdin(t), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("cmdLink exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Fatalf("stderr = %q, want it to contain the adapter error", stderr.String())
	}
}

func TestCmdLinkMatrixCallsAdapterLoginFunc(t *testing.T) {
	origLogin := matrixLoginFunc
	defer func() { matrixLoginFunc = origLogin }()

	var gotAccount string
	matrixLoginFunc = func(_ context.Context, acc config.Account, out io.Writer) (matrix.Session, error) {
		gotAccount = acc.Name
		io.WriteString(out, "opening browser for SSO\n")
		return matrix.Session{UserID: "@alice:matrix.example.org"}, nil
	}

	var stdout, stderr bytes.Buffer
	code := cmdLink(context.Background(), testConfig(), []string{"matrix", "work"}, closedStdin(t), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cmdLink exit code = %d, stderr = %s", code, stderr.String())
	}
	if gotAccount != "work" {
		t.Fatalf("matrixLoginFunc called with account %q, want %q", gotAccount, "work")
	}
	if !strings.Contains(stdout.String(), "opening browser for SSO") {
		t.Fatalf("stdout = %q, want the adapter's own output relayed", stdout.String())
	}
}

func stubMatrixLogin(t *testing.T) {
	t.Helper()
	orig := matrixLoginFunc
	matrixLoginFunc = func(context.Context, config.Account, io.Writer) (matrix.Session, error) {
		return matrix.Session{UserID: "@alice:matrix.example.org"}, nil
	}
	t.Cleanup(func() { matrixLoginFunc = orig })
}

func TestCmdLinkMatrixRecoveryKeyRequiresATerminal(t *testing.T) {
	stubMatrixLogin(t)

	var stdout, stderr bytes.Buffer
	// closedStdin is a pipe, never a terminal: --recovery-key must refuse
	// to silently fall back to reading it as plaintext from a pipe.
	code := cmdLink(context.Background(), testConfig(), []string{"matrix", "work", "--recovery-key"}, closedStdin(t), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("cmdLink exit code = %d, want 1 (stdin is not a terminal), stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not a terminal") {
		t.Fatalf("stderr = %q, want it to explain stdin is not a terminal", stderr.String())
	}
	if !strings.Contains(stderr.String(), "recovery-key-stdin") {
		t.Fatalf("stderr = %q, want it to point at --recovery-key-stdin", stderr.String())
	}
}

func TestCmdLinkMatrixRecoveryKeyAndStdinAreMutuallyExclusive(t *testing.T) {
	stubMatrixLogin(t)

	var stdout, stderr bytes.Buffer
	code := cmdLink(context.Background(), testConfig(), []string{"matrix", "work", "--recovery-key", "--recovery-key-stdin"}, closedStdin(t), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("cmdLink exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("stderr = %q, want it to say the flags are mutually exclusive", stderr.String())
	}
}

func TestCmdLinkMatrixRecoveryKeyStdinNormalizesAndImports(t *testing.T) {
	stubMatrixLogin(t)
	origImport := matrixImportRecoveryKeyFunc
	defer func() { matrixImportRecoveryKeyFunc = origImport }()

	var gotKey string
	var gotAccount string
	matrixImportRecoveryKeyFunc = func(_ context.Context, acc config.Account, recoveryKey string) (matrix.RecoveryKeyResult, error) {
		gotAccount = acc.Name
		gotKey = recoveryKey
		return matrix.RecoveryKeyResult{MegolmSessionsRestored: 3, MegolmSessionsTotal: 5}, nil
	}

	// A key an extractor wrapped across lines, with the spec's own visual
	// grouping spaces thrown in too: normalizeSecret must strip all of it.
	piped := "EsTx 32c1\ntg4o 9F2z\n"
	var stdout, stderr bytes.Buffer
	code := cmdLink(context.Background(), testConfig(), []string{"matrix", "work", "--recovery-key-stdin"}, pipedStdin(t, piped), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cmdLink exit code = %d, stderr = %s", code, stderr.String())
	}
	if gotAccount != "work" {
		t.Errorf("matrixImportRecoveryKeyFunc account = %q, want work", gotAccount)
	}
	if want := "EsTx32c1tg4o9F2z"; gotKey != want {
		t.Errorf("normalized recovery key = %q, want %q", gotKey, want)
	}
	if !strings.Contains(stdout.String(), "3/5") {
		t.Errorf("stdout = %q, want it to report 3/5 megolm sessions restored", stdout.String())
	}
}

func TestCmdLinkMatrixRecoveryKeyErrorNeverIncludesTheKey(t *testing.T) {
	stubMatrixLogin(t)
	origImport := matrixImportRecoveryKeyFunc
	defer func() { matrixImportRecoveryKeyFunc = origImport }()

	const secretKey = "EsTxSuperSecretRecoveryKeyValue"
	matrixImportRecoveryKeyFunc = func(context.Context, config.Account, string) (matrix.RecoveryKeyResult, error) {
		return matrix.RecoveryKeyResult{}, errors.New("verify recovery key: mismatching MAC")
	}

	var stdout, stderr bytes.Buffer
	code := cmdLink(context.Background(), testConfig(), []string{"matrix", "work", "--recovery-key-stdin"}, pipedStdin(t, secretKey), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("cmdLink exit code = %d, want 1", code)
	}
	if strings.Contains(stderr.String(), secretKey) || strings.Contains(stdout.String(), secretKey) {
		t.Fatalf("output leaked the recovery key: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCmdLinkUnknownAccountErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdLink(context.Background(), testConfig(), []string{"whatsapp", "ghost"}, closedStdin(t), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("cmdLink exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "ghost") {
		t.Fatalf("stderr = %q, want it to name the missing account", stderr.String())
	}
}

func TestCmdLinkUnknownChannelErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdLink(context.Background(), testConfig(), []string{"mail", "cl"}, closedStdin(t), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("cmdLink exit code = %d, want 2 (mail has no link flow)", code)
	}
}

func TestCmdLinkMissingArgsIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdLink(context.Background(), testConfig(), []string{"whatsapp"}, closedStdin(t), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("cmdLink exit code = %d, want 2", code)
	}
}

// Live bug 2026-09-25: with a session already saved, `--recovery-key-stdin`
// re-ran SSO, minted a new device and left session.json and crypto.db on
// different device IDs. Importing a key into an existing login must reuse
// that session and never call the login flow.
func TestCmdLinkMatrixRecoveryKeyReusesExistingSession(t *testing.T) {
	t.Setenv("BUNKER_STATE_DIR", t.TempDir())
	acc := config.Account{Channel: "matrix", Name: "work"}
	if err := matrix.SaveSession(matrix.StateDirFor(acc), matrix.Session{
		HomeserverURL: "https://hs.example", UserID: "@u:hs.example", AccessToken: "tok", DeviceID: "EXISTING",
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	origLogin := matrixLoginFunc
	defer func() { matrixLoginFunc = origLogin }()
	loginCalled := false
	matrixLoginFunc = func(context.Context, config.Account, io.Writer) (matrix.Session, error) {
		loginCalled = true
		return matrix.Session{}, nil
	}
	origImport := matrixImportRecoveryKeyFunc
	defer func() { matrixImportRecoveryKeyFunc = origImport }()
	imported := false
	matrixImportRecoveryKeyFunc = func(context.Context, config.Account, string) (matrix.RecoveryKeyResult, error) {
		imported = true
		return matrix.RecoveryKeyResult{}, nil
	}

	var stdout, stderr bytes.Buffer
	code := cmdLink(context.Background(), testConfig(), []string{"matrix", "work", "--recovery-key-stdin"}, pipedStdin(t, "EsTx 32c1\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cmdLink exit code = %d, stderr = %s", code, stderr.String())
	}
	if loginCalled {
		t.Fatal("login flow ran although a session already exists; it must be reused")
	}
	if !imported {
		t.Fatal("recovery key was not imported")
	}
}
