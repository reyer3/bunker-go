// M1: Matrix attachment download. This file is the adapter's
// core.AttachmentDownloader half: extracting a core.Attachment's public
// metadata from an incoming m.image/m.video/m.audio/m.file event
// (attachmentFromContent, called from toItem), privately persisting the
// download material one is fetched with later (matrixMediaDescriptor,
// mirroring WhatsApp's D2 mediaDescriptor: never a core.Item field, so it
// can never reach RPC or JSON output), and DownloadAttachment itself.
package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// ErrNoMediaDescriptor is returned by DownloadAttachment when item's
// attachment has no persisted download descriptor — it was stored before
// this feature existed, or the room key never arrived, so
// persistMediaDescriptor never ran. It is never a panic: the homeserver
// still has the media, so a later re-sync (or, for an encrypted room, the
// key finally arriving) is the recovery path.
var ErrNoMediaDescriptor = errors.New("matrix: no media descriptor stored; re-sync from the homeserver")

// isMediaMsgType reports whether t is one of the four msgtypes that carry
// a downloadable attachment (buildMediaContent's own dispatch, in
// reverse).
func isMediaMsgType(t event.MessageType) bool {
	switch t {
	case event.MsgImage, event.MsgVideo, event.MsgAudio, event.MsgFile:
		return true
	default:
		return false
	}
}

// attachmentFromContent extracts one core.Attachment's public metadata
// from content, when content describes a downloadable m.image/m.video/
// m.audio/m.file message: Name (FileName, falling back to Body), MIME and
// Size from Info, and Ref set to the content's mxc:// URI (the plain
// `url`, or an encrypted room's `file.url`) — never the encrypted room's
// key material, which only matrixMediaDescriptor carries. ok is false for
// any other msgtype (m.text, m.notice, ...).
func attachmentFromContent(content *event.MessageEventContent) (core.Attachment, bool) {
	if !isMediaMsgType(content.MsgType) {
		return core.Attachment{}, false
	}
	name := content.FileName
	if name == "" {
		name = content.Body
	}
	var mimeType string
	var size int64
	if content.Info != nil {
		mimeType = content.Info.MimeType
		size = int64(content.Info.Size)
	}
	ref := string(content.URL)
	if content.File != nil {
		ref = string(content.File.URL)
	}
	att := core.Attachment{Name: name, MIME: mimeType, Size: size, Ref: ref}
	if content.MsgType == event.MsgAudio && content.MSC3245Voice != nil {
		att.Voice = true
		if content.MSC1767Audio != nil {
			att.Duration = (content.MSC1767Audio.Duration + 500) / 1000
			att.Waveform = waveformFromMSC1767(content.MSC1767Audio.Waveform)
		}
		if att.Duration == 0 && content.Info != nil {
			att.Duration = (content.Info.Duration + 500) / 1000
		}
	}
	return att, true
}

// maxWaveformBars bounds the waveform kept on an Attachment: Matrix lets a
// sender put any number of samples there, and every copy rides along in
// the store row and every listing. Element sends 100.
const maxWaveformBars = 256

// waveformFromMSC1767 converts Matrix's 0-1024 samples to Attachment's
// 0-100 bars. A waveform longer than maxWaveformBars is dropped (the note
// still plays; only the bar graph is lost).
func waveformFromMSC1767(w []int) []byte {
	if len(w) == 0 || len(w) > maxWaveformBars {
		return nil
	}
	out := make([]byte, len(w))
	for i, v := range w {
		if v < 0 {
			v = 0
		}
		if v > 1024 {
			v = 1024
		}
		out[i] = byte(v * 100 / 1024)
	}
	return out
}

// matrixMediaDescriptor is the download material for one Matrix media
// event's attachment, persisted through core.Sink's Cursor/SetCursor —
// 0600 adapter-private state, never exposed through core.Item, RPC or
// JSON API output. An encrypted room's descriptor carries File (mautrix's
// EncryptedFileInfo: the mxc URL plus the AES key/iv/hash
// DownloadAttachment needs to decrypt); an unencrypted room's carries URL
// alone.
type matrixMediaDescriptor struct {
	URL  id.ContentURIString      `json:"url,omitempty"`
	File *event.EncryptedFileInfo `json:"file,omitempty"`
}

