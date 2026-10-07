package rpc

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/secfile"
)

// remoteEnv forces the remote decision: "1" (or any strconv.ParseBool
// true) for a client on another machine, "0" for one on the daemon's
// machine. Unset, Client probes the daemon (see isRemote).
const remoteEnv = "BUNKER_REMOTE"

// releaseTimeout bounds the download_close sent after a transfer, which
// runs even when the caller's context is already canceled.
const releaseTimeout = 10 * time.Second

// isRemote reports whether the daemon runs on another machine (its socket
// forwarded here, e.g. over SSH), where it cannot read or write this
// machine's paths. The answer is decided once, on the first call that
// touches files, and cached: BUNKER_REMOTE wins when set; otherwise the
// client writes a random nonce to a temp file and asks the daemon
// (fs_probe) whether it reads the same bytes at that path. A daemon too
// old to know fs_probe is treated as local, which is exactly what every
// client assumed before; any other probe failure is returned, never
// guessed at, and the next call probes again.
func (c *Client) isRemote(ctx context.Context) (bool, error) {
	c.remoteMu.Lock()
	defer c.remoteMu.Unlock()
	if c.remoteKnown {
		return c.remote, nil
	}
	if v := os.Getenv(remoteEnv); v != "" {
		forced, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("rpc: %s=%q: want 1 or 0", remoteEnv, v)
		}
		c.remote, c.remoteKnown = forced, true
		return forced, nil
	}
	readable, err := c.probeSharedFS(ctx)
	if err != nil {
		return false, err
	}
	c.remote, c.remoteKnown = !readable, true
	return c.remote, nil
}

// probeSharedFS runs the fs_probe handshake isRemote describes.
func (c *Client) probeSharedFS(ctx context.Context) (bool, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return false, fmt.Errorf("rpc: probe nonce: %w", err)
	}
	f, err := os.CreateTemp("", "bunker-probe-*")
	if err != nil {
		return false, fmt.Errorf("rpc: probe file: %w", err)
	}
	path := f.Name()
	defer os.Remove(path)
	_, werr := f.WriteString(hex.EncodeToString(nonce))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return false, fmt.Errorf("rpc: write probe file: %w", werr)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("rpc: probe file path: %w", err)
	}

	var res fsProbeResult
	err = c.call(ctx, MethodFSProbe, fsProbeParams{Path: abs, Nonce: hex.EncodeToString(nonce)}, &res)
	if err != nil {
		// An older daemon answers `rpc: unknown method "fs_probe"` (see
		// Server.call) with no error code to match on.
		if strings.Contains(err.Error(), "unknown method") {
			return true, nil
		}
		return false, fmt.Errorf("rpc: detect remote daemon: %w", err)
	}
	return res.Readable, nil
}

// downloadRemote is Download for a daemon on another machine: the daemon
// stages the attachment (download_open), the client pulls it in
// core.MaxStagedChunk pieces (download_chunk) straight into
// core.WriteAttachmentFile at destPath on this machine — so Force, the
// size cap, 0600 and the declared-size check apply here exactly as they
// do on the daemon — and the token is always released (download_close).
// The sha256 the daemon computed while staging is checked before the
// file is renamed into place, so a corrupted transfer leaves nothing at
// destPath.
func (c *Client) downloadRemote(ctx context.Context, id string, index int, destPath string, opts core.DownloadOptions) (res core.DownloadResult, err error) {
	if !opts.Force {
		// Fail before the daemon fetches and stages anything.
		if _, statErr := os.Stat(destPath); statErr == nil {
			return core.DownloadResult{}, fmt.Errorf("rpc: download %s: %s: %w", id, destPath, core.ErrDestinationExists)
		}
	}
	var open downloadOpenResult
	if err := c.call(ctx, MethodDownloadOpen, downloadOpenParams{ID: id, Index: index, MaxBytes: opts.MaxBytes}, &open); err != nil {
		return core.DownloadResult{}, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
		defer cancel()
		closeErr := c.call(closeCtx, MethodDownloadClose, downloadCloseParams{Token: open.Token}, nil)
		if err == nil && closeErr != nil {
			err = fmt.Errorf("rpc: release download %s: %w", id, closeErr)
		}
	}()

	r := &stagedReader{ctx: ctx, c: c, token: open.Token, size: open.Size, want: open.SHA256, h: sha256.New()}
	n, err := core.WriteAttachmentFile(destPath, r, open.Size, opts)
	if err != nil {
		return core.DownloadResult{}, fmt.Errorf("rpc: download %s: %w", id, err)
	}
	return core.DownloadResult{Path: destPath, Bytes: n, Name: open.Name, MIME: open.MIME}, nil
}

