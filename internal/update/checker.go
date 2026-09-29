package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

const (
	// InitialCheckDelay is how long after the daemon starts it first
	// looks for a release: long enough to stay out of the way of the
	// adapters connecting, short enough that a restart shows news soon.
	InitialCheckDelay = 2 * time.Minute
	// CheckInterval is how often the daemon looks again after that.
	CheckInterval = 24 * time.Hour
	// checkTimeout bounds one request to the API.
	checkTimeout = 30 * time.Second
	// CacheFile is the last release seen, under the state dir, so a
	// restarted daemon (and "bunker update --dry-run", which never
	// touches the network) know about it before the next check.
	CacheFile = "update.json"
)

// Cache is what CacheFile holds.
type Cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Release   Release   `json:"release"`
}

// LoadCache reads the cached release at path.
func LoadCache(path string) (Cache, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Cache{}, fmt.Errorf("update: read cache: %w", err)
	}
	var c Cache
	if err := json.Unmarshal(b, &c); err != nil {
		return Cache{}, fmt.Errorf("update: decode cache %s: %w", path, err)
	}
	return c, nil
}

// saveCache writes c to path through a temp file and a rename, so a
// reader never sees half of it.
func saveCache(path string, c Cache) error {
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("update: encode cache: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-*.json")
	if err != nil {
		return fmt.Errorf("update: write cache: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("update: write cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("update: write cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("update: write cache: %w", err)
	}
	return nil
}

// Checker is the daemon's periodic release check. Its Status is what
// the health RPC reports; a failed check keeps the previous result.
type Checker struct {
	// Current is the running daemon's version.
	Current string
	// Enabled is false for "[update] check = false" and for a build
	// that is not a release: Run then does nothing and Status never
	// reports an update.
	Enabled bool
	Client  *http.Client
	BaseURL string
	// CachePath is where the last release is kept; empty keeps it in
	// memory only.
	CachePath string
	Logger    *slog.Logger

	// InitialDelay and Interval default to InitialCheckDelay and
	// CheckInterval; tests shorten them.
	InitialDelay time.Duration
	Interval     time.Duration

	mu     sync.Mutex
	latest Release
}

// Status is the cached answer: whether the latest release seen is newer
// than Current.
func (c *Checker) Status() core.UpdateStatus {
	if c == nil || !c.Enabled {
		return core.UpdateStatus{}
	}
	c.mu.Lock()
	rel := c.latest
	c.mu.Unlock()
	return StatusFor(c.Current, rel)
}

// StatusFor compares current against rel: an unparsable version on
// either side, or no release at all, is simply "no update".
func StatusFor(current string, rel Release) core.UpdateStatus {
	if rel.Version == "" || rel.Prerelease {
		return core.UpdateStatus{}
	}
	newer, err := Newer(rel.Version, current)
	if err != nil || !newer {
		return core.UpdateStatus{}
	}
	return core.UpdateStatus{Available: true, Latest: rel.Version}
}

// Run loads the cache, then checks after InitialDelay and every
// Interval until ctx ends. Network and cache errors are logged at debug
// level and never stop it: the update check must not be why a daemon
// misbehaves.
func (c *Checker) Run(ctx context.Context) {
	if !c.Enabled {
		return
	}
	if c.CachePath != "" {
		if cache, err := LoadCache(c.CachePath); err == nil {
			c.set(cache.Release)
		} else if !errors.Is(err, fs.ErrNotExist) {
			c.logger().Debug("update cache unreadable", "error", err)
		}
	}
	delay, interval := c.InitialDelay, c.Interval
	if delay <= 0 {
		delay = InitialCheckDelay
	}
	if interval <= 0 {
		interval = CheckInterval
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := c.Check(ctx); err != nil {
			c.logger().Debug("update check failed", "error", err)
		}
		timer.Reset(interval)
	}
}

// Check asks for the latest release once and caches it.
func (c *Checker) Check(ctx context.Context) error {
	if !c.Enabled {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	rel, err := Latest(ctx, c.Client, c.BaseURL)
	if err != nil {
		return err
	}
	c.set(rel)
	if c.CachePath != "" {
		if err := saveCache(c.CachePath, Cache{CheckedAt: time.Now().UTC(), Release: rel}); err != nil {
			return err
		}
	}
	if st := c.Status(); st.Available {
		c.logger().Info("new bunker release available", "current", c.Current, "latest", st.Latest)
	}
	return nil
}

func (c *Checker) set(rel Release) {
	c.mu.Lock()
	c.latest = rel
	c.mu.Unlock()
}

func (c *Checker) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}