// descriptorFromContent builds the matrixMediaDescriptor to persist for
// content, mirroring attachmentFromContent's own msgtype check. ok is
// false when content carries no attachment at all, or a media msgtype
// with neither url nor file set (malformed).
func descriptorFromContent(content *event.MessageEventContent) (matrixMediaDescriptor, bool) {
	if !isMediaMsgType(content.MsgType) {
		return matrixMediaDescriptor{}, false
	}
	if content.File != nil {
		return matrixMediaDescriptor{File: content.File}, true
	}
	if content.URL != "" {
		return matrixMediaDescriptor{URL: content.URL}, true
	}
	return matrixMediaDescriptor{}, false
}

// matrixMediaDescriptorKey is the core.Sink cursor key persisting item
// id's attachment at index's download descriptor.
func matrixMediaDescriptorKey(account, itemID string, index int) string {
	return fmt.Sprintf("matrix:%s:media:%s:%d", account, itemID, index)
}

// persistMediaDescriptor saves item's first attachment's download
// descriptor (a Matrix message carries at most one, like WhatsApp, so
// index is always 0 here), keyed by (account, item.ID, 0). A marshal or
// sink failure is logged, never fatal: messageHandler/encryptedHandler
// must keep processing the sync stream either way, and a missing
// descriptor only means a later download fails with
// ErrNoMediaDescriptor instead of succeeding.
func (a *Adapter) persistMediaDescriptor(ctx context.Context, sink core.Sink, item core.Item, content *event.MessageEventContent) {
	if len(item.Attachments) == 0 || content == nil {
		return
	}
	desc, ok := descriptorFromContent(content)
	if !ok {
		return
	}
	data, err := json.Marshal(desc)
	if err != nil {
		log.Printf("matrix: marshal media descriptor for %s: %v", item.ID, err)
		return
	}
	if err := sink.SetCursor(ctx, matrixMediaDescriptorKey(a.account, item.ID, 0), string(data)); err != nil {
		log.Printf("matrix: persist media descriptor for %s: %v", item.ID, err)
	}
}

// DownloadAttachment implements core.AttachmentDownloader: it looks up
// the download descriptor persisted for (item.ID, index) when the event
// first arrived (see persistMediaDescriptor) and downloads it through the
// homeserver's media repo, decrypting it first (attachment.EncryptedFile,
// hash-checked) when the descriptor carries File.
func (a *Adapter) DownloadAttachment(ctx context.Context, item core.Item, index int) (io.ReadCloser, error) {
	if index < 0 || index >= len(item.Attachments) {
		return nil, fmt.Errorf("matrix: download %s: attachment index %d out of range: %w", item.ID, index, core.ErrNotFound)
	}

	a.mu.Lock()
	sink := a.sink
	a.mu.Unlock()
	if sink == nil {
		return nil, fmt.Errorf("matrix: download %s: adapter is not running: %w", item.ID, core.ErrUnsupported)
	}

	raw, err := sink.Cursor(ctx, matrixMediaDescriptorKey(a.account, item.ID, index))
	if err != nil {
		return nil, fmt.Errorf("matrix: download %s: %w", item.ID, err)
	}
	if raw == "" {
		return nil, fmt.Errorf("matrix: download %s: %w", item.ID, ErrNoMediaDescriptor)
	}
	var desc matrixMediaDescriptor
	if err := json.Unmarshal([]byte(raw), &desc); err != nil {
		return nil, fmt.Errorf("matrix: download %s: decode stored descriptor: %w", item.ID, err)
	}

	if desc.File != nil {
		uri, err := desc.File.URL.Parse()
		if err != nil {
			return nil, fmt.Errorf("matrix: download %s: parse encrypted file url: %w", item.ID, err)
		}
		data, err := a.downloadMedia(ctx, uri)
		if err != nil {
			return nil, fmt.Errorf("matrix: download %s: %w", item.ID, err)
		}
		if err := desc.File.DecryptInPlace(data); err != nil {
			return nil, fmt.Errorf("matrix: download %s: decrypt: %w", item.ID, err)
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}

	uri, err := desc.URL.Parse()
	if err != nil {
		return nil, fmt.Errorf("matrix: download %s: parse url: %w", item.ID, err)
	}
	data, err := a.downloadMedia(ctx, uri)
	if err != nil {
		return nil, fmt.Errorf("matrix: download %s: %w", item.ID, err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
