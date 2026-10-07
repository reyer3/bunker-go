package core

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"sync"
	"time"

	_ "image/gif"  // register decoders: adapters may fetch a profile picture in
	_ "image/jpeg" // any of these common formats; Service.Avatar decodes
	_ "image/png"  // whatever comes back and re-encodes it as PNG.
)

// avatarTTL/avatarNegativeTTL bound how long Service.Avatar trusts a
// cached picture, or a cached "no picture" result, before it asks the
// adapter again. Both are 24h per the feature doc.
const (
	avatarTTL         = 24 * time.Hour
	avatarNegativeTTL = 24 * time.Hour
)

// avatarCacheCapBytes is the default total size Service.Avatar's on-disk
// cache is trimmed to (LRU eviction, see evictAvatarCacheIfNeeded).
const avatarCacheCapBytes = 50 << 20 // 50 MB

// defaultAvatarFetchInterval is the minimum time Service.Avatar waits
// between two real adapter fetches for the same (channel, account) —
// protecting WhatsApp/Matrix from being hammered with profile-picture
// requests when many conversations become visible in the TUI at once.
// When the interval has not elapsed, Service.Avatar never blocks waiting
// for it: it just skips the fetch for that call and serves whatever it
// already has (a stale cache entry, or the generated fallback).
const defaultAvatarFetchInterval = 250 * time.Millisecond

// AvatarResult is what Service.Avatar returns: a path to a cached PNG
// file (at most avatarSize x avatarSize, mode 0600 in a 0700 directory)
// the caller — the RPC layer, then the CLI/TUI — reads directly. Like
// DownloadResult, the daemon writes the file itself so avatar bytes never
// round-trip the RPC protocol.
type AvatarResult struct {
	Path string
	// Generated is true when Path is the deterministic brand-color/
	// initial fallback (see generateAvatarPNG), not a picture actually
	// fetched from the channel.
	Generated bool
}

// SetAvatarCacheDir configures where Service.Avatar caches thumbnails and
// generated fallbacks. It must be called before the first Avatar call;
// tests inject a t.TempDir() so they never touch a real ~/.cache.
// Production wiring (cmd/bunker/daemon.go) sets it to
// filepath.Join(config.CacheDir(), "avatars").
func (s *Service) SetAvatarCacheDir(dir string) { s.avatarCacheDir = dir }

// SetAvatarClock overrides the clock Service.Avatar uses for cache TTLs
// and rate limiting. Tests inject a fake so 24h TTLs and the fetch
// interval never require a real sleep.
func (s *Service) SetAvatarClock(now func() time.Time) { s.avatarClock = now }

// SetAvatarFetchInterval overrides defaultAvatarFetchInterval. Tests set
// it to 0 to disable rate limiting, or to a value they then advance a
// fake clock past, to assert the limiter opens again.
func (s *Service) SetAvatarFetchInterval(d time.Duration) {
	s.avatarLimiter.mu.Lock()
	defer s.avatarLimiter.mu.Unlock()
	s.avatarLimiter.interval = d
}

// SetAvatarCacheCapBytes overrides avatarCacheCapBytes. Tests use a small
// cap to exercise eviction without writing tens of megabytes of fixtures.
func (s *Service) SetAvatarCacheCapBytes(n int64) { s.avatarCacheCapBytes = n }

// avatarLimiter rate-limits Service.Avatar's real adapter fetches,
// independently per (channel, account): a burst of Avatar calls across
// many different conversations on the same account must not turn into a
// burst of profile-picture requests against that channel's servers.
type avatarLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     map[string]time.Time
}

func newAvatarLimiter(interval time.Duration) *avatarLimiter {
	return &avatarLimiter{interval: interval, next: make(map[string]time.Time)}
}

