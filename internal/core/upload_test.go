package core_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// uploadFile runs a whole upload of data under name in chunk-sized
// pieces and returns the committed path.
func uploadFile(t *testing.T, svc *core.Service, name string, data []byte, chunk int) string {
	t.Helper()
	token, err := svc.BeginUpload(name, int64(len(data)), sha256Hex(data))
	if err != nil {
		t.Fatalf("BeginUpload(%q): %v", name, err)
	}
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		if err := svc.WriteUploadChunk(token, int64(off), data[off:end]); err != nil {
			t.Fatalf("WriteUploadChunk(%d): %v", off, err)
		}
	}
	path, err := svc.CommitUpload(token)
	if err != nil {
		t.Fatalf("CommitUpload: %v", err)
	}
	return path
}

func TestServiceUploadCommitsUnderOriginalName(t *testing.T) {
	svc, _, dir, _ := newStagingService(t, []byte("x"))
	data := bytes.Repeat([]byte("abcdefgh"), 4000)

	path := uploadFile(t, svc, "Informe final.pdf", data, 10000)

	if filepath.Base(path) != "Informe final.pdf" || filepath.Dir(filepath.Dir(path)) != dir {
		t.Fatalf("committed path = %s, want <staging>/<token>/Informe final.pdf under %s", path, dir)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("committed bytes differ (%v)", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("committed file mode = %v (%v), want 0600", info, err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("upload dir mode = %v (%v), want 0700", info, err)
	}
	if !svc.IsStagedUpload(path) {
		t.Fatalf("IsStagedUpload(%s) = false for a committed upload", path)
	}

	token := filepath.Base(filepath.Dir(path))
	if err := svc.ReleaseUpload(token); err != nil {
		t.Fatalf("ReleaseUpload: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("upload dir after release: %v, want it gone", err)
	}
	if err := svc.ReleaseUpload(token); err != nil {
		t.Fatalf("second ReleaseUpload: %v, want idempotent", err)
	}
}

func TestServiceBeginUploadRejectsBadInput(t *testing.T) {
	svc, _, _, _ := newStagingService(t, []byte("x"))
	sum := sha256Hex([]byte("x"))
	tests := []struct {
		name, file, sha string
		size            int64
	}{
		{"parent escape", "../x", sum, 1},
		{"separator", "a/b", sum, 1},
		{"backslash", `a\b`, sum, 1},
		{"empty name", "", sum, 1},
		{"dot", ".", sum, 1},
		{"dot dot", "..", sum, 1},
		{"zero size", "a.txt", sum, 0},
		{"over the cap", "a.txt", sum, core.MaxUploadBytes + 1},
		{"bad sha", "a.txt", "nothex", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if token, err := svc.BeginUpload(tt.file, tt.size, tt.sha); err == nil {
				t.Fatalf("BeginUpload(%q, %d, %q) = %s, want an error", tt.file, tt.size, tt.sha, token)
			}
		})
	}
}

func TestServiceUploadChunkRules(t *testing.T) {
	svc, _, _, _ := newStagingService(t, []byte("x"))
	data := []byte("0123456789")
	token, err := svc.BeginUpload("a.txt", int64(len(data)), sha256Hex(data))
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	if err := svc.WriteUploadChunk(token, 0, data[:4]); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if err := svc.WriteUploadChunk(token, 2, data[2:6]); err == nil {
		t.Fatal("overlapping offset accepted, want sequential offsets only")
	}
	if err := svc.WriteUploadChunk(token, 6, data[6:]); err == nil {
		t.Fatal("offset past a gap accepted, want sequential offsets only")
	}
	if err := svc.WriteUploadChunk(token, 4, append(data[4:], 'x')); err == nil {
		t.Fatal("chunk past the declared size accepted")
	}
	if err := svc.WriteUploadChunk(token, 4, make([]byte, core.MaxStagedChunk+1)); err == nil {
		t.Fatal("oversized chunk accepted")
	}
	if err := svc.WriteUploadChunk("00000000000000000000000000000000", 0, data); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown token err = %v, want ErrNotFound", err)
	}
	if err := svc.WriteUploadChunk("../x", 0, data); err == nil {
		t.Fatal("malformed token accepted")
	}
}

func TestServiceCommitUploadRemovesMismatches(t *testing.T) {
	svc, _, dir, _ := newStagingService(t, []byte("x"))
	data := []byte("0123456789")

	short, err := svc.BeginUpload("a.txt", int64(len(data)), sha256Hex(data))
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	if err := svc.WriteUploadChunk(short, 0, data[:5]); err != nil {
		t.Fatalf("chunk: %v", err)
	}
	if _, err := svc.CommitUpload(short); err == nil {
		t.Fatal("commit of a short upload succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, short)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("short upload left %v behind", err)
	}

	wrong, err := svc.BeginUpload("a.txt", int64(len(data)), sha256Hex([]byte("something else")))
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	if err := svc.WriteUploadChunk(wrong, 0, data); err != nil {
		t.Fatalf("chunk: %v", err)
	}
	if _, err := svc.CommitUpload(wrong); err == nil {
		t.Fatal("commit with a sha256 mismatch succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, wrong)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched upload left %v behind", err)
	}
	if _, err := svc.CommitUpload(wrong); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("second commit err = %v, want ErrNotFound", err)
	}
}

func TestServiceStagingSweepsStaleUploads(t *testing.T) {
	svc, _, dir, now := newStagingService(t, []byte("x"))
	data := []byte("0123456789")

	stale := uploadFile(t, svc, "old.txt", data, 4)
	live, err := svc.BeginUpload("live.txt", int64(len(data)), sha256Hex(data))
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	abandoned, err := svc.BeginUpload("gone.txt", int64(len(data)), sha256Hex(data))
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}

	// A chunk just before the TTL runs out keeps the live upload alive.
	*now = now.Add(core.StagingTTL - time.Second)
	if err := svc.WriteUploadChunk(live, 0, data[:4]); err != nil {
		t.Fatalf("live chunk: %v", err)
	}
	*now = now.Add(2 * time.Second)
	if _, err := svc.BeginUpload("new.txt", 1, sha256Hex([]byte("y"))); err != nil {
		t.Fatalf("BeginUpload (sweeping): %v", err)
	}

	if _, err := os.Stat(filepath.Dir(stale)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale committed upload: %v, want swept", err)
	}
	if _, err := os.Stat(filepath.Join(dir, abandoned)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned upload: %v, want swept", err)
	}
	if err := svc.WriteUploadChunk(abandoned, 0, data); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("chunk to a swept upload: %v, want ErrNotFound", err)
	}
	if err := svc.WriteUploadChunk(live, 4, data[4:]); err != nil {
		t.Fatalf("live upload was swept: %v", err)
	}
	if _, err := svc.CommitUpload(live); err != nil {
		t.Fatalf("CommitUpload(live): %v", err)
	}
}