// stagedReader reads a staged download chunk by chunk. At the end of the
// file it compares the bytes' sha256 with the daemon's and reports a
// mismatch as a read error, which makes WriteAttachmentFile discard its
// temp file instead of renaming it into place.
type stagedReader struct {
	ctx   context.Context
	c     *Client
	token string
	size  int64
	off   int64
	buf   []byte
	h     hash.Hash
	want  string
}

func (r *stagedReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		if r.off >= r.size {
			if got := hex.EncodeToString(r.h.Sum(nil)); got != r.want {
				return 0, fmt.Errorf("rpc: downloaded bytes sha256 %s, daemon staged %s", got, r.want)
			}
			return 0, io.EOF
		}
		length := min(int64(core.MaxStagedChunk), r.size-r.off)
		var res downloadChunkResult
		if err := r.c.call(r.ctx, MethodDownloadChunk, downloadChunkParams{Token: r.token, Offset: r.off, Length: length}, &res); err != nil {
			return 0, err
		}
		if len(res.Data) == 0 || int64(len(res.Data)) > length {
			return 0, fmt.Errorf("rpc: download chunk at %d: got %d bytes, asked %d", r.off, len(res.Data), length)
		}
		r.h.Write(res.Data)
		r.off += int64(len(res.Data))
		r.buf = res.Data
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// avatarRemote is Avatar for a daemon on another machine: the daemon's
// cache path means nothing here, so the PNG's bytes come back inline
// (avatar_data) and are kept in this machine's own cache, named like the
// daemon's entry, and that local path is what the caller gets.
func (c *Client) avatarRemote(ctx context.Context, channel core.Channel, account, thread string) (core.AvatarResult, error) {
	var res avatarDataResult
	if err := c.call(ctx, MethodAvatarData, avatarParams{Channel: channel, Account: account, Thread: thread}, &res); err != nil {
		return core.AvatarResult{}, err
	}
	dir := c.avatarDir
	if dir == "" {
		// A sibling of the local daemon's avatar cache, never inside it:
		// that cache is LRU-evicted by its own daemon.
		dir = filepath.Join(config.CacheDir(), "remote-avatars")
	}
	if err := secfile.EnsureDir(dir); err != nil {
		return core.AvatarResult{}, fmt.Errorf("rpc: create avatar cache dir: %w", err)
	}
	path := filepath.Join(dir, core.AvatarCacheKey(channel, account, thread)+".png")
	opts := core.DownloadOptions{Force: true, MaxBytes: core.MaxAvatarDataBytes}
	if _, err := core.WriteAttachmentFile(path, bytes.NewReader(res.Data), int64(len(res.Data)), opts); err != nil {
		return core.AvatarResult{}, fmt.Errorf("rpc: write avatar: %w", err)
	}
	return core.AvatarResult{Path: path, Generated: res.Generated}, nil
}

// errRemoteFiles is what a remote client's send carrying a file path
// fails with until upload support lands.
var errRemoteFiles = fmt.Errorf("rpc: sending files from a remote client is not supported yet (the daemon cannot read this machine's files): %w", core.ErrUnsupported)

// refuseRemoteFiles fails a send that carries this machine's file paths
// (or a voice note, which always does) when the daemon runs on another
// machine: the daemon would open those paths on its own filesystem, and
// either fail obscurely or, if the same path exists there, send the
// wrong file. It runs before any call, dry runs included, and decides
// remoteness only when there is a file at stake, so a text-only send
// never pays for (or fails on) the probe.
func (c *Client) refuseRemoteFiles(ctx context.Context, hasFiles bool) error {
	if !hasFiles {
		return nil
	}
	remote, err := c.isRemote(ctx)
	if err != nil {
		return err
	}
	if remote {
		return errRemoteFiles
	}
	return nil
}
