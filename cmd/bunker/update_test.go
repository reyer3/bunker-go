package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reyer3/bunker-go/internal/update"
)

const newBinary = "#!/bin/sh\necho new bunker\n"

// fakeRelease is a GitHub stand-in: the releases API and the download
// URLs it points at, all on one httptest server.
type fakeRelease struct {
	t        *testing.T
	srv      *httptest.Server
	tag      string
	archive  []byte
	sums     string
	omitArch bool

	mu    sync.Mutex
	paths []string
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newFakeRelease(t *testing.T, tag string) *fakeRelease {
	f := &fakeRelease{t: t, tag: tag}
	f.archive = tarGz(t, map[string]string{"bunker": newBinary, "LICENSE": "license text\n"})
	sum := sha256.Sum256(f.archive)
	f.sums = fmt.Sprintf("%s  %s\n%s  bunker_%s_linux_arm64.tar.gz\n", hex.EncodeToString(sum[:]), f.assetName(), strings.Repeat("0", 64), strings.TrimPrefix(tag, "v"))
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRelease) assetName() string {
	return update.ArchiveName(f.tag, "linux", "amd64")
}

func (f *fakeRelease) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.mu.Unlock()
	switch r.URL.Path {
	case "/repos/" + update.Repo + "/releases/latest":
		fmt.Fprint(w, f.releaseJSON())
	case "/dl/" + f.assetName():
		w.Write(f.archive)
	case "/dl/checksums.txt":
		fmt.Fprint(w, f.sums)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeRelease) releaseJSON() string {
	type asset struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int    `json:"size"`
	}
	assets := []asset{{Name: "checksums.txt", URL: f.srv.URL + "/dl/checksums.txt", Size: len(f.sums)}}
	if !f.omitArch {
		assets = append(assets, asset{Name: f.assetName(), URL: f.srv.URL + "/dl/" + f.assetName(), Size: len(f.archive)})
	}
	b, _ := json.Marshal(map[string]any{"tag_name": f.tag, "draft": false, "prerelease": false, "assets": assets})
	return string(b)
}

func (f *fakeRelease) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

// installFake puts an "old" bunker in a temp dir, reached through a
// symlink the way ~/bin links often are, and returns the real path.
func installFake(t *testing.T) (link, real string) {
	t.Helper()
	dir := t.TempDir()
	real = filepath.Join(dir, "bin", "bunker")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("old bunker"), 0o755); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(dir, "bunker-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	return link, real
}

type fakeRunner struct {
	active bool
	calls  []string
}

func (r *fakeRunner) run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if len(args) > 1 && args[1] == "is-active" && !r.active {
		return errors.New("exit status 3")
	}
	return nil
}

func testUpdateDeps(t *testing.T, f *fakeRelease, exe string, runner *fakeRunner) updateDeps {
	t.Helper()
	return updateDeps{
		build:      buildInfo{Version: "0.12.0", Release: true},
		client:     f.srv.Client(),
		baseURL:    f.srv.URL,
		executable: func() (string, error) { return exe, nil },
		run:        runner.run,
		cachePath:  filepath.Join(t.TempDir(), update.CacheFile),
		goos:       "linux",
		goarch:     "amd64",
	}
}

