package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/secfile"
)

// avatarMeta is the sidecar JSON Service.Avatar keeps next to each cached
// PNG, recording when it was fetched and whether it is a negative result
// (no picture) or a generated fallback.
type avatarMeta struct {
	FetchedAt time.Time `json:"fetchedAt"`
	Negative  bool      `json:"negative"`
	Generated bool      `json:"generated"`
}

// fresh reports whether m is still within its TTL as of now. A zero
// FetchedAt (used for a rate-limited or transient-error placeholder,
// never negative-cached on purpose) is always stale.
func (m avatarMeta) fresh(now time.Time) bool {
	if m.FetchedAt.IsZero() {
		return false
	}
	ttl := avatarTTL
	if m.Negative {
		ttl = avatarNegativeTTL
	}
	return now.Sub(m.FetchedAt) < ttl
}

// avatarCacheKey derives the filename-safe cache key for one
// conversation's avatar: a plain sha256 hex digest, since channel/
// account/thread values (a WhatsApp JID, a Matrix room id) may contain
// characters unsafe in a filename.
func avatarCacheKey(channel Channel, account, thread string) string {
	sum := sha256.Sum256([]byte(string(channel) + "\x00" + account + "\x00" + thread))
	return hex.EncodeToString(sum[:])
}

// AvatarCacheKey is avatarCacheKey for callers outside core: a remote
// RPC client names its local copy of an avatar the same way the daemon
// names its cache entry.
func AvatarCacheKey(channel Channel, account, thread string) string {
	return avatarCacheKey(channel, account, thread)
}

func (s *Service) avatarPNGPath(key string) string {
	return filepath.Join(s.avatarCacheDir, key+".png")
}

func (s *Service) avatarMetaPath(key string) string {
	return filepath.Join(s.avatarCacheDir, key+".json")
}

// readAvatarMeta reads key's sidecar metadata. It reports ok=false when
// either file is missing, unreadable or malformed — including a meta file
// whose PNG has vanished — so a caller never trusts a half-present entry.
func (s *Service) readAvatarMeta(key string) (avatarMeta, bool) {
	data, err := os.ReadFile(s.avatarMetaPath(key))
	if err != nil {
		return avatarMeta{}, false
	}
	var m avatarMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return avatarMeta{}, false
	}
	if _, err := os.Stat(s.avatarPNGPath(key)); err != nil {
		return avatarMeta{}, false
	}
	return m, true
}

// writeAvatarCacheEntry persists png and meta for key, creating the cache
// directory (0700) on first use. Both files are written at 0600 — the
// cache directory holds no secrets by itself, but every bunker-go state
// directory stays private (internal/secfile) rather than trusting the
// process's umask.
func (s *Service) writeAvatarCacheEntry(key string, png []byte, meta avatarMeta) error {
	if err := secfile.EnsureDir(s.avatarCacheDir); err != nil {
		return fmt.Errorf("core: create avatar cache dir: %w", err)
	}
	if err := writePrivateFile(s.avatarPNGPath(key), png); err != nil {
		return fmt.Errorf("core: write avatar png: %w", err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("core: marshal avatar meta: %w", err)
	}
	if err := writePrivateFile(s.avatarMetaPath(key), data); err != nil {
		return fmt.Errorf("core: write avatar meta: %w", err)
	}
	return nil
}

// writePrivateFile writes data to path at mode 0600, tightening it after
// creation too (os.WriteFile's mode is subject to the process umask).
func writePrivateFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// touchAvatarCache updates key's on-disk recency (mtime) to now, so
// evictAvatarCacheIfNeeded's LRU ordering treats a just-served entry as
// freshly used. A failure here is not fatal to serving the avatar — it
// just makes this entry a slightly more likely eviction candidate next
// time than it ideally should be.
func (s *Service) touchAvatarCache(key string, now time.Time) {
	_ = os.Chtimes(s.avatarPNGPath(key), now, now)
	_ = os.Chtimes(s.avatarMetaPath(key), now, now)
}

// evictAvatarCacheIfNeeded enforces s.avatarCacheCapBytes across the
// whole avatar cache directory, evicting whole entries (both the .png and
// its .json sidecar) oldest-mtime-first until the total fits. mtime is
// the LRU signal: writeAvatarCacheEntry sets a fresh one, and every
// cache-hit read calls touchAvatarCache.
func (s *Service) evictAvatarCacheIfNeeded() {
	entries, err := os.ReadDir(s.avatarCacheDir)
	if err != nil {
		return
	}

	type cacheEntry struct {
		key     string
		size    int64
		modTime time.Time
		files   []string
	}
	byKey := make(map[string]*cacheEntry)
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		key := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		ce, ok := byKey[key]
		if !ok {
			ce = &cacheEntry{key: key}
			byKey[key] = ce
		}
		ce.size += info.Size()
		ce.files = append(ce.files, filepath.Join(s.avatarCacheDir, e.Name()))
		if info.ModTime().After(ce.modTime) {
			ce.modTime = info.ModTime()
		}
		total += info.Size()
	}

	capBytes := s.avatarCacheCapBytes
	if capBytes <= 0 {
		capBytes = avatarCacheCapBytes
	}
	if total <= capBytes {
		return
	}

	all := make([]*cacheEntry, 0, len(byKey))
	for _, ce := range byKey {
		all = append(all, ce)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].modTime.Before(all[j].modTime) })

	for _, ce := range all {
		if total <= capBytes {
			break
		}
		for _, f := range ce.files {
			_ = os.Remove(f)
		}
		total -= ce.size
	}
}
