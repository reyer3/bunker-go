package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"

	"go.mau.fi/whatsmeow/proto/waE2E"

	"github.com/reyer3/bunker-go/internal/core"
)

// ErrNoMediaKey is returned by DownloadAttachment when item's attachment
// has no persisted download descriptor — it was stored before D2 added
// this persistence, so its MediaKey was never kept. It is never a
// panic: the phone still has the media, so re-sending or re-syncing it
// is the recovery path, not a crash.
var ErrNoMediaKey = errors.New("whatsapp: no media key stored; re-download from the phone")

// persistMediaDescriptor saves item's first attachment's download
// descriptor (see mediaDescriptor), keyed by (account, item.ID, 0) —
// bodyAndMedia's own doc comment notes a WhatsApp message carries at
// most one media attachment, so index is always 0 here. A marshal or
// sink failure is logged, never fatal: handleEvent/handleHistorySync
// must keep processing the rest of the stream either way, and a missing
// descriptor only means a later download fails with ErrNoMediaKey
// instead of succeeding.
func (a *Adapter) persistMediaDescriptor(ctx context.Context, sink core.Sink, item core.Item, msg *waE2E.Message) {
	if len(item.Attachments) == 0 {
		return
	}
	desc, ok := descriptorFromMessage(msg)
	if !ok {
		return
	}
	data, err := json.Marshal(desc)
	if err != nil {
		log.Printf("whatsapp: marshal media descriptor for %s: %v", item.ID, err)
		return
	}
	if err := sink.SetCursor(ctx, mediaDescriptorKey(a.account, item.ID, 0), string(data)); err != nil {
		log.Printf("whatsapp: persist media descriptor for %s: %v", item.ID, err)
	}
}

// DownloadAttachment implements core.AttachmentDownloader: it looks up
// the download descriptor persisted for (item.ID, index) when the
// message first arrived (live or history sync, see
// persistMediaDescriptor) and downloads it through whatsmeow's Download.
func (a *Adapter) DownloadAttachment(ctx context.Context, item core.Item, index int) (io.ReadCloser, error) {
	if index < 0 || index >= len(item.Attachments) {
		return nil, fmt.Errorf("whatsapp: download %s: attachment index %d out of range: %w", item.ID, index, core.ErrNotFound)
	}

	a.mu.Lock()
	sink := a.sink
	a.mu.Unlock()
	if sink == nil {
		return nil, fmt.Errorf("whatsapp: download %s: adapter is not running: %w", item.ID, core.ErrUnsupported)
	}

	raw, err := sink.Cursor(ctx, mediaDescriptorKey(a.account, item.ID, index))
	if err != nil {
		return nil, fmt.Errorf("whatsapp: download %s: %w", item.ID, err)
	}
	if raw == "" {
		return nil, fmt.Errorf("whatsapp: download %s: %w", item.ID, ErrNoMediaKey)
	}
	var desc mediaDescriptor
	if err := json.Unmarshal([]byte(raw), &desc); err != nil {
		return nil, fmt.Errorf("whatsapp: download %s: decode stored descriptor: %w", item.ID, err)
	}

	data, err := a.cli.Download(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: download %s: %w", item.ID, err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
