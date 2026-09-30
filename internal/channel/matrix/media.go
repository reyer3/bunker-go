// T16(b): Matrix media. This file is the adapter's core.MediaSender half,
// kept separate from adapter.go/send.go (Send, resolveRoom) so it does not
// collide with concurrent work on the plain-text send path.
package matrix

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"

	"github.com/reyer3/bunker-go/internal/core"
)

// defaultMaxUploadBytes is used when the homeserver's own media config
// cannot be fetched (network error, or an older server with no
// m.upload.size at all): a conservative fallback, not a hard Matrix spec
// limit.
const defaultMaxUploadBytes = 50 * 1024 * 1024 // 50 MB

// maxUploadBytes returns the homeserver's advertised m.upload.size,
// fetched via GetMediaConfig exactly once (mediaConfigOnce) and cached for
// the adapter's lifetime — AttachmentPolicy has no context parameter to
// thread a per-call deadline through, and core.Service calls it
// synchronously before every send/reply validation (dry-run included), so
// repeating the network round trip on every call would be wasteful and
// would make every dry-run pay for it. A fetch that fails or returns no
// size at all falls back to defaultMaxUploadBytes; that failure is not
// retried on a later call, matching "fetch once" as literally as this
// interface allows.
func (a *Adapter) maxUploadBytes() int64 {
	a.mediaConfigOnce.Do(func() {
		a.mediaMaxUploadBytes = defaultMaxUploadBytes
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cfg, err := a.client.GetMediaConfig(ctx)
		if err == nil && cfg.UploadSize > 0 {
			a.mediaMaxUploadBytes = cfg.UploadSize
		}
	})
	return a.mediaMaxUploadBytes
}

// AttachmentPolicy declares that this adapter accepts any MIME type
// (core.AnyMIME), up to the homeserver's own upload size limit. Unlike
// WhatsApp, Matrix's media repo has no per-type limit of its own.
func (a *Adapter) AttachmentPolicy() core.AttachmentPolicy {
	return core.AttachmentPolicy{MaxBytes: map[string]int64{core.AnyMIME: a.maxUploadBytes()}}
}

// msgTypeForMIME picks the m.room.message msgtype an attachment's content
// type maps to: image/video/audio by MIME prefix, m.file for everything
// else (the same "everything else" catch-all core.AnyMIME lets through
// AttachmentPolicy in the first place).
func msgTypeForMIME(mimeType string) event.MessageType {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return event.MsgImage
	case strings.HasPrefix(mimeType, "video/"):
		return event.MsgVideo
	case strings.HasPrefix(mimeType, "audio/"):
		return event.MsgAudio
	default:
		return event.MsgFile
	}
}

// detectAttachmentMIME content-sniffs data (falling back to path's
// extension when sniffing is inconclusive), the same two-step
// core.Service's own inspectAttachment uses for AttachmentPolicy
// validation.
func detectAttachmentMIME(path string, data []byte) string {
	mimeType := http.DetectContentType(data)
	if mimeType == "application/octet-stream" {
		if guessed := mime.TypeByExtension(filepath.Ext(path)); guessed != "" {
			mimeType = guessed
		}
	}
	return mimeType
}

// buildMediaContent reads path, uploads it to the homeserver's media repo
// and returns the MessageEventContent for it: m.image/m.video/m.audio/
// m.file by content type, body/filename from the base name, and, when
// encrypted is true, the file encrypted client-side first (crypto/
// attachment) with the result in Info+File (no URL); an unencrypted room
// instead carries the plaintext upload's URL (no File). This is the one
// place SendMedia turns a local path into an outgoing event's content.
func (a *Adapter) buildMediaContent(ctx context.Context, path string, encrypted bool) (*event.MessageEventContent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("matrix: read attachment %q: %w", path, err)
	}
	mimeType := detectAttachmentMIME(path, data)
	name := filepath.Base(path)
	size := len(data)

	uploadContentType := mimeType
	var encFile *attachment.EncryptedFile
	if encrypted {
		encFile = attachment.NewEncryptedFile()
		encFile.EncryptInPlace(data) // mutates data into ciphertext in place
		uploadContentType = "application/octet-stream"
	}

	up, err := a.client.UploadBytesWithName(ctx, data, uploadContentType, name)
	if err != nil {
		return nil, fmt.Errorf("matrix: upload attachment %q: %w", path, err)
	}

	content := &event.MessageEventContent{
		MsgType:  msgTypeForMIME(mimeType),
		Body:     name,
		FileName: name,
		Info:     &event.FileInfo{MimeType: mimeType, Size: size},
	}
	if encrypted {
		content.File = &event.EncryptedFileInfo{EncryptedFile: *encFile, URL: up.ContentURI.CUString()}
	} else {
		content.URL = up.ContentURI.CUString()
	}
	return content, nil
}

// SendMedia delivers out.Attachments to the room out.Thread/out.To[0]
// resolves to (see resolveRoom in adapter.go, unchanged and shared with
// Send), one message event per file, in order. Each attachment is
// uploaded — encrypted client-side first when the room is encrypted (its
// event carries `file`, never `url`; an unencrypted room's carries `url`,
// never `file`) — and sent as its own m.image/m.video/m.audio/m.file
// event; only the first such event carries m.relates_to/m.in_reply_to
// when out.ReplyTo is set, the same "first attachment only" rule
// WhatsApp's SendMedia applies to its caption/quote. out.Body (if any) is
// sent as one further, separate m.text event after every attachment —
// chosen over an MSC2530 caption (attaching text to the media event
// itself) because MSC2530 is still unstable and unevenly supported across
// clients, while a plain trailing text message renders everywhere.
// core.Service picks this method over plain Send whenever out.Attachments
// is non-empty; a direct call with none falls back to Send.
func (a *Adapter) SendMedia(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	if len(out.Attachments) == 0 {
		return a.Send(ctx, out)
	}

	roomID, err := a.resolveRoom(ctx, out)
	if err != nil {
		return core.Receipt{}, err
	}

	encrypted, err := a.client.StateStore.IsEncrypted(ctx, roomID)
	if err != nil {
		return core.Receipt{}, fmt.Errorf("matrix: send media: check room encryption: %w", err)
	}

	var relatesTo *event.RelatesTo
	if out.ReplyTo != "" {
		_, _, replyEventID, err := parseItemID(out.ReplyTo)
		if err != nil {
			return core.Receipt{}, fmt.Errorf("matrix: reply target: %w", err)
		}
		relatesTo = &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: replyEventID}}
	}

	var receipt core.Receipt
	for i, path := range out.Attachments {
		content, err := a.buildMediaContent(ctx, path, encrypted)
		if err != nil {
			return core.Receipt{}, fmt.Errorf("matrix: send media: %w", err)
		}
		if i == 0 {
			content.RelatesTo = relatesTo
		}
		resp, err := a.client.SendMessageEvent(ctx, roomID, event.EventMessage, content)
		if err != nil {
			return core.Receipt{}, fmt.Errorf("matrix: send media to %s: %w", roomID, err)
		}
		receipt = a.sentReceipt(roomID, resp.EventID)
	}

	if out.Body != "" {
		resp, err := a.client.SendMessageEvent(ctx, roomID, event.EventMessage, &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    out.Body,
		})
		if err != nil {
			return core.Receipt{}, fmt.Errorf("matrix: send media text to %s: %w", roomID, err)
		}
		receipt = a.sentReceipt(roomID, resp.EventID)
	}

	return receipt, nil
}