func runUpdate(t *testing.T, deps updateDeps, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cmdUpdate(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr, deps)
	return code, stdout.String(), stderr.String()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpdateReplacesBinaryAndRestartsService(t *testing.T) {
	f := newFakeRelease(t, "v0.13.0")
	link, real := installFake(t)
	runner := &fakeRunner{active: true}
	code, stdout, stderr := runUpdate(t, testUpdateDeps(t, f, link, runner), "", "--yes")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if got := readFile(t, real); got != newBinary {
		t.Fatalf("binary = %q, want the release's", got)
	}
	info, err := os.Stat(real)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
	if got := readFile(t, real+".old"); got != "old bunker" {
		t.Fatalf("bunker.old = %q", got)
	}
	if l, err := os.Readlink(link); err != nil || l != real {
		t.Fatalf("the symlink itself was touched: %q %v", l, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(real))
	if len(entries) != 2 {
		t.Fatalf("leftover files in the target dir: %v", entries)
	}
	want := []string{"systemctl --user is-active --quiet bunker", "systemctl --user restart bunker"}
	if strings.Join(runner.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("runner calls = %q, want %q", runner.calls, want)
	}
	if !strings.Contains(stdout, "0.12.0 → 0.13.0") || !strings.Contains(stdout, "bunker.service restarted") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestUpdateWithInactiveServiceSaysToRestart(t *testing.T) {
	f := newFakeRelease(t, "v0.13.0")
	link, _ := installFake(t)
	runner := &fakeRunner{active: false}
	code, stdout, stderr := runUpdate(t, testUpdateDeps(t, f, link, runner), "", "--yes")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if len(runner.calls) != 1 || !strings.Contains(stdout, "restart your 'bunker daemon'") {
		t.Fatalf("calls %q, stdout %q", runner.calls, stdout)
	}
}

func TestUpdateChecksumMismatchChangesNothing(t *testing.T) {
	f := newFakeRelease(t, "v0.13.0")
	f.sums = strings.Repeat("a", 64) + "  " + f.assetName() + "\n"
	link, real := installFake(t)
	runner := &fakeRunner{active: true}
	code, _, stderr := runUpdate(t, testUpdateDeps(t, f, link, runner), "", "--yes")
	if code == 0 || !strings.Contains(stderr, "checksum mismatch") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	assertUntouched(t, real)
	if len(runner.calls) != 0 {
		t.Fatalf("restarted after a failed update: %q", runner.calls)
	}
}

func TestUpdateMissingChecksumEntryChangesNothing(t *testing.T) {
	f := newFakeRelease(t, "v0.13.0")
	f.sums = strings.Repeat("a", 64) + "  something_else.tar.gz\n"
	link, real := installFake(t)
	code, _, stderr := runUpdate(t, testUpdateDeps(t, f, link, &fakeRunner{}), "", "--yes")
	if code == 0 || !strings.Contains(stderr, "no entry for "+f.assetName()) {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	assertUntouched(t, real)
}

func TestUpdateMissingAssetForArch(t *testing.T) {
	f := newFakeRelease(t, "v0.13.0")
	f.omitArch = true
	link, real := installFake(t)
	code, _, stderr := runUpdate(t, testUpdateDeps(t, f, link, &fakeRunner{}), "", "--yes")
	if code == 0 || !strings.Contains(stderr, "no build for linux/amd64") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	assertUntouched(t, real)
	for _, p := range f.requests() {
		if strings.HasPrefix(p, "/dl/") {
			t.Fatalf("downloaded %s for a release without our arch", p)
		}
	}
}

func assertUntouched(t *testing.T, real string) {
	t.Helper()
	if got := readFile(t, real); got != "old bunker" {
		t.Fatalf("binary = %q, want it untouched", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(real))
	if len(entries) != 1 {
		t.Fatalf("the target dir changed: %v", entries)
	}
}

func TestUpdateDryRunMakesNoNetworkCalls(t *testing.T) {
	f := newFakeRelease(t, "v0.13.0")
	link, real := installFake(t)
	deps := testUpdateDeps(t, f, link, &fakeRunner{active: true})
	// The daemon's cached check is all --dry-run reads.
	rel, err := update.Latest(context.Background(), f.srv.Client(), f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cache, _ := json.Marshal(update.Cache{Release: rel})
	if err := os.WriteFile(deps.cachePath, cache, 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(f.requests())
	deps.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("--dry-run made a request to %s", r.URL)
		return nil, errors.New("no network in dry-run")
	})}

	code, stdout, stderr := runUpdate(t, deps, "", "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	for _, want := range []string{"0.12.0 → 0.13.0", f.srv.URL + "/dl/" + f.assetName(), real} {
		if !strings.Contains(stdout, want) {
			t.Errorf("plan lacks %q:\n%s", want, stdout)
		}
	}
	if len(f.requests()) != before {
		t.Fatalf("dry-run reached the server: %v", f.requests()[before:])
	}
	assertUntouched(t, real)

	code, stdout, _ = runUpdate(t, deps, "", "--dry-run", "--json")
	var got struct {
		DryRun bool        `json:"dry_run"`
		Plan   update.Plan `json:"plan"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &got) != nil || !got.DryRun || got.Plan.Target != real || got.Plan.To != "0.13.0" {
		t.Fatalf("json plan: exit %d, %s", code, stdout)
	}
}

func TestUpdateDryRunWithoutCacheFails(t *testing.T) {
	f := newFakeRelease(t, "v0.13.0")
	link, _ := installFake(t)
	code, _, stderr := runUpdate(t, testUpdateDeps(t, f, link, &fakeRunner{}), "", "--dry-run")
	if code == 0 || !strings.Contains(stderr, "never touches the network") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if len(f.requests()) != 0 {
		t.Fatalf("requests = %v", f.requests())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpdateRefusals(t *testing.T) {
	t.Run("dev build", func(t *testing.T) {
		f := newFakeRelease(t, "v0.13.0")
		link, real := installFake(t)
		deps := testUpdateDeps(t, f, link, &fakeRunner{})
		deps.build = buildInfo{Version: devVersion}
		code, _, stderr := runUpdate(t, deps, "", "--yes")
		if code == 0 || !strings.Contains(stderr, "dev build") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if len(f.requests()) != 0 {
			t.Fatalf("a dev build reached the network: %v", f.requests())
		}
		assertUntouched(t, real)
	})
	t.Run("up to date", func(t *testing.T) {
		f := newFakeRelease(t, "v0.12.0")
		link, real := installFake(t)
		code, stdout, stderr := runUpdate(t, testUpdateDeps(t, f, link, &fakeRunner{}), "", "--yes")
		if code != 0 || !strings.Contains(stdout, "up to date") {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		assertUntouched(t, real)
	})
	t.Run("older release", func(t *testing.T) {
		f := newFakeRelease(t, "v0.11.0")
		link, real := installFake(t)
		code, stdout, _ := runUpdate(t, testUpdateDeps(t, f, link, &fakeRunner{}), "", "--yes")
		if code != 0 || !strings.Contains(stdout, "up to date") {
			t.Fatalf("exit %d, stdout %q", code, stdout)
		}
		assertUntouched(t, real)
	})
	t.Run("read-only dir", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes to read-only directories")
		}
		f := newFakeRelease(t, "v0.13.0")
		link, real := installFake(t)
		dir := filepath.Dir(real)
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
		code, _, stderr := runUpdate(t, testUpdateDeps(t, f, link, &fakeRunner{}), "", "--yes")
		if code == 0 || !strings.Contains(stderr, "cannot write to "+dir) {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if got := readFile(t, real); got != "old bunker" {
			t.Fatalf("binary = %q", got)
		}
	})
	t.Run("non-tty without --yes", func(t *testing.T) {
		f := newFakeRelease(t, "v0.13.0")
		link, real := installFake(t)
		code, _, stderr := runUpdate(t, testUpdateDeps(t, f, link, &fakeRunner{}), "y\n")
		if code == 0 || !strings.Contains(stderr, "--yes") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		assertUntouched(t, real)
	})
	t.Run("tty says no", func(t *testing.T) {
		f := newFakeRelease(t, "v0.13.0")
		link, real := installFake(t)
		deps := testUpdateDeps(t, f, link, &fakeRunner{})
		deps.stdinIsTTY = true
		code, _, stderr := runUpdate(t, deps, "n\n")
		if code == 0 || !strings.Contains(stderr, "aborted") || !strings.Contains(stderr, "Proceed?") {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		assertUntouched(t, real)
	})
	t.Run("tty says yes", func(t *testing.T) {
		f := newFakeRelease(t, "v0.13.0")
		link, real := installFake(t)
		deps := testUpdateDeps(t, f, link, &fakeRunner{})
		deps.stdinIsTTY = true
		code, _, stderr := runUpdate(t, deps, "y\n")
		if code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if got := readFile(t, real); got != newBinary {
			t.Fatalf("binary = %q", got)
		}
	})
}

func TestUpdateRestartFailureIsLoud(t *testing.T) {
	f := newFakeRelease(t, "v0.13.0")
	link, real := installFake(t)
	deps := testUpdateDeps(t, f, link, &fakeRunner{})
	deps.run = func(_ context.Context, name string, args ...string) error {
		if args[1] == "restart" {
			return errors.New("exit status 1")
		}
		return nil
	}
	code, _, stderr := runUpdate(t, deps, "", "--yes")
	if code == 0 || !strings.Contains(stderr, "restarting bunker.service failed") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if got := readFile(t, real); got != newBinary {
		t.Fatalf("binary = %q, the update itself succeeded", got)
	}
}
