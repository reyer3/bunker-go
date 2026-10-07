package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/reyer3/bunker-go/internal/secfile"
)

// StagingTTL is how long a staged download survives without being read.
// A remote client reads its token chunk by chunk and releases it at the
// end; one that dies halfway never does, so every StageDownload sweeps
// files idle for longer than this. Each ReadStaged refreshes the clock,
// so a slow but live transfer is never swept from under its reader.
const StagingTTL = 10 * time.Minute

// MaxStagedChunk caps one ReadStaged call. Base64 inflates it to about
// 5.6 MB, which still fits the RPC protocol's 8 MB line buffer
// (internal/rpc/server.go) with room for the JSON around it.
const MaxStagedChunk = 4 << 20

// stagingTokenLen is the hex length of a staging token: 16 random bytes.
const stagingTokenLen = 32

// StagedFile describes an attachment StageDownload copied into the
// staging area: the opaque Token that names it, its byte Size and
// SHA256 (hex) for the client to verify, and the attachment's Name/MIME.
type StagedFile struct {
	Token  string
	Size   int64
	Name   string
	MIME   string
	SHA256 string
}

// SetStagingDir sets where StageDownload keeps files for remote clients
// (production wiring: <stateDir>/staging in cmd/bunker/daemon.go; a
// t.TempDir() in tests). Like the avatar cache, staging refuses to run
// without it rather than guessing a default under a real HOME.
func (s *Service) SetStagingDir(dir string) { s.stagingDir = dir }

// SetStagingClock overrides the clock the staging TTL is measured with.
func (s *Service) SetStagingClock(now func() time.Time) { s.stagingClock = now }

// StageDownload resolves item id's attachment at index exactly like
// Download (same Fetch fallbacks and errors), fetches it once from the
// adapter and writes it into the staging dir under a fresh random token,
// for a client on another machine to read with ReadStaged. The adapter's
// reader is one-shot, which is why the bytes are parked on disk instead
// of being re-fetched per chunk. opts.Force is ignored (a token is always
// new) and opts.MaxBytes is clamped to DefaultMaxDownloadBytes, so a
// remote client can never make the daemon park more than a local
// download would write.
func (s *Service) StageDownload(ctx context.Context, id string, index int, opts DownloadOptions) (StagedFile, error) {
	if s.stagingDir == "" {
		return StagedFile{}, fmt.Errorf("core: staging dir not configured: %w", ErrUnsupported)
	}
	att, rc, err := s.openAttachment(ctx, id, index)
	if err != nil {
		return StagedFile{}, err
	}
	defer rc.Close()

	if err := secfile.EnsureDir(s.stagingDir); err != nil {
		return StagedFile{}, fmt.Errorf("core: create staging dir: %w", err)
	}
	s.sweepStaging()

	token, err := newStagingToken()
	if err != nil {
		return StagedFile{}, err
	}
	path := filepath.Join(s.stagingDir, token)
	opts.Force = false
	if opts.MaxBytes <= 0 || opts.MaxBytes > DefaultMaxDownloadBytes {
		opts.MaxBytes = DefaultMaxDownloadBytes
	}
	h := sha256.New()
	n, err := WriteAttachmentFile(path, io.TeeReader(rc, h), att.Size, opts)
	if err != nil {
		return StagedFile{}, fmt.Errorf("core: stage %s: %w", id, err)
	}
	s.touchStaged(path)
	return StagedFile{Token: token, Size: n, Name: att.Name, MIME: att.MIME, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// ReadStaged returns at most length bytes of token's staged file from
// offset; fewer (possibly none) at the end of the file. An unknown or
// already released token is ErrNotFound.
func (s *Service) ReadStaged(token string, offset, length int64) ([]byte, error) {
	path, err := s.stagedPath(token)
	if err != nil {
		return nil, err
	}
	if length <= 0 || length > MaxStagedChunk {
		return nil, fmt.Errorf("core: staged read length %d outside 1..%d", length, MaxStagedChunk)
	}
	if offset < 0 {
		return nil, fmt.Errorf("core: staged read offset %d is negative", offset)
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("core: staged file %s: %w", token, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("core: open staged file: %w", err)
	}
	defer f.Close()
	buf := make([]byte, length)
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("core: read staged file: %w", err)
	}
	s.touchStaged(path)
	return buf[:n], nil
}

// ReleaseStaged deletes token's staged file. Releasing a token that is
// already gone (swept, or released twice) is not an error: the client's
// goal, no staged copy left behind, already holds.
func (s *Service) ReleaseStaged(token string) error {
	path, err := s.stagedPath(token)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("core: release staged file: %w", err)
	}
	return nil
}

// stagedPath maps token to its file, accepting only the exact shape
// newStagingToken produces (lowercase hex of a fixed length): a token
// arrives over the RPC socket, and anything else (a "../x", a slash, an
// absolute path) must never reach the filesystem.
func (s *Service) stagedPath(token string) (string, error) {
	if s.stagingDir == "" {
		return "", fmt.Errorf("core: staging dir not configured: %w", ErrUnsupported)
	}
	if len(token) != stagingTokenLen {
		return "", fmt.Errorf("core: invalid staging token")
	}
	for _, r := range token {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", fmt.Errorf("core: invalid staging token")
		}
	}
	return filepath.Join(s.stagingDir, token), nil
}

func newStagingToken() (string, error) {
	b := make([]byte, stagingTokenLen/2)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("core: staging token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *Service) stagingNow() time.Time {
	if s.stagingClock != nil {
		return s.stagingClock()
	}
	return time.Now()
}

// touchStaged records path's last use as its mtime, the signal
// sweepStaging ages files by. A failure only makes the file look older.
func (s *Service) touchStaged(path string) {
	now := s.stagingNow()
	_ = os.Chtimes(path, now, now)
}

// sweepStaging removes every staging entry idle for longer than
// StagingTTL: tokens a client abandoned, and temp files a daemon crash
// left mid-write. Running it lazily on each StageDownload bounds the
// leftovers without a background goroutine the daemon has to own.
func (s *Service) sweepStaging() {
	entries, err := os.ReadDir(s.stagingDir)
	if err != nil {
		return
	}
	now := s.stagingNow()
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > StagingTTL {
			_ = os.Remove(filepath.Join(s.stagingDir, e.Name()))
		}
	}
}
