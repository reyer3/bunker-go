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
	a.storeDescriptor(ctx, sink, item.ID, 0, desc)
}

// persistSentDescriptors saves the descriptors of media WE uploaded,
// keyed by the item id core stores the send under (its receipt id), one
// per attachment index in send order: core.Service stores a multi-file
// send as ONE item carrying every attachment, so index i must resolve
// to the i-th upload. Without this, our own media could never be
// downloaded again (WhatsApp never echoes a linked device's own send).
// No-op when the adapter is not running (no sink to persist into).
func (a *Adapter) persistSentDescriptors(ctx context.Context, itemID string, msgs []*waE2E.Message) {
	a.mu.Lock()
	sink := a.sink
	a.mu.Unlock()
	if sink == nil || itemID == "" {
		return
	}
	for i, msg := range msgs {
		if desc, ok := descriptorFromMessage(msg); ok {
			a.storeDescriptor(ctx, sink, itemID, i, desc)
		}
	}
}

// storeDescriptor persists desc for (itemID, index). A descriptor
// without a MediaKey is never written: it cannot decrypt anything, and
// writing it would overwrite a usable one already stored — e.g. the key
// kept when we sent the message, clobbered by a keyless echo of it.
func (a *Adapter) storeDescriptor(ctx context.Context, sink core.Sink, itemID string, index int, desc mediaDescriptor) {
	if len(desc.MediaKey) == 0 {
		return
	}
	data, err := json.Marshal(desc)
	if err != nil {
		log.Printf("whatsapp: marshal media descriptor for %s: %v", itemID, err)
		return
	}
	if err := sink.SetCursor(ctx, mediaDescriptorKey(a.account, itemID, index), string(data)); err != nil {
		log.Printf("whatsapp: persist media descriptor for %s: %v", itemID, err)
	}
}

// DownloadAttachment implements core.AttachmentDownloader: it looks up
// the download descriptor persisted for (item.ID, index) when the
// message first arrived (live or history sync, see
// persistMediaDescriptor) or when we sent it (persistSentDescriptors)
// and downloads it through whatsmeow's Download.
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
