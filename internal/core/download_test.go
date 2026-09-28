package core_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// spyDownloaderAdapter implements core.AttachmentDownloader on top of
// spyAdapter's mandatory Adapter capability, so Service.Download tests
// never need a real mail/WhatsApp connection.
type spyDownloaderAdapter struct {
	spyAdapter

	data          []byte
	err           error
	downloadCalls int
	lastItem      core.Item
	lastIndex     int
}

func (s *spyDownloaderAdapter) DownloadAttachment(_ context.Context, item core.Item, index int) (io.ReadCloser, error) {
	s.downloadCalls++
	s.lastItem = item
	s.lastIndex = index
	if s.err != nil {
		return nil, s.err
	}
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

var _ core.AttachmentDownloader = (*spyDownloaderAdapter)(nil)

func itemWithAttachment(id string, att core.Attachment) core.Item {
	return core.Item{
		ID:          id,
		Channel:     core.ChannelMail,
		Account:     "cl",
		Attachments: []core.Attachment{att},
	}
}

func TestServiceDownloadWritesFileWithDeclaredBytes(t *testing.T) {
	data := []byte("hello attachment bytes")
	item := itemWithAttachment("mail:cl:1", core.Attachment{Name: "a.txt", MIME: "text/plain", Size: int64(len(data)), Ref: "part-1"})
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyDownloaderAdapter{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}, data: data}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	dest := filepath.Join(t.TempDir(), "out.txt")
	res, err := svc.Download(context.Background(), item.ID, 0, dest, core.DownloadOptions{})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.Bytes != int64(len(data)) {
		t.Errorf("Bytes = %d, want %d", res.Bytes, len(data))
	}
	if res.Path != dest {
		t.Errorf("Path = %q, want %q", res.Path, dest)
	}
	if res.Name != "a.txt" || res.MIME != "text/plain" {
		t.Errorf("Name/MIME = %q/%q, want a.txt/text/plain", res.Name, res.MIME)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", dest, err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("file contents = %q, want %q", got, data)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("Stat(%s): %v", dest, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %v, want 0600", perm)
	}

	if spy.downloadCalls != 1 {
		t.Fatalf("DownloadAttachment called %d times, want 1", spy.downloadCalls)
	}
	if spy.lastIndex != 0 {
		t.Errorf("lastIndex = %d, want 0", spy.lastIndex)
	}
}

func TestServiceDownloadRefusesToOverwriteWithoutForce(t *testing.T) {
	data := []byte("new bytes")
	item := itemWithAttachment("mail:cl:1", core.Attachment{Name: "a.txt", MIME: "text/plain", Size: int64(len(data))})
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyDownloaderAdapter{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}, data: data}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	dest := filepath.Join(t.TempDir(), "existing.txt")
	if err := os.WriteFile(dest, []byte("old bytes"), 0o600); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	_, err := svc.Download(context.Background(), item.ID, 0, dest, core.DownloadOptions{})
	if !errors.Is(err, core.ErrDestinationExists) {
		t.Fatalf("Download() error = %v, want ErrDestinationExists", err)
	}

	got, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatalf("ReadFile(%s): %v", dest, readErr)
	}
	if string(got) != "old bytes" {
		t.Errorf("existing file was overwritten: got %q", got)
	}
}

func TestServiceDownloadForceOverwritesExistingFile(t *testing.T) {
	data := []byte("new bytes")
	item := itemWithAttachment("mail:cl:1", core.Attachment{Name: "a.txt", MIME: "text/plain", Size: int64(len(data))})
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyDownloaderAdapter{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}, data: data}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	dest := filepath.Join(t.TempDir(), "existing.txt")
	if err := os.WriteFile(dest, []byte("old bytes"), 0o600); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	res, err := svc.Download(context.Background(), item.ID, 0, dest, core.DownloadOptions{Force: true})
	if err != nil {
		t.Fatalf("Download() with Force error = %v", err)
	}
	if res.Bytes != int64(len(data)) {
		t.Errorf("Bytes = %d, want %d", res.Bytes, len(data))
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(data) {
		t.Errorf("file contents = %q, want %q", got, data)
	}
}

func TestServiceDownloadCapsOversizedAttachment(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 100)
	item := itemWithAttachment("mail:cl:1", core.Attachment{Name: "big.bin", MIME: "application/octet-stream", Size: int64(len(data))})
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyDownloaderAdapter{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}, data: data}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	dest := filepath.Join(t.TempDir(), "big.bin")
	_, err := svc.Download(context.Background(), item.ID, 0, dest, core.DownloadOptions{MaxBytes: 10})
	if !errors.Is(err, core.ErrAttachmentTooLarge) {
		t.Fatalf("Download() error = %v, want ErrAttachmentTooLarge", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("Download() left a partial file behind at %s", dest)
	}
}

func TestServiceDownloadRejectsSizeMismatch(t *testing.T) {
	data := []byte("actual bytes are shorter")
	item := itemWithAttachment("mail:cl:1", core.Attachment{Name: "a.txt", MIME: "text/plain", Size: int64(len(data)) + 1000})
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyDownloaderAdapter{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}, data: data}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	dest := filepath.Join(t.TempDir(), "a.txt")
	_, err := svc.Download(context.Background(), item.ID, 0, dest, core.DownloadOptions{})
	if !errors.Is(err, core.ErrSizeMismatch) {
		t.Fatalf("Download() error = %v, want ErrSizeMismatch", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("Download() left a partial file behind at %s", dest)
	}
}

