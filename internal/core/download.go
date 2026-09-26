package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// DefaultMaxDownloadBytes caps how large an attachment Service.Download
// writes to disk when DownloadOptions.MaxBytes is zero. 100 MB is
// generous for a mail or WhatsApp attachment while still bounding a
// misbehaving adapter (or a declared Size that lied) to something finite.
const DefaultMaxDownloadBytes = 100 << 20

// DownloadOptions controls how Service.Download writes an attachment to
// disk.
type DownloadOptions struct {
	// Force allows overwriting an existing file at destPath. Without it,
	// Download refuses with ErrDestinationExists.
	Force bool
	// MaxBytes caps the attachment's size; zero uses
	// DefaultMaxDownloadBytes.
	MaxBytes int64
}

// DownloadResult reports what Service.Download actually wrote.
type DownloadResult struct {
	Path  string
	Bytes int64
	Name  string
	MIME  string
}

// Download resolves item id's attachment at index and writes its raw
// bytes to destPath.
//
// It never returns the bytes to the caller: the CLI and the daemon run
// as the same user on the same machine, so the daemon writing the file
// directly is simpler than streaming a potentially 100 MB attachment
// back over the RPC socket's line-delimited JSON protocol (whose
// scanner buffer, internal/rpc/server.go, is capped at 8 MB — far below
// this cap) — and it keeps the size cap and 0600 permission enforced in
// exactly one place instead of twice.
func (s *Service) Download(ctx context.Context, id string, index int, destPath string, opts DownloadOptions) (DownloadResult, error) {
	item, err := s.store.Get(ctx, id)
	if err != nil {
		return DownloadResult{}, err
	}
	if index >= len(item.Attachments) {
		// Mail sync stores headers only; attachments appear on the full
		// fetch, the same one `read` does.
		if item, err = s.Fetch(ctx, id); err != nil {
			return DownloadResult{}, fmt.Errorf("core: download %s: %w", id, err)
		}
	}
	if index < 0 || index >= len(item.Attachments) {
		return DownloadResult{}, fmt.Errorf("core: download %s: attachment index %d out of range (%d attachments): %w", id, index, len(item.Attachments), ErrNotFound)
	}
	att := item.Attachments[index]

	adapter, err := s.adapterFor(item.Channel, item.Account)
	if err != nil {
		return DownloadResult{}, err
	}
	downloader, ok := adapter.(AttachmentDownloader)
	if !ok {
		return DownloadResult{}, fmt.Errorf("core: adapter %s/%s cannot download attachments: %w", item.Channel, item.Account, ErrUnsupported)
	}

	rc, err := downloader.DownloadAttachment(ctx, item, index)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("core: download %s: %w", id, err)
	}
	defer rc.Close()

	n, err := writeAttachmentFile(destPath, rc, att.Size, opts)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("core: download %s: %w", id, err)
	}
	return DownloadResult{Path: destPath, Bytes: n, Name: att.Name, MIME: att.MIME}, nil
}

// writeAttachmentFile streams r into destPath through a temp file in the
// same directory plus a rename, so a reader that dies partway never
// leaves a half-written file at destPath. The temp file (and therefore
// the renamed destination) is created at 0600; destPath is refused when
// it already exists unless opts.Force; the copy is capped at
// opts.MaxBytes (or DefaultMaxDownloadBytes); and, when declaredSize is
// known (> 0), the actual byte count must match it exactly.
func writeAttachmentFile(destPath string, r io.Reader, declaredSize int64, opts DownloadOptions) (int64, error) {
	if !opts.Force {
		if _, err := os.Stat(destPath); err == nil {
			return 0, fmt.Errorf("%s: %w", destPath, ErrDestinationExists)
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, fmt.Errorf("stat %s: %w", destPath, err)
		}
	}

	max := opts.MaxBytes
	if max <= 0 {
		max = DefaultMaxDownloadBytes
	}

	dir := filepath.Dir(destPath)
	tmp, err := os.CreateTemp(dir, ".bunker-download-*")
	if err != nil {
		return 0, fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return 0, fmt.Errorf("chmod temp file: %w", err)
	}

	// Ask for one more byte than the cap allows: reading exactly max+1
	// bytes (or more, which CopyN silently truncates to max+1) is the
	// signal the attachment is over the cap; reading fewer with io.EOF is
	// the whole attachment, within budget.
	n, err := io.CopyN(tmp, r, max+1)
	if err != nil && !errors.Is(err, io.EOF) {
		cleanup()
		return 0, fmt.Errorf("write %s: %w", destPath, err)
	}
	if n > max {
		cleanup()
		return 0, fmt.Errorf("%d bytes exceeds the %d byte cap: %w", n, max, ErrAttachmentTooLarge)
	}
	if declaredSize > 0 && n != declaredSize {
		cleanup()
		return 0, fmt.Errorf("downloaded %d bytes, declared size was %d: %w", n, declaredSize, ErrSizeMismatch)
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("close %s: %w", destPath, err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("rename into place: %w", err)
	}
	return n, nil
}
