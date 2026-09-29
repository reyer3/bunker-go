package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const releasePath = "/repos/" + Repo + "/releases/latest"

// releaseJSON is a trimmed GitHub API answer for tag, with assets
// served from base.
func releaseJSON(tag, base string) string {
	v := strings.TrimPrefix(tag, "v")
	return fmt.Sprintf(`{
  "tag_name": %q,
  "name": %q,
  "draft": false,
  "prerelease": false,
  "published_at": "2026-09-29T10:00:00Z",
  "assets": [
    {"name": "bunker_%[3]s_linux_amd64.tar.gz", "browser_download_url": "%[4]s/dl/bunker_%[3]s_linux_amd64.tar.gz", "size": 100},
    {"name": "bunker_%[3]s_linux_arm64.tar.gz", "browser_download_url": "%[4]s/dl/bunker_%[3]s_linux_arm64.tar.gz", "size": 90},
    {"name": "checksums.txt", "browser_download_url": "%[4]s/dl/checksums.txt", "size": 10}
  ]
}`, tag, tag, v, base)
}

func TestLatestParsesTagAndAssets(t *testing.T) {
	var gotPath, gotAccept, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAccept, gotUA = r.URL.Path, r.Header.Get("Accept"), r.Header.Get("User-Agent")
		fmt.Fprint(w, releaseJSON("v0.13.0", "https://example.invalid"))
	}))
	defer srv.Close()

	rel, err := Latest(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if gotPath != releasePath || gotAccept != "application/vnd.github+json" || gotUA == "" {
		t.Fatalf("request = %s accept=%q ua=%q", gotPath, gotAccept, gotUA)
	}
	if rel.Tag != "v0.13.0" || rel.Version != "0.13.0" || len(rel.Assets) != 3 {
		t.Fatalf("release = %+v", rel)
	}
	a, ok := rel.Asset(ArchiveName("0.13.0", "linux", "arm64"))
	if !ok || a.URL != "https://example.invalid/dl/bunker_0.13.0_linux_arm64.tar.gz" || a.Size != 90 {
		t.Fatalf("arm64 asset = %+v, %v", a, ok)
	}
	if !rel.Published.Equal(time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("published = %v", rel.Published)
	}
}

func TestLatestErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"not found", http.StatusNotFound, `{"message":"Not Found"}`, "404"},
		{"rate limited", http.StatusForbidden, `{}`, "403"},
		{"bad json", http.StatusOK, `{`, "decode"},
		{"bad tag", http.StatusOK, `{"tag_name":"nightly","assets":[]}`, "invalid version"},
		{"draft", http.StatusOK, `{"tag_name":"v1.0.0","draft":true,"assets":[]}`, "draft"},
		{"too large", http.StatusOK, `{"tag_name":"v1.0.0","body":"` + strings.Repeat("x", maxReleaseJSONBytes) + `"}`, "larger than"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			_, err := Latest(context.Background(), srv.Client(), srv.URL)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.HasPrefix(err.Error(), "update: ") {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestCheckerDisabledMakesNoRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, releaseJSON("v9.0.0", "https://example.invalid"))
	}))
	defer srv.Close()

	c := &Checker{Current: "0.12.0", Enabled: false, Client: srv.Client(), BaseURL: srv.URL, InitialDelay: time.Millisecond, Interval: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c.Run(ctx) // returns at once when disabled
	if err := c.Check(ctx); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("a disabled checker made %d requests", hits.Load())
	}
	if st := c.Status(); st.Available {
		t.Fatalf("status = %+v", st)
	}
	var nilChecker *Checker
	if st := nilChecker.Status(); st.Available {
		t.Fatalf("nil checker status = %+v", st)
	}
}

func TestCheckerReportsAndCachesNewerRelease(t *testing.T) {
	tag := "v0.13.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, releaseJSON(tag, "https://example.invalid"))
	}))
	defer srv.Close()
	cache := filepath.Join(t.TempDir(), CacheFile)

	c := &Checker{Current: "0.12.0", Enabled: true, Client: srv.Client(), BaseURL: srv.URL, CachePath: cache}
	if st := c.Status(); st.Available {
		t.Fatalf("before any check: %+v", st)
	}
	if err := c.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if st := c.Status(); !st.Available || st.Latest != "0.13.0" {
		t.Fatalf("status = %+v", st)
	}
	saved, err := LoadCache(cache)
	if err != nil || saved.Release.Version != "0.13.0" || saved.CheckedAt.IsZero() {
		t.Fatalf("cache = %+v, %v", saved, err)
	}

	// A restarted daemon knows from the cache before its first check.
	restarted := &Checker{Current: "0.12.0", Enabled: true, Client: srv.Client(), BaseURL: srv.URL, CachePath: cache, InitialDelay: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { restarted.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for !restarted.Status().Available && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if st := restarted.Status(); !st.Available || st.Latest != "0.13.0" {
		t.Fatalf("restarted status = %+v", st)
	}

	// Once the daemon runs that version, there is nothing to report.
	upToDate := &Checker{Current: "0.13.0", Enabled: true, Client: srv.Client(), BaseURL: srv.URL}
	if err := upToDate.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if st := upToDate.Status(); st.Available {
		t.Fatalf("up to date status = %+v", st)
	}
}

func TestCheckerKeepsLastResultOnNetworkError(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, releaseJSON("v0.13.0", "https://example.invalid"))
	}))
	defer srv.Close()
	c := &Checker{Current: "0.12.0", Enabled: true, Client: srv.Client(), BaseURL: srv.URL}
	if err := c.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	fail.Store(true)
	if err := c.Check(context.Background()); err == nil {
		t.Fatal("a 502 should be an error")
	}
	if st := c.Status(); !st.Available || st.Latest != "0.13.0" {
		t.Fatalf("status after a failed check = %+v", st)
	}
}

func TestCheckerRunChecksPeriodically(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, releaseJSON("v0.13.0", "https://example.invalid"))
	}))
	defer srv.Close()
	c := &Checker{Current: "0.12.0", Enabled: true, Client: srv.Client(), BaseURL: srv.URL, InitialDelay: time.Millisecond, Interval: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if hits.Load() < 2 {
		t.Fatalf("Run made %d checks, want at least 2", hits.Load())
	}
	if !c.Status().Available {
		t.Fatal("Run did not record the release")
	}
}

func TestStatusForIgnoresPrereleaseAndDev(t *testing.T) {
	if st := StatusFor("0.12.0", Release{Version: "0.13.0-rc.1", Prerelease: true}); st.Available {
		t.Fatalf("pre-release: %+v", st)
	}
	if st := StatusFor("dev", Release{Version: "0.13.0"}); st.Available {
		t.Fatalf("dev: %+v", st)
	}
	if st := StatusFor("0.12.0", Release{}); st.Available {
		t.Fatalf("no release: %+v", st)
	}
}
