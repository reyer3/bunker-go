// Package update finds bunker's newest GitHub release and replaces the
// running binary with it. The only request it makes to learn about a
// release is GET /repos/{Repo}/releases/latest: no telemetry, nothing
// about the user is sent.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the GitHub API; tests point Latest at an
	// httptest server instead.
	DefaultBaseURL = "https://api.github.com"
	// Repo is where bunker's releases are published.
	Repo = "reyer3/bunker-go"
	// ChecksumsAsset is goreleaser's sha256 list for every archive.
	ChecksumsAsset = "checksums.txt"

	// maxReleaseJSONBytes bounds the release metadata: a real one is a
	// few KiB, so anything past this is not GitHub answering.
	maxReleaseJSONBytes = 1 << 20
	// userAgent identifies the request, as GitHub's API requires one.
	userAgent = "bunker-go-update"
)

// Asset is one file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// Release is the part of a GitHub release bunker needs.
type Release struct {
	Tag string `json:"tag"`
	// Version is Tag without its "v" prefix, matching what goreleaser
	// stamps into the binary and into the archive names.
	Version    string    `json:"version"`
	Prerelease bool      `json:"prerelease,omitempty"`
	Published  time.Time `json:"published,omitempty"`
	Assets     []Asset   `json:"assets"`
}

// Asset returns the release asset called name.
func (r Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// ArchiveName is goreleaser's archive name for version on goos/goarch
// (see .goreleaser.yaml's name_template).
func ArchiveName(version, goos, goarch string) string {
	return fmt.Sprintf("bunker_%s_%s_%s.tar.gz", strings.TrimPrefix(version, "v"), goos, goarch)
}

// githubRelease is the API's JSON shape, trimmed to what Release keeps.
type githubRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

// Latest asks baseURL (DefaultBaseURL when empty) for Repo's latest
// release. GitHub never returns a draft or a pre-release there, but a
// draft is still refused in case a proxy or mirror does.
func Latest(ctx context.Context, client *http.Client, baseURL string) (Release, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	url := strings.TrimRight(baseURL, "/") + "/repos/" + Repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, fmt.Errorf("update: latest release: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("update: latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("update: latest release: %s answered %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseJSONBytes+1))
	if err != nil {
		return Release{}, fmt.Errorf("update: latest release: read: %w", err)
	}
	if len(body) > maxReleaseJSONBytes {
		return Release{}, fmt.Errorf("update: latest release: response larger than %d bytes", maxReleaseJSONBytes)
	}
	return parseRelease(body)
}

func parseRelease(body []byte) (Release, error) {
	var gh githubRelease
	if err := json.Unmarshal(body, &gh); err != nil {
		return Release{}, fmt.Errorf("update: latest release: decode: %w", err)
	}
	if gh.Draft {
		return Release{}, fmt.Errorf("update: latest release %q is a draft", gh.TagName)
	}
	if _, err := parseSemver(gh.TagName); err != nil {
		return Release{}, fmt.Errorf("update: latest release tag: %w", err)
	}
	rel := Release{
		Tag:        gh.TagName,
		Version:    strings.TrimPrefix(gh.TagName, "v"),
		Prerelease: gh.Prerelease,
		Published:  gh.PublishedAt,
		Assets:     make([]Asset, 0, len(gh.Assets)),
	}
	for _, a := range gh.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	return rel, nil
}
