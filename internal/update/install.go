package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// MaxArchiveBytes bounds a release archive download; a real one is a
	// few tens of MiB.
	MaxArchiveBytes = 150 << 20
	// MaxChecksumsBytes bounds checksums.txt, a handful of lines.
	MaxChecksumsBytes = 64 << 10
	// maxBinaryBytes bounds the binary extracted from the archive.
	maxBinaryBytes = 300 << 20
	// BinaryName is the executable inside the archive.
	BinaryName = "bunker"
	// OldSuffix names the previous binary kept next to the new one, for
	// a manual rollback.
	OldSuffix = ".old"
)

// Plan is what "bunker update" is about to do; --dry-run prints it and
// stops.
type Plan struct {
	From         string `json:"from"`
	To           string `json:"to"`
	Asset        string `json:"asset"`
	AssetURL     string `json:"asset_url"`
	ChecksumsURL string `json:"checksums_url"`
	// Target is the resolved path of the binary to replace; Backup is
	// where the current one is kept.
	Target string `json:"target"`
	Backup string `json:"backup"`
}

// PlanFor picks rel's archive for goos/goarch and its checksums. A
// release without either is an error: there is nothing safe to install.
func PlanFor(rel Release, current, goos, goarch, target string) (Plan, error) {
	name := ArchiveName(rel.Version, goos, goarch)
	archive, ok := rel.Asset(name)
	if !ok {
		return Plan{}, fmt.Errorf("update: release %s has no build for %s/%s (no %s asset)", rel.Tag, goos, goarch, name)
	}
	sums, ok := rel.Asset(ChecksumsAsset)
	if !ok {
		return Plan{}, fmt.Errorf("update: release %s has no %s to verify %s against", rel.Tag, ChecksumsAsset, name)
	}
	return Plan{
		From:         current,
		To:           rel.Version,
		Asset:        name,
		AssetURL:     archive.URL,
		ChecksumsURL: sums.URL,
		Target:       target,
		Backup:       target + OldSuffix,
	}, nil
}

// CheckWritable fails when a new file cannot be created in dir, which
// is what replacing the binary needs (not write access to the binary
// itself).
func CheckWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".bunker-write-check-*")
	if err != nil {
		return fmt.Errorf("update: cannot write to %s: %w", dir, err)
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("update: cannot write to %s: %w", dir, err)
	}
	return nil
}

// Apply downloads plan's archive and checksums, verifies the archive's
// SHA-256, extracts the binary and swaps it into plan.Target, keeping
// the current one at plan.Backup. Nothing on disk changes until the
// checksum has matched and the binary has been extracted, so any error
// before the swap leaves the installed binary as it was.
func Apply(ctx context.Context, client *http.Client, plan Plan) error {
	if client == nil {
		client = http.DefaultClient
	}
	sums, err := download(ctx, client, plan.ChecksumsURL, MaxChecksumsBytes)
	if err != nil {
		return err
	}
	want, err := checksumFor(sums, plan.Asset)
	if err != nil {
		return err
	}
	archive, err := download(ctx, client, plan.AssetURL, MaxArchiveBytes)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("update: checksum mismatch for %s: got sha256 %s, %s says %s; nothing was installed", plan.Asset, got, ChecksumsAsset, want)
	}
	bin, err := extractBinary(archive)
	if err != nil {
		return err
	}
	return install(bin, plan.Target, plan.Backup)
}

// download GETs url, refusing a non-200 answer or a body over limit.
func download(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("update: download %s: %w", url, err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: download %s: %s", url, resp.Status)
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("update: download %s: %d bytes is over the %d-byte limit", url, resp.ContentLength, limit)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("update: download %s: %w", url, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("update: download %s: over the %d-byte limit", url, limit)
	}
	return body, nil
}

// checksumFor finds name's sha256 in a goreleaser checksums.txt
// ("<hex>  <name>" per line).
func checksumFor(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		sum := strings.ToLower(fields[0])
		if b, err := hex.DecodeString(sum); err != nil || len(b) != sha256.Size {
			return "", fmt.Errorf("update: %s has a malformed sha256 for %s", ChecksumsAsset, name)
		}
		return sum, nil
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("update: read %s: %w", ChecksumsAsset, err)
	}
	return "", fmt.Errorf("update: %s has no entry for %s; nothing was installed", ChecksumsAsset, name)
}

// extractBinary returns BinaryName's contents from a tar.gz archive.
func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("update: open archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("update: archive has no %s binary", BinaryName)
		}
		if err != nil {
			return nil, fmt.Errorf("update: read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != BinaryName {
			continue
		}
		if hdr.Size <= 0 || hdr.Size > maxBinaryBytes {
			return nil, fmt.Errorf("update: archive's %s is %d bytes, outside (0, %d]", BinaryName, hdr.Size, maxBinaryBytes)
		}
		bin, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
		if err != nil {
			return nil, fmt.Errorf("update: extract %s: %w", BinaryName, err)
		}
		if int64(len(bin)) != hdr.Size {
			return nil, fmt.Errorf("update: extract %s: truncated", BinaryName)
		}
		return bin, nil
	}
}

// install writes bin next to target (same directory, so both renames
// stay on one filesystem and are atomic), moves target to backup and the
// new file to target. If the second rename fails, the old binary is put
// back.
func install(bin []byte, target, backup string) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".bunker-update-*")
	if err != nil {
		return fmt.Errorf("update: write new binary in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { os.Remove(tmpName) }
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("update: write new binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("update: write new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("update: write new binary: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("update: make new binary executable: %w", err)
	}
	if err := os.Rename(target, backup); err != nil {
		cleanup()
		return fmt.Errorf("update: keep current binary as %s: %w", backup, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		cleanup()
		if restoreErr := os.Rename(backup, target); restoreErr != nil {
			return fmt.Errorf("update: install new binary: %w; restoring %s also failed: %v", err, target, restoreErr)
		}
		return fmt.Errorf("update: install new binary: %w", err)
	}
	return nil
}