// allow reports whether a fetch for (channel, account) may proceed at
// now, and — when it may — immediately reserves the next interval so a
// concurrent or immediately following call is blocked without waiting.
// It never blocks the caller itself: a "no" is a normal, cheap outcome,
// not a wait.
func (l *avatarLimiter) allow(channel Channel, account string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := string(channel) + "\x00" + account
	if next, ok := l.next[key]; ok && now.Before(next) {
		return false
	}
	l.next[key] = now.Add(l.interval)
	return true
}

// avatarProviderFor returns the AvatarProvider capability for
// (channel, account) when one is registered and implements it, or nil —
// never an error: no adapter, or an adapter without the capability (every
// mail adapter, by design), both just mean "always generate the
// fallback", not a failure.
func (s *Service) avatarProviderFor(channel Channel, account string) AvatarProvider {
	adapter, ok := s.registry.Get(channel, account)
	if !ok {
		return nil
	}
	provider, ok := adapter.(AvatarProvider)
	if !ok {
		return nil
	}
	return provider
}

// avatarDisplayName picks the text Service.Avatar's generated fallback
// derives its initial letter from: an adapter-supplied name (from a
// picture fetch that itself failed to decode, for instance) takes
// priority; otherwise the stored item's ThreadName for this
// (channel, account, thread) conversation, when one is known; otherwise
// the raw thread id itself, so a fallback can always be generated.
func (s *Service) avatarDisplayName(ctx context.Context, channel Channel, account, thread, adapterName string) string {
	if adapterName != "" {
		return adapterName
	}
	items, err := s.store.List(ctx, Filter{Channel: channel, Account: account})
	if err == nil {
		for _, it := range items {
			if it.Thread == thread && it.ThreadName != "" {
				return it.ThreadName
			}
		}
	}
	return thread
}

// Avatar returns a local PNG path for (channel, account, thread)'s
// conversation avatar: a cached, resized-to-avatarSize picture actually
// fetched from the channel when one is available, or the deterministic
// brand-color/initial fallback otherwise. It never returns an error for
// "no picture" or a fetch failure — those always resolve to the
// generated fallback — only for a misconfigured cache directory.
//
// Fetches are on demand, rate-limited per adapter (avatarLimiter) and
// cached with a TTL (avatarTTL) so a busy TUI polling many visible
// conversations never turns into a fetch storm; a "no picture"/"not
// authorized" outcome is itself cached (avatarNegativeTTL) so it is not
// retried every call. Mail never implements AvatarProvider, so it always
// takes the no-capability path below and never touches the network.
func (s *Service) Avatar(ctx context.Context, channel Channel, account, thread string) (AvatarResult, error) {
	if s.avatarCacheDir == "" {
		return AvatarResult{}, fmt.Errorf("core: avatar cache dir not configured: %w", ErrUnsupported)
	}
	key := avatarCacheKey(channel, account, thread)
	now := s.avatarClock()

	if meta, ok := s.readAvatarMeta(key); ok && meta.fresh(now) {
		s.touchAvatarCache(key, now)
		return AvatarResult{Path: s.avatarPNGPath(key), Generated: meta.Generated}, nil
	}

	provider := s.avatarProviderFor(channel, account)
	if provider == nil {
		name := s.avatarDisplayName(ctx, channel, account, thread, "")
		return s.fallbackAvatar(key, channel, name, now, false)
	}

	if !s.avatarLimiter.allow(channel, account, now) {
		if meta, ok := s.readAvatarMeta(key); ok {
			s.touchAvatarCache(key, now)
			return AvatarResult{Path: s.avatarPNGPath(key), Generated: meta.Generated}, nil
		}
		name := s.avatarDisplayName(ctx, channel, account, thread, "")
		// Rate-limited, not a real answer: cached with a zero FetchedAt so
		// the very next call (once allowed) tries the provider again
		// instead of trusting this placeholder for a full TTL.
		return s.fallbackAvatar(key, channel, name, time.Time{}, false)
	}

	src, ok, err := provider.Avatar(ctx, thread)
	if err != nil {
		// Transient fetch failure: fall back, but never negative-cache it
		// (zero FetchedAt), so the next call retries.
		name := s.avatarDisplayName(ctx, channel, account, thread, src.DisplayName)
		return s.fallbackAvatar(key, channel, name, time.Time{}, false)
	}
	if !ok {
		// "No picture" / "not authorized": a real, stable answer — cache it
		// as negative for avatarNegativeTTL.
		name := s.avatarDisplayName(ctx, channel, account, thread, src.DisplayName)
		return s.fallbackAvatar(key, channel, name, now, true)
	}

	png, decodeErr := resizeToAvatarPNG(src.Data)
	if decodeErr != nil {
		name := s.avatarDisplayName(ctx, channel, account, thread, src.DisplayName)
		return s.fallbackAvatar(key, channel, name, time.Time{}, false)
	}
	if err := s.writeAvatarCacheEntry(key, png, avatarMeta{FetchedAt: now, Negative: false, Generated: false}); err != nil {
		return AvatarResult{}, err
	}
	s.evictAvatarCacheIfNeeded()
	return AvatarResult{Path: s.avatarPNGPath(key), Generated: false}, nil
}

