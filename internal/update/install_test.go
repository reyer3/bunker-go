package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archiveWith(t *testing.T, name string, body []byte, typ byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: typ}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractBinary(t *testing.T) {
	bin, err := extractBinary(archiveWith(t, "bunker", []byte("ELF"), tar.TypeReg))
	if err != nil || string(bin) != "ELF" {
		t.Fatalf("extract = %q, %v", bin, err)
	}
	if bin, err := extractBinary(archiveWith(t, "bunker_0.13.0_linux_amd64/bunker", []byte("ELF"), tar.TypeReg)); err != nil || string(bin) != "ELF" {
		t.Fatalf("nested extract = %q, %v", bin, err)
	}
	if _, err := extractBinary(archiveWith(t, "README.md", []byte("x"), tar.TypeReg)); err == nil || !strings.Contains(err.Error(), "no bunker binary") {
		t.Fatalf("no binary: %v", err)
	}
	if _, err := extractBinary([]byte("not gzip")); err == nil {
		t.Fatal("garbage archive accepted")
	}
}

func TestChecksumFor(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sums := []byte(sum + "  bunker_0.13.0_linux_amd64.tar.gz\n" + strings.Repeat("cd", 32) + "  bunker_0.13.0_linux_arm64.tar.gz\n")
	if got, err := checksumFor(sums, "bunker_0.13.0_linux_amd64.tar.gz"); err != nil || got != sum {
		t.Fatalf("checksum = %q, %v", got, err)
	}
	if _, err := checksumFor(sums, "bunker_0.13.0_darwin_amd64.tar.gz"); err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Fatalf("missing entry: %v", err)
	}
	if _, err := checksumFor([]byte("xyz  a.tar.gz\n"), "a.tar.gz"); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed: %v", err)
	}
}

func TestCheckWritable(t *testing.T) {
	dir := t.TempDir()
	if err := CheckWritable(dir); err != nil {
		t.Fatalf("CheckWritable(temp dir) = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("the probe left %v behind", entries)
	}
	missing := filepath.Join(dir, "missing")
	if err := CheckWritable(missing); err == nil || !strings.Contains(err.Error(), "cannot write to "+missing) {
		t.Fatalf("CheckWritable(missing) = %v", err)
	}
}

func TestDownloadSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chunked" {
			// No Content-Length: the limit must hold on the body itself.
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, strings.Repeat("x", 100))
	}))
	defer srv.Close()
	for _, p := range []string{"/sized", "/chunked"} {
		if _, err := download(context.Background(), srv.Client(), srv.URL+p, 10); err == nil || !strings.Contains(err.Error(), "limit") {
			t.Errorf("%s: err = %v, want a size limit error", p, err)
		}
	}
	if b, err := download(context.Background(), srv.Client(), srv.URL+"/sized", 100); err != nil || len(b) != 100 {
		t.Fatalf("download at the limit = %d bytes, %v", len(b), err)
	}
}

func TestPlanForNeedsChecksums(t *testing.T) {
	rel := Release{Tag: "v0.13.0", Version: "0.13.0", Assets: []Asset{{Name: ArchiveName("0.13.0", "linux", "amd64"), URL: "u"}}}
	if _, err := PlanFor(rel, "0.12.0", "linux", "amd64", "/x/bunker"); err == nil || !strings.Contains(err.Error(), ChecksumsAsset) {
		t.Fatalf("PlanFor without checksums = %v", err)
	}
	rel.Assets = append(rel.Assets, Asset{Name: ChecksumsAsset, URL: "c"})
	plan, err := PlanFor(rel, "0.12.0", "linux", "amd64", "/x/bunker")
	if err != nil || plan.Backup != "/x/bunker.old" || plan.AssetURL != "u" || plan.ChecksumsURL != "c" {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
}
