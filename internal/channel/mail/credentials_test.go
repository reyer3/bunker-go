package mail

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEnvFilePasswordSourcePassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mail-cl.env")
	if err := os.WriteFile(path, []byte("# comment\nMAIL_PASSWORD='s3cr3t'\nOTHER=ignored\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	src := EnvFilePasswordSource{Path: path}
	got, err := src.Password(context.Background(), "cl")
	if err != nil {
		t.Fatalf("Password() error = %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("Password() = %q, want %q", got, "s3cr3t")
	}
}

func TestEnvFilePasswordSourceMissingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mail-cl.env")
	if err := os.WriteFile(path, []byte("OTHER=ignored\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	src := EnvFilePasswordSource{Path: path}
	if _, err := src.Password(context.Background(), "cl"); err == nil {
		t.Error("Password() error = nil, want an error for a missing key")
	}
}

func TestEnvFilePasswordSourceMissingFile(t *testing.T) {
	src := EnvFilePasswordSource{Path: filepath.Join(t.TempDir(), "missing.env")}
	if _, err := src.Password(context.Background(), "cl"); err == nil {
		t.Error("Password() error = nil, want an error for a missing file")
	}
}