// MaxAvatarDataBytes caps the PNG AvatarData returns. Avatars are
// resized to avatarSize pixels, so a real one is a few KB; the cap only
// bounds a corrupted cache entry that would otherwise travel inline.
const MaxAvatarDataBytes = 1 << 20

// AvatarData is Avatar plus the PNG's bytes, for a client on another
// machine that cannot open the daemon's cache path and keeps its own
// copy instead.
func (s *Service) AvatarData(ctx context.Context, channel Channel, account, thread string) (AvatarResult, []byte, error) {
	res, err := s.Avatar(ctx, channel, account, thread)
	if err != nil {
		return AvatarResult{}, nil, err
	}
	f, err := os.Open(res.Path)
	if err != nil {
		return AvatarResult{}, nil, fmt.Errorf("core: open avatar: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxAvatarDataBytes+1))
	if err != nil {
		return AvatarResult{}, nil, fmt.Errorf("core: read avatar: %w", err)
	}
	if len(data) > MaxAvatarDataBytes {
		return AvatarResult{}, nil, fmt.Errorf("core: avatar %s exceeds %d bytes: %w", res.Path, MaxAvatarDataBytes, ErrAttachmentTooLarge)
	}
	return res, data, nil
}

// fallbackAvatar renders and caches the generated brand-color/initial
// avatar for key, records whether that cache entry is a real negative
// (no picture, trust it for avatarNegativeTTL) or just a placeholder
// (zero fetchedAt, always stale), and returns it.
func (s *Service) fallbackAvatar(key string, channel Channel, displayName string, fetchedAt time.Time, negative bool) (AvatarResult, error) {
	data := generateAvatarPNG(channel, displayName)
	meta := avatarMeta{FetchedAt: fetchedAt, Negative: negative, Generated: true}
	if err := s.writeAvatarCacheEntry(key, data, meta); err != nil {
		return AvatarResult{}, err
	}
	if negative {
		s.evictAvatarCacheIfNeeded()
	}
	return AvatarResult{Path: s.avatarPNGPath(key), Generated: true}, nil
}

// resizeToAvatarPNG decodes data (any format image/png, image/jpeg or
// image/gif registers a decoder for — WhatsApp and Matrix profile
// pictures are always one of the first two), downsamples it to fit
// within avatarSize x avatarSize when it is larger, and re-encodes it as
// PNG. It never upsamples a smaller source image.
func resizeToAvatarPNG(data []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("core: decode avatar picture: %w", err)
	}

	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= avatarSize && h <= avatarSize {
		var buf bytes.Buffer
		if err := png.Encode(&buf, src); err != nil {
			return nil, fmt.Errorf("core: encode avatar picture: %w", err)
		}
		return buf.Bytes(), nil
	}

	dst := nearestNeighborResize(src, avatarSize, avatarSize)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, fmt.Errorf("core: encode resized avatar picture: %w", err)
	}
	return buf.Bytes(), nil
}