func TestServiceIsStagedUploadOnlyForCommittedUploads(t *testing.T) {
	svc, _, dir, _ := newStagingService(t, []byte("payload"))
	committed := uploadFile(t, svc, "a.txt", []byte("hello"), 5)
	token := filepath.Base(filepath.Dir(committed))

	download, err := svc.StageDownload(context.Background(), "mail:cl:1", 0, core.DownloadOptions{})
	if err != nil {
		t.Fatalf("StageDownload: %v", err)
	}
	pending, err := svc.BeginUpload("p.txt", 5, sha256Hex([]byte("hello")))
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	outside := writeTestFile(t, "a.txt", []byte("hello"))
	link := filepath.Join(dir, token, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"committed upload", committed, true},
		{"committed upload, uncleaned", dir + "/" + token + "/./a.txt", true},
		{"download token file", filepath.Join(dir, download.Token), false},
		{"download token as dir", filepath.Join(dir, download.Token, "a.txt"), false},
		{"pending upload", filepath.Join(dir, pending, "p.txt"), false},
		{"pending part file", filepath.Join(dir, pending, pending+".part"), false},
		{"forged token", filepath.Join(dir, "0123456789abcdef0123456789abcdef", "a.txt"), false},
		{"upload dir itself", filepath.Join(dir, token), false},
		{"escape after clean", dir + "/" + token + "/../../a.txt", false},
		{"symlink inside an upload dir", link, false},
		{"relative", filepath.Join("staging", token, "a.txt"), false},
		{"outside staging", outside, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := svc.IsStagedUpload(tt.path); got != tt.want {
				t.Fatalf("IsStagedUpload(%s) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestServiceBeginUploadWithoutStagingDirIsUnsupported(t *testing.T) {
	svc := core.NewService(newMemStore(), core.NewRegistry())
	if _, err := svc.BeginUpload("a.txt", 1, sha256Hex([]byte("x"))); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if svc.IsStagedUpload("/a.txt") {
		t.Fatal("IsStagedUpload true without a staging dir")
	}
}

// newUploadSendService wires a Service with a staging dir and one media
// adapter accepting any file, for the staged-path guard on Send, Reply
// and PostStatus.
func newUploadSendService(t *testing.T) (*core.Service, *spyMediaAdapter, string) {
	t.Helper()
	spy := &spyMediaAdapter{
		spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"},
		policy:     core.AttachmentPolicy{MaxBytes: map[string]int64{core.AnyMIME: 1 << 20}},
	}
	reg := core.NewRegistry()
	reg.Register(spy)
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", From: core.Address{ID: "them@example.test"}}
	svc := core.NewService(newMemStore(item), reg)
	dir := filepath.Join(t.TempDir(), "staging")
	svc.SetStagingDir(dir)
	return svc, spy, dir
}

func TestServiceSendAcceptsCommittedUpload(t *testing.T) {
	svc, spy, _ := newUploadSendService(t)
	path := uploadFile(t, svc, "notes.txt", []byte("hello there"), 4)

	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"to@example.test"}, Body: "x", Attachments: []string{path}}
	plan, _, err := svc.Send(context.Background(), out, false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(plan.Attachments) != 1 || plan.Attachments[0].Name != "notes.txt" {
		t.Fatalf("plan attachments = %+v, want notes.txt", plan.Attachments)
	}
	if spy.sendMediaCalls != 1 || spy.lastMediaOutgoing.Attachments[0] != path {
		t.Fatalf("SendMedia calls = %d with %v, want one with %s", spy.sendMediaCalls, spy.lastMediaOutgoing.Attachments, path)
	}
}

// TestServiceRefusesStagingPathsThatAreNotCommittedUploads is the
// security guard: a client must not make the daemon send a download
// token's file, a half-written upload, or anything else under its
// staging dir by naming its path.
func TestServiceRefusesStagingPathsThatAreNotCommittedUploads(t *testing.T) {
	svc, spy, dir := newUploadSendService(t)
	committed := uploadFile(t, svc, "ok.txt", []byte("ok"), 2)

	downloadLike := filepath.Join(dir, "0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(downloadLike, []byte("daemon secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	pending, err := svc.BeginUpload("p.txt", 5, sha256Hex([]byte("hello")))
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	link := filepath.Join(t.TempDir(), "innocent.txt")
	if err := os.Symlink(downloadLike, link); err != nil {
		t.Fatal(err)
	}
	forged := []string{
		downloadLike,
		filepath.Join(dir, pending, pending+".part"),
		filepath.Join(dir, "fedcba9876543210fedcba9876543210", "x.txt"),
		filepath.Join(filepath.Dir(committed), "..", "0123456789abcdef0123456789abcdef"),
		link,
	}
	ctx := context.Background()
	for _, p := range forged {
		for _, dryRun := range []bool{true, false} {
			out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"to@example.test"}, Body: "x", Attachments: []string{committed, p}}
			if _, _, err := svc.Send(ctx, out, dryRun); !errors.Is(err, core.ErrNotFound) {
				t.Errorf("Send(%s, dry=%v) err = %v, want ErrNotFound", p, dryRun, err)
			}
			if _, _, err := svc.Reply(ctx, "mail:cl:1", "x", nil, []string{p}, dryRun); !errors.Is(err, core.ErrNotFound) {
				t.Errorf("Reply(%s, dry=%v) err = %v, want ErrNotFound", p, dryRun, err)
			}
			if _, _, err := svc.PostStatus(ctx, core.ChannelMail, "cl", core.Status{Media: p}, dryRun); !errors.Is(err, core.ErrNotFound) {
				t.Errorf("PostStatus(%s, dry=%v) err = %v, want ErrNotFound", p, dryRun, err)
			}
		}
	}
	if spy.sendMediaCalls != 0 {
		t.Fatalf("SendMedia called %d times, want never", spy.sendMediaCalls)
	}
}

// TestServiceIdempotentSendMatchesReuploadedFile covers a remote client
// retrying with the same idempotency key: its retry uploads the file
// again under a new token, and that must replay, not count as a
// different message.
func TestServiceIdempotentSendMatchesReuploadedFile(t *testing.T) {
	svc, spy, _ := newUploadSendService(t)
	first := uploadFile(t, svc, "a.txt", []byte("same bytes"), 4)
	second := uploadFile(t, svc, "a.txt", []byte("same bytes"), 4)
	other := uploadFile(t, svc, "a.txt", []byte("other bytes"), 4)
	ctx := core.WithIdempotencyKey(context.Background(), "k-upload")
	send := func(path string) error {
		out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"to@example.test"}, Body: "x", Attachments: []string{path}}
		_, _, err := svc.Send(ctx, out, false)
		return err
	}
	if err := send(first); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if err := send(second); err != nil {
		t.Fatalf("retry with the re-uploaded file: %v", err)
	}
	if spy.sendMediaCalls != 1 {
		t.Fatalf("SendMedia calls = %d, want 1 (the retry replays)", spy.sendMediaCalls)
	}
	if err := send(other); err == nil {
		t.Fatal("same key with different file content succeeded, want a different-message error")
	}
}
