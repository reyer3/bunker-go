package core_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// spyAvatarAdapter implements core.AvatarProvider on top of spyAdapter's
// mandatory Adapter capability, so Service.Avatar tests never need a real
// WhatsApp/Matrix connection.
type spyAvatarAdapter struct {
	spyAdapter

	// source/ok/err are what the next Avatar call returns; errAfterOK, when
	// set, makes ok/err apply only from that call number onward, so a test
	// can flip behavior mid-sequence.
	source core.AvatarSource
	ok     bool
	err    error

	calls      int
	lastThread string
}

func (s *spyAvatarAdapter) Avatar(_ context.Context, thread string) (core.AvatarSource, bool, error) {
	s.calls++
	s.lastThread = thread
	return s.source, s.ok, s.err
}

var _ core.AvatarProvider = (*spyAvatarAdapter)(nil)

// tinyPNG is a minimal valid 2x2 PNG, standing in for a "fetched profile
// picture" in tests: real size/shape do not matter to Service.Avatar,
// only that it is a decodable image Service can resize and re-encode.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode tiny PNG: %v", err)
	}
	return buf.Bytes()
}

func newAvatarService(t *testing.T, store core.Store, reg *core.Registry) *core.Service {
	t.Helper()
	svc := core.NewService(store, reg)
	svc.SetAvatarCacheDir(t.TempDir())
	return svc
}

func requirePrivateFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return // POSIX permission bits are not meaningful on Windows.
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != want {
		t.Errorf("mode of %s = %v, want %v", path, perm, want)
	}
}

func TestServiceAvatarGeneratesFallbackWithNoAvatarProviderCapability(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	reg.Register(bareAdapter{channel: core.ChannelMail, account: "cl"})
	svc := newAvatarService(t, store, reg)

	res, err := svc.Avatar(context.Background(), core.ChannelMail, "cl", "thread-1")
	if err != nil {
		t.Fatalf("Avatar() error = %v", err)
	}
	if !res.Generated {
		t.Errorf("Generated = false, want true (mail never fetches a picture)")
	}
	if res.Path == "" {
		t.Fatal("Path is empty")
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", res.Path, err)
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Errorf("cached avatar at %s is not a valid PNG: %v", res.Path, err)
	}

	requirePrivateFileMode(t, res.Path, 0o600)
	requirePrivateFileMode(t, filepath.Dir(res.Path), 0o700)
}

func TestServiceAvatarWithNoRegisteredAdapterAlsoFallsBack(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	svc := newAvatarService(t, store, reg)

	res, err := svc.Avatar(context.Background(), core.ChannelWhatsApp, "personal", "5511@s.whatsapp.net")
	if err != nil {
		t.Fatalf("Avatar() error = %v", err)
	}
	if !res.Generated {
		t.Error("Generated = false, want true (no adapter registered)")
	}
}

func TestServiceAvatarFetchesAndCachesAdapterPictureThenServesFreshCacheWithoutRefetching(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	adapter := &spyAvatarAdapter{
		spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"},
		source:     core.AvatarSource{Data: tinyPNG(t), DisplayName: "Widget Team"},
		ok:         true,
	}
	reg.Register(adapter)
	svc := newAvatarService(t, store, reg)

	res, err := svc.Avatar(context.Background(), core.ChannelWhatsApp, "personal", "group-1")
	if err != nil {
		t.Fatalf("Avatar() error = %v", err)
	}
	if res.Generated {
		t.Error("Generated = true, want false (adapter returned a real picture)")
	}
	if adapter.calls != 1 {
		t.Fatalf("provider called %d times, want 1", adapter.calls)
	}
	if adapter.lastThread != "group-1" {
		t.Errorf("provider thread = %q, want %q", adapter.lastThread, "group-1")
	}

	// A second call, same (fake) instant, must be a fresh cache hit: no
	// second provider call, same path.
	res2, err := svc.Avatar(context.Background(), core.ChannelWhatsApp, "personal", "group-1")
	if err != nil {
		t.Fatalf("Avatar() second call error = %v", err)
	}
	if res2.Path != res.Path || res2.Generated {
		t.Errorf("second call = %+v, want identical fresh-cache hit to %+v", res2, res)
	}
	if adapter.calls != 1 {
		t.Errorf("provider called %d times after a fresh cache hit, want still 1", adapter.calls)
	}
}

