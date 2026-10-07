package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/reyer3/bunker-go/internal/secfile"
)

// MaxUploadBytes caps one file a remote client uploads for a send. No
// adapter accepts more than this (each AttachmentPolicy is lower, and
// still applies after the upload), and it matches what a download may
// park in the same staging dir.
const MaxUploadBytes = DefaultMaxDownloadBytes

// uploadPartSuffix names the file an upload is written to until
// CommitUpload renames it to its real name: <staging>/<token>/<token>.part.
// Embedding the random token makes it a name no committed upload has, so
// IsStagedUpload can tell a half-written upload from a finished one even
// after a daemon restart forgot which uploads were in flight.
const uploadPartSuffix = ".part"

// pendingUpload is an upload BeginUpload opened and CommitUpload has not
// finished yet.
type pendingUpload struct {
	f       *os.File
	name    string
	size    int64
	written int64
	sha256  string
	h       hash.Hash
}

// BeginUpload starts staging a file a client on another machine is about
// to send: size bytes (1..MaxUploadBytes) whose sha256 is sha256Hex, to
// be named name (the attachment name the recipient sees, so it is kept
// as given but must be a plain file name, never a path). The bytes go
// into <staging>/<token>/, a directory of its own, through
// WriteUploadChunk; CommitUpload turns them into a file Send, Reply and
// PostStatus accept.
func (s *Service) BeginUpload(name string, size int64, sha256Hex string) (string, error) {
	if s.stagingDir == "" {
		return "", fmt.Errorf("core: staging dir not configured: %w", ErrUnsupported)
	}
	if !validUploadName(name) {
		return "", fmt.Errorf("core: upload name %q is not a plain file name", name)
	}
	if size <= 0 || size > MaxUploadBytes {
		return "", fmt.Errorf("core: upload size %d outside 1..%d: %w", size, MaxUploadBytes, ErrAttachmentTooLarge)
	}
	if !validHex(sha256Hex, sha256.Size*2) {
		return "", fmt.Errorf("core: upload sha256 %q is not %d lowercase hex digits", sha256Hex, sha256.Size*2)
	}
	if err := secfile.EnsureDir(s.stagingDir); err != nil {
		return "", fmt.Errorf("core: create staging dir: %w", err)
	}
	s.sweepStaging()

	token, err := newStagingToken()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.stagingDir, token)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("core: create upload dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, token+uploadPartSuffix), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("core: create upload file: %w", err)
	}
	s.uploadMu.Lock()
	if s.uploads == nil {
		s.uploads = make(map[string]*pendingUpload)
	}
	s.uploads[token] = &pendingUpload{f: f, name: name, size: size, sha256: sha256Hex, h: sha256.New()}
	s.uploadMu.Unlock()
	s.touchStaged(dir)
	return token, nil
}

// WriteUploadChunk appends data (1..MaxStagedChunk bytes) to token's
// upload. offset must equal the bytes written so far: chunks arrive in
// order over one connection, so anything else is a confused or forged
// request, and accepting it would let a client seek around the file.
func (s *Service) WriteUploadChunk(token string, offset int64, data []byte) error {
	dir, err := s.stagedPath(token)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > MaxStagedChunk {
		return fmt.Errorf("core: upload chunk of %d bytes outside 1..%d", len(data), MaxStagedChunk)
	}
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	u, ok := s.uploads[token]
	if !ok {
		return fmt.Errorf("core: upload %s: %w", token, ErrNotFound)
	}
	if offset != u.written {
		return fmt.Errorf("core: upload chunk at offset %d, want %d", offset, u.written)
	}
	if u.written+int64(len(data)) > u.size {
		return fmt.Errorf("core: upload chunk ends at %d, past the declared %d bytes", u.written+int64(len(data)), u.size)
	}
	if _, err := u.f.Write(data); err != nil {
		return fmt.Errorf("core: write upload: %w", err)
	}
	u.h.Write(data)
	u.written += int64(len(data))
	// The sweep ages an upload by its directory's mtime, which writing
	// the file inside it never changes on its own.
	s.touchStaged(dir)
	return nil
}

