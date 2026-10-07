package core_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// newStagingService wires a Service whose only adapter serves data as
// item mail:cl:1's attachment 0, with its staging area in a fresh temp
// dir and a clock the test controls.
func newStagingService(t *testing.T, data []byte) (*core.Service, *spyDownloaderAdapter, string, *time.Time) {
	t.Helper()
	item := itemWithAttachment("mail:cl:1", core.Attachment{Name: "a.bin", MIME: "application/octet-stream", Size: int64(len(data))})
	reg := core.NewRegistry()
	spy := &spyDownloaderAdapter{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}, data: data}
	reg.Register(spy)
	svc := core.NewService(newMemStore(item), reg)
	dir := filepath.Join(t.TempDir(), "staging")
	svc.SetStagingDir(dir)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	svc.SetStagingClock(func() time.Time { return now })
	return svc, spy, dir, &now
}

func TestServiceStageDownloadReadAndRelease(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 1000)
	svc, spy, dir, _ := newStagingService(t, data)

	staged, err := svc.StageDownload(context.Background(), "mail:cl:1", 0, core.DownloadOptions{})
	if err != nil {
		t.Fatalf("StageDownload: %v", err)
	}
	sum := sha256.Sum256(data)
	if staged.Size != int64(len(data)) || staged.Name != "a.bin" || staged.MIME != "application/octet-stream" || staged.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("StageDownload = %+v, want size %d, a.bin, sha %x", staged, len(data), sum)
	}
	if spy.downloadCalls != 1 {
		t.Fatalf("adapter downloads = %d, want exactly 1 (the adapter's reader is one-shot)", spy.downloadCalls)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("staging dir mode = %v (%v), want 0700", info, err)
	}
	info, err := os.Stat(filepath.Join(dir, staged.Token))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("staged file mode = %v (%v), want 0600", info, err)
	}

	var got []byte
	for off := int64(0); off < staged.Size; off += 3000 {
		chunk, err := svc.ReadStaged(staged.Token, off, 3000)
		if err != nil {
			t.Fatalf("ReadStaged(%d): %v", off, err)
		}
		got = append(got, chunk...)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("reassembled %d bytes, want the %d staged bytes", len(got), len(data))
	}
	if tail, err := svc.ReadStaged(staged.Token, staged.Size, 10); err != nil || len(tail) != 0 {
		t.Fatalf("ReadStaged at EOF = %d bytes, %v; want 0, nil", len(tail), err)
	}

	if err := svc.ReleaseStaged(staged.Token); err != nil {
		t.Fatalf("ReleaseStaged: %v", err)
	}
	if _, err := svc.ReadStaged(staged.Token, 0, 10); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("ReadStaged after release err = %v, want ErrNotFound", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("staging dir still holds %d entries after release", len(entries))
	}
}

func TestServiceReadStagedRejectsBadTokensAndLengths(t *testing.T) {
	svc, _, dir, _ := newStagingService(t, []byte("payload"))
	staged, err := svc.StageDownload(context.Background(), "mail:cl:1", 0, core.DownloadOptions{})
	if err != nil {
		t.Fatalf("StageDownload: %v", err)
	}
	// A file next to the staging dir a traversal would reach.
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		token  string
		offset int64
		length int64
	}{
		{"path traversal", "../secret", 0, 10},
		{"absolute path", "/etc/passwd", 0, 10},
		{"uppercase hex", strings.ToUpper(staged.Token), 0, 10},
		{"empty token", "", 0, 10},
		{"well-formed but unknown token", strings.Repeat("ab", 16), 0, 10},
		{"length over the chunk cap", staged.Token, 0, core.MaxStagedChunk + 1},
		{"negative offset", staged.Token, -1, 10},
		{"zero length", staged.Token, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if data, err := svc.ReadStaged(tt.token, tt.offset, tt.length); err == nil {
				t.Fatalf("ReadStaged(%q, %d, %d) = %q, want an error", tt.token, tt.offset, tt.length, data)
			}
		})
	}
	if err := svc.ReleaseStaged("../secret"); err == nil {
		t.Fatal("ReleaseStaged(../secret) succeeded, want an error")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "secret")); err != nil {
		t.Fatalf("file outside the staging dir was touched: %v", err)
	}
}

func TestServiceStagingSweepsAbandonedFiles(t *testing.T) {
	svc, _, _, now := newStagingService(t, []byte("payload"))
	ctx := context.Background()

	abandoned, err := svc.StageDownload(ctx, "mail:cl:1", 0, core.DownloadOptions{})
	if err != nil {
		t.Fatalf("StageDownload: %v", err)
	}
	inUse, err := svc.StageDownload(ctx, "mail:cl:1", 0, core.DownloadOptions{})
	if err != nil {
		t.Fatalf("StageDownload: %v", err)
	}

	// Reading inUse just before the TTL runs out keeps it alive.
	*now = now.Add(core.StagingTTL - time.Second)
	if _, err := svc.ReadStaged(inUse.Token, 0, 1); err != nil {
		t.Fatalf("ReadStaged(inUse): %v", err)
	}
	*now = now.Add(2 * time.Second)
	if _, err := svc.StageDownload(ctx, "mail:cl:1", 0, core.DownloadOptions{}); err != nil {
		t.Fatalf("StageDownload (sweeping): %v", err)
	}

	if _, err := svc.ReadStaged(abandoned.Token, 0, 1); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("abandoned token err = %v, want ErrNotFound after the TTL sweep", err)
	}
	if _, err := svc.ReadStaged(inUse.Token, 0, 1); err != nil {
		t.Fatalf("recently read token was swept: %v", err)
	}
}

func TestServiceStageDownloadEnforcesMaxBytes(t *testing.T) {
	svc, _, dir, _ := newStagingService(t, bytes.Repeat([]byte("x"), 100))
	_, err := svc.StageDownload(context.Background(), "mail:cl:1", 0, core.DownloadOptions{MaxBytes: 10})
	if !errors.Is(err, core.ErrAttachmentTooLarge) {
		t.Fatalf("StageDownload err = %v, want ErrAttachmentTooLarge", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("staging dir holds %d entries after a refused stage, want 0", len(entries))
	}
}

func TestServiceStageDownloadWithoutStagingDirIsUnsupported(t *testing.T) {
	item := itemWithAttachment("mail:cl:1", core.Attachment{Name: "a", Size: 1})
	svc := core.NewService(newMemStore(item), core.NewRegistry())
	if _, err := svc.StageDownload(context.Background(), "mail:cl:1", 0, core.DownloadOptions{}); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestServiceStageDownloadKeepsDownloadErrors(t *testing.T) {
	svc, _, _, _ := newStagingService(t, []byte("payload"))
	if _, err := svc.StageDownload(context.Background(), "mail:cl:1", 5, core.DownloadOptions{}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("out-of-range index err = %v, want ErrNotFound", err)
	}
}

func TestWriteAttachmentFileIsExported(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	n, err := core.WriteAttachmentFile(dest, strings.NewReader("abc"), 3, core.DownloadOptions{})
	if err != nil || n != 3 {
		t.Fatalf("WriteAttachmentFile = %d, %v; want 3, nil", n, err)
	}
	if info, err := os.Stat(dest); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v (%v), want 0600", info, err)
	}
}