func TestServiceAvatarNegativeCachesNoPictureAndFallsBackToGenerated(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	adapter := &spyAvatarAdapter{
		spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"},
		ok:         false, // whatsmeow.ErrProfilePictureNotSet / ErrProfilePictureUnauthorized map to this.
		err:        nil,
	}
	reg.Register(adapter)
	svc := newAvatarService(t, store, reg)

	res, err := svc.Avatar(context.Background(), core.ChannelWhatsApp, "personal", "5511@s.whatsapp.net")
	if err != nil {
		t.Fatalf("Avatar() error = %v", err)
	}
	if !res.Generated {
		t.Error("Generated = false, want true (adapter reported no picture)")
	}
	if adapter.calls != 1 {
		t.Fatalf("provider called %d times, want 1", adapter.calls)
	}

	// Negative-cache hit: a second call at the same instant must not call
	// the provider again.
	if _, err := svc.Avatar(context.Background(), core.ChannelWhatsApp, "personal", "5511@s.whatsapp.net"); err != nil {
		t.Fatalf("Avatar() second call error = %v", err)
	}
	if adapter.calls != 1 {
		t.Errorf("provider called %d times after a negative-cache hit, want still 1", adapter.calls)
	}
}

func TestServiceAvatarTransientProviderErrorFallsBackWithoutNegativeCaching(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	adapter := &spyAvatarAdapter{
		spyAdapter: spyAdapter{channel: core.ChannelMatrix, account: "work"},
		err:        context.DeadlineExceeded,
	}
	reg.Register(adapter)
	svc := newAvatarService(t, store, reg)

	clock := time.Unix(1_700_000_000, 0)
	svc.SetAvatarClock(func() time.Time { return clock })
	// A high fetch rate so the rate limiter never masks the retry this
	// test cares about.
	svc.SetAvatarFetchInterval(0)

	res, err := svc.Avatar(context.Background(), core.ChannelMatrix, "work", "!room:example.com")
	if err != nil {
		t.Fatalf("Avatar() error = %v", err)
	}
	if !res.Generated {
		t.Error("Generated = false, want true (transient fetch error falls back)")
	}
	if adapter.calls != 1 {
		t.Fatalf("provider called %d times, want 1", adapter.calls)
	}

	// A transient error must NOT be negative-cached: the very next call
	// (even at the same instant) retries the provider.
	if _, err := svc.Avatar(context.Background(), core.ChannelMatrix, "work", "!room:example.com"); err != nil {
		t.Fatalf("Avatar() second call error = %v", err)
	}
	if adapter.calls != 2 {
		t.Errorf("provider called %d times after a transient error, want 2 (must retry, not negative-cache)", adapter.calls)
	}
}