// CommitUpload finishes token's upload: when every declared byte arrived
// and their sha256 matches, the file is renamed to its real name and its
// absolute path, <staging>/<token>/<name>, is returned for the client to
// put in its send. An incomplete or corrupted upload is removed, so the
// only files left under an upload dir are ones IsStagedUpload accepts.
func (s *Service) CommitUpload(token string) (string, error) {
	dir, err := s.stagedPath(token)
	if err != nil {
		return "", err
	}
	s.uploadMu.Lock()
	u, ok := s.uploads[token]
	delete(s.uploads, token)
	s.uploadMu.Unlock()
	if !ok {
		return "", fmt.Errorf("core: upload %s: %w", token, ErrNotFound)
	}
	cerr := u.f.Close()
	fail := func(err error) (string, error) {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if cerr != nil {
		return fail(fmt.Errorf("core: close upload: %w", cerr))
	}
	if u.written != u.size {
		return fail(fmt.Errorf("core: upload %s got %d of %d bytes", u.name, u.written, u.size))
	}
	if got := hex.EncodeToString(u.h.Sum(nil)); got != u.sha256 {
		return fail(fmt.Errorf("core: upload %s sha256 %s, client declared %s", u.name, got, u.sha256))
	}
	path := filepath.Join(dir, u.name)
	if err := os.Rename(filepath.Join(dir, token+uploadPartSuffix), path); err != nil {
		return fail(fmt.Errorf("core: commit upload: %w", err))
	}
	s.touchStaged(dir)
	return path, nil
}

// ReleaseUpload deletes token's upload, finished or not. Like
// ReleaseStaged, releasing one that is already gone is not an error.
func (s *Service) ReleaseUpload(token string) error {
	dir, err := s.stagedPath(token)
	if err != nil {
		return err
	}
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	s.dropUploadLocked(token)
	// Only an upload's directory: a download token names a file, and
	// that one is released through ReleaseStaged.
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("core: release upload: %w", err)
	}
	return nil
}

// dropUploadLocked forgets token's pending upload, if any, closing its
// file. The caller holds uploadMu.
func (s *Service) dropUploadLocked(token string) {
	if u, ok := s.uploads[token]; ok {
		_ = u.f.Close()
		delete(s.uploads, token)
	}
}

// IsStagedUpload reports whether path is a file CommitUpload produced:
// exactly <staging>/<token>/<name> once cleaned, with a well-formed
// token that is not still being written, and both the directory and the
// file real ones rather than symlinks that could lead elsewhere.
func (s *Service) IsStagedUpload(path string) bool {
	if s.stagingDir == "" || !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(s.stagingDir), filepath.Clean(path))
	if err != nil {
		return false
	}
	token, name, ok := strings.Cut(rel, string(filepath.Separator))
	if !ok || !validUploadName(name) || name == token+uploadPartSuffix {
		return false
	}
	dir, err := s.stagedPath(token)
	if err != nil {
		return false
	}
	s.uploadMu.Lock()
	_, pending := s.uploads[token]
	s.uploadMu.Unlock()
	if pending {
		return false
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return false
	}
	info, err := os.Lstat(filepath.Join(dir, name))
	return err == nil && info.Mode().IsRegular()
}

// checkStagedPaths is the daemon's guard on the files a send names. The
// RPC socket cannot tell a client on this machine from one on another,
// and a remote client's files only exist here as committed uploads, so
// any path that is, or resolves through symlinks to, somewhere inside
// the staging dir must be exactly a committed upload: never a download
// token's file (another item's attachment) or a half-written upload.
// Paths outside staging are a local client's own files, opened as
// before. It lives in Service rather than the RPC server so every entry
// point (RPC, MCP, an in-process backend) gets the same check, on dry
// runs too.
func (s *Service) checkStagedPaths(paths ...string) error {
	if s.stagingDir == "" {
		return nil
	}
	for _, p := range paths {
		if p == "" || !s.underStaging(p) {
			continue
		}
		// IsStagedUpload Lstats the token dir and the file, so a path
		// that passes cannot lead out of the upload through a symlink;
		// one outside staging that resolves into it fails here as well.
		abs, err := filepath.Abs(p)
		if err != nil || !s.IsStagedUpload(abs) {
			return fmt.Errorf("core: attachment %q is not a committed upload: %w", p, ErrNotFound)
		}
	}
	return nil
}

// underStaging reports whether p, taken as given or with its symlinks
// resolved, lies inside the staging dir (or is the dir itself).
func (s *Service) underStaging(p string) bool {
	staging := filepath.Clean(s.stagingDir)
	abs, err := filepath.Abs(p)
	if err != nil {
		return true
	}
	if within(staging, abs) {
		return true
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false
	}
	if within(staging, real) {
		return true
	}
	if realStaging, err := filepath.EvalSymlinks(staging); err == nil && within(realStaging, real) {
		return true
	}
	return false
}

func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// fingerprintPaths is what an idempotency fingerprint records for the
// files a send names. A committed upload stands for its name and content
// rather than its path: a remote client retrying with the same key
// uploads the file again under a fresh token, and that retry must replay
// the first send instead of looking like a different message.
func (s *Service) fingerprintPaths(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = p
		if !s.IsStagedUpload(p) {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err == nil {
			out[i] = "upload:" + filepath.Base(p) + ":" + hex.EncodeToString(h.Sum(nil))
		}
	}
	return out
}

// validUploadName accepts a plain, non-empty file name: no directory
// separators, no "." or "..", no NUL or control characters, valid UTF-8
// and within a file system's 255-byte name limit.
func validUploadName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