func TestServiceDownloadRejectsOutOfRangeIndex(t *testing.T) {
	item := itemWithAttachment("mail:cl:1", core.Attachment{Name: "a.txt", MIME: "text/plain", Size: 1})
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyDownloaderAdapter{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}}
	reg.Register(spy)
	svc := core.NewService(store, reg)

	dest := filepath.Join(t.TempDir(), "a.txt")
	_, err := svc.Download(context.Background(), item.ID, 5, dest, core.DownloadOptions{})
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Download() error = %v, want ErrNotFound", err)
	}
	if spy.downloadCalls != 0 {
		t.Errorf("DownloadAttachment called %d times, want 0 for an out-of-range index", spy.downloadCalls)
	}
}

func TestServiceDownloadUnsupportedAdapterCapability(t *testing.T) {
	item := itemWithAttachment("mail:cl:1", core.Attachment{Name: "a.txt", MIME: "text/plain", Size: 1})
	store := newMemStore(item)
	reg := core.NewRegistry()
	reg.Register(bareAdapter{channel: core.ChannelMail, account: "cl"})
	svc := core.NewService(store, reg)

	dest := filepath.Join(t.TempDir(), "a.txt")
	_, err := svc.Download(context.Background(), item.ID, 0, dest, core.DownloadOptions{})
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("Download() error = %v, want ErrUnsupported", err)
	}
}

func TestServiceDownloadUnknownItem(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	svc := core.NewService(store, reg)

	dest := filepath.Join(t.TempDir(), "a.txt")
	_, err := svc.Download(context.Background(), "mail:cl:missing", 0, dest, core.DownloadOptions{})
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Download() error = %v, want ErrNotFound", err)
	}
}

// spyFetchingDownloaderAdapter also implements core.Fetcher, like the
// mail adapter: sync stores headers only, so attachments exist only on
// the fetched item.
type spyFetchingDownloaderAdapter struct {
	spyDownloaderAdapter
	fetched core.Item
}

func (s *spyFetchingDownloaderAdapter) Fetch(_ context.Context, _ string) (core.Item, error) {
	return s.fetched, nil
}

// TestServiceDownloadFetchesWhenStoredItemLacksAttachments covers the
// live failure: a mail synced from headers is stored with 0 attachments,
// while `read` (Fetch) shows them, so Download must fall back to Fetch
// instead of reporting the index out of range.
func TestServiceDownloadFetchesWhenStoredItemLacksAttachments(t *testing.T) {
	data := []byte("png bytes")
	stored := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl"}
	fetched := itemWithAttachment("mail:cl:1", core.Attachment{Name: "a.png", MIME: "image/png", Size: int64(len(data)), Ref: "part-1"})
	reg := core.NewRegistry()
	spy := &spyFetchingDownloaderAdapter{
		spyDownloaderAdapter: spyDownloaderAdapter{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}, data: data},
		fetched:              fetched,
	}
	reg.Register(spy)
	svc := core.NewService(newMemStore(stored), reg)

	dest := filepath.Join(t.TempDir(), "a.png")
	res, err := svc.Download(context.Background(), stored.ID, 0, dest, core.DownloadOptions{})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if res.Name != "a.png" || res.Bytes != int64(len(data)) {
		t.Errorf("result = %+v, want a.png with %d bytes", res, len(data))
	}
	if len(spy.lastItem.Attachments) != 1 {
		t.Errorf("DownloadAttachment got item with %d attachments, want the fetched item (1)", len(spy.lastItem.Attachments))
	}
}

// TestServiceDownloadFetchesFromServerWhenStoreLacksItem: mail-history
// H1 follow-up. An id older than the store's first synced UID is not in
// the store at all; Download must go through Fetch's server fallback
// (which also upserts it) instead of failing on store.Get.
func TestServiceDownloadFetchesFromServerWhenStoreLacksItem(t *testing.T) {
	data := []byte("xlsx bytes")
	fetched := itemWithAttachment("mail:cl:5", core.Attachment{Name: "carga.xlsx", MIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Size: int64(len(data)), Ref: "part-1"})
	reg := core.NewRegistry()
	spy := &spyFetchingDownloaderAdapter{
		spyDownloaderAdapter: spyDownloaderAdapter{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}, data: data},
		fetched:              fetched,
	}
	reg.Register(spy)
	store := newMemStore() // empty: mail:cl:5 was never synced
	svc := core.NewService(store, reg)

	dest := filepath.Join(t.TempDir(), "carga.xlsx")
	res, err := svc.Download(context.Background(), "mail:cl:5", 0, dest, core.DownloadOptions{})
	if err != nil {
		t.Fatalf("Download() error = %v, want the server-fetched attachment", err)
	}
	if res.Name != "carga.xlsx" || res.Bytes != int64(len(data)) {
		t.Errorf("result = %+v, want carga.xlsx with %d bytes", res, len(data))
	}
	if _, err := store.Get(context.Background(), "mail:cl:5"); err != nil {
		t.Errorf("store.Get after Download error = %v, want the fetched item upserted", err)
	}
}

// TestServiceDownloadUnknownIDStaysErrNotFound: with no adapter for the
// id's account, the missing-from-store case is still ErrNotFound.
func TestServiceDownloadUnknownIDStaysErrNotFound(t *testing.T) {
	svc := core.NewService(newMemStore(), core.NewRegistry())
	_, err := svc.Download(context.Background(), "mail:nope:5", 0, filepath.Join(t.TempDir(), "x"), core.DownloadOptions{})
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Download() error = %v, want ErrNotFound", err)
	}
}