func TestServiceAvatarRateLimitsFetchesPerAdapter(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	adapter := &spyAvatarAdapter{
		spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"},
		source:     core.AvatarSource{Data: tinyPNG(t)},
		ok:         true,
	}
	reg.Register(adapter)
	svc := newAvatarService(t, store, reg)

	clock := time.Unix(1_700_000_000, 0)
	svc.SetAvatarClock(func() time.Time { return clock })
	svc.SetAvatarFetchInterval(time.Second)

	// Two different (uncached) threads, same instant: the second must be
	// rate-limited and fall back to generated instead of calling the
	// provider again.
	res1, err := svc.Avatar(context.Background(), core.ChannelWhatsApp, "personal", "thread-a")
	if err != nil {
		t.Fatalf("Avatar() thread-a error = %v", err)
	}
	res2, err := svc.Avatar(context.Background(), core.ChannelWhatsApp, "personal", "thread-b")
	if err != nil {
		t.Fatalf("Avatar() thread-b error = %v", err)
	}
	if res1.Generated {
		t.Error("thread-a Generated = true, want false (the allowed fetch)")
	}
	if !res2.Generated {
		t.Error("thread-b Generated = false, want true (rate-limited, falls back)")
	}
	if adapter.calls != 1 {
		t.Fatalf("provider called %d times, want 1 (rate limit must block the second fetch)", adapter.calls)
	}

	// Advancing the clock past the interval opens the limiter again.
	clock = clock.Add(2 * time.Second)
	if _, err := svc.Avatar(context.Background(), core.ChannelWhatsApp, "personal", "thread-b"); err != nil {
		t.Fatalf("Avatar() thread-b retry error = %v", err)
	}
	if adapter.calls != 2 {
		t.Errorf("provider called %d times after the rate limit window passed, want 2", adapter.calls)
	}
}

func TestServiceAvatarUsesStoredThreadNameForTheGeneratedInitial(t *testing.T) {
	item := core.Item{
		ID:         "matrix:work:!room:example.com:$evt1",
		Channel:    core.ChannelMatrix,
		Account:    "work",
		Thread:     "!room:example.com",
		ThreadName: "Zephyr Team",
	}
	store := newMemStore(item)
	reg := core.NewRegistry()
	reg.Register(bareAdapter{channel: core.ChannelMatrix, account: "work"})
	svc := newAvatarService(t, store, reg)

	res, err := svc.Avatar(context.Background(), core.ChannelMatrix, "work", "!room:example.com")
	if err != nil {
		t.Fatalf("Avatar() error = %v", err)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", res.Path, err)
	}
	want := core.GenerateAvatarPNGForTest(core.ChannelMatrix, "Zephyr Team")
	if !bytes.Equal(got, want) {
		t.Error("generated avatar does not match the stored item's ThreadName-derived initial")
	}
}

func TestServiceAvatarEvictsLeastRecentlyUsedEntriesOverCap(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	adapter := &spyAvatarAdapter{
		spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"},
		source:     core.AvatarSource{Data: tinyPNG(t)},
		ok:         true,
	}
	reg.Register(adapter)
	svc := newAvatarService(t, store, reg)
	svc.SetAvatarCacheCapBytes(3000) // small cap, easy to exceed with a handful of real (thumbnail-sized) entries.
	svc.SetAvatarFetchInterval(0)

	clock := time.Unix(1_700_000_000, 0)
	svc.SetAvatarClock(func() time.Time { return clock })

	first, err := svc.Avatar(context.Background(), core.ChannelWhatsApp, "personal", "thread-1")
	if err != nil {
		t.Fatalf("Avatar() thread-1 error = %v", err)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("thread-1 cache file missing right after write: %v", err)
	}

	// Write enough further distinct entries, each instant later so mtimes
	// strictly order oldest-to-newest, to push the cache over its cap.
	for i := 0; i < 30; i++ {
		clock = clock.Add(time.Second)
		if _, err := svc.Avatar(context.Background(), core.ChannelWhatsApp, "personal", threadName(i)); err != nil {
			t.Fatalf("Avatar() thread-%d error = %v", i, err)
		}
	}

	if _, err := os.Stat(first.Path); err == nil {
		t.Error("thread-1's cache file still exists after the cap was exceeded, want it evicted (least recently used)")
	} else if !os.IsNotExist(err) {
		t.Fatalf("Stat(%s): unexpected error %v", first.Path, err)
	}

	var total int64
	entries, err := os.ReadDir(filepath.Dir(first.Path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	if total > 3000 {
		t.Errorf("total cache size = %d bytes, want <= 3000 after eviction", total)
	}
}

func threadName(i int) string {
	return fmt.Sprintf("thread-evict-%d", i)
}
