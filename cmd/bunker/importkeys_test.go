package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/channel/matrix"
	"github.com/reyer3/bunker-go/internal/config"
)

func TestCmdImportKeysMatrixReadsFilePassphraseStdinAndReportsCounts(t *testing.T) {
	origImport := matrixImportKeyExportFunc
	defer func() { matrixImportKeyExportFunc = origImport }()

	var gotAccount, gotPassphrase string
	var gotData []byte
	matrixImportKeyExportFunc = func(_ context.Context, acc config.Account, passphrase string, data []byte) (matrix.ImportKeyResult, error) {
		gotAccount = acc.Name
		gotPassphrase = passphrase
		gotData = data
		return matrix.ImportKeyResult{New: 3, AlreadyKnown: 7, Total: 10}, nil
	}

	file := filepath.Join(t.TempDir(), "export.txt")
	const exportContents = "-----BEGIN MEGOLM SESSION DATA-----\nfake\n-----END MEGOLM SESSION DATA-----\n"
	if err := os.WriteFile(file, []byte(exportContents), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var stdout, stderr bytes.Buffer
	// A passphrase with meaningful internal spaces: only the trailing
	// newline piped input adds must be trimmed, nothing else.
	code := cmdImportKeys(context.Background(), testConfig(), []string{"matrix", "work", file, "--passphrase-stdin"}, pipedStdin(t, "correct horse battery staple\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cmdImportKeys exit code = %d, stderr = %s", code, stderr.String())
	}
	if gotAccount != "work" {
		t.Errorf("account = %q, want work", gotAccount)
	}
	if gotPassphrase != "correct horse battery staple" {
		t.Errorf("passphrase = %q, want the internal spaces preserved and only the trailing newline trimmed", gotPassphrase)
	}
	if string(gotData) != exportContents {
		t.Errorf("data = %q, want the file's contents verbatim", gotData)
	}
	if !strings.Contains(stdout.String(), "3 new, 7 already known") || !strings.Contains(stdout.String(), "10 in export") {
		t.Errorf("stdout = %q, want it to report 3 new, 7 already known (10 in export)", stdout.String())
	}
}

func TestCmdImportKeysMatrixRequiresATerminalWithoutPassphraseStdin(t *testing.T) {
	file := filepath.Join(t.TempDir(), "export.txt")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdImportKeys(context.Background(), testConfig(), []string{"matrix", "work", file}, closedStdin(t), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("cmdImportKeys exit code = %d, want 1, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not a terminal") {
		t.Fatalf("stderr = %q, want it to explain stdin is not a terminal", stderr.String())
	}
}

func TestCmdImportKeysMatrixMissingFileErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdImportKeys(context.Background(), testConfig(), []string{"matrix", "work", filepath.Join(t.TempDir(), "does-not-exist.txt"), "--passphrase-stdin"}, pipedStdin(t, "pw\n"), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("cmdImportKeys exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "read key export file") {
		t.Fatalf("stderr = %q, want it to name the file-read failure", stderr.String())
	}
}

func TestCmdImportKeysErrorNeverIncludesThePassphrase(t *testing.T) {
	origImport := matrixImportKeyExportFunc
	defer func() { matrixImportKeyExportFunc = origImport }()
	matrixImportKeyExportFunc = func(context.Context, config.Account, string, []byte) (matrix.ImportKeyResult, error) {
		return matrix.ImportKeyResult{}, errors.New("mismatching hash; incorrect passphrase?")
	}

	file := filepath.Join(t.TempDir(), "export.txt")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	const secretPassphrase = "correct horse battery staple"
	var stdout, stderr bytes.Buffer
	code := cmdImportKeys(context.Background(), testConfig(), []string{"matrix", "work", file, "--passphrase-stdin"}, pipedStdin(t, secretPassphrase+"\n"), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("cmdImportKeys exit code = %d, want 1", code)
	}
	if strings.Contains(stdout.String(), secretPassphrase) || strings.Contains(stderr.String(), secretPassphrase) {
		t.Fatalf("output leaked the passphrase: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCmdImportKeysUnknownChannelErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdImportKeys(context.Background(), testConfig(), []string{"whatsapp", "personal", "file.txt"}, closedStdin(t), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("cmdImportKeys exit code = %d, want 2", code)
	}
}

func TestCmdImportKeysMissingArgsIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdImportKeys(context.Background(), testConfig(), []string{"matrix", "work"}, closedStdin(t), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("cmdImportKeys exit code = %d, want 2", code)
	}
}

func TestCmdImportKeysUnknownAccountErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdImportKeys(context.Background(), testConfig(), []string{"matrix", "ghost", "file.txt", "--passphrase-stdin"}, pipedStdin(t, "pw\n"), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("cmdImportKeys exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "ghost") {
		t.Fatalf("stderr = %q, want it to name the missing account", stderr.String())
	}
}
