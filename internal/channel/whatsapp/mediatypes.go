package whatsapp

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"

	"github.com/reyer3/bunker-go/internal/core"
)

// T16(a): WhatsApp media beyond images. maxMediaBytes is WhatsApp's
// documented upload limit shared by image, video and audio messages (see
// also maxImageBytes in media.go, which predates this file and stays as
// the name buildImageMessage itself reaches for). maxDocumentBytes is the
// looser limit for the catch-all DocumentMessage.
const (
	maxMediaBytes    = maxImageBytes
	maxDocumentBytes = 100 * 1024 * 1024 // 100 MB
)

// videoExtensionMIME and audioExtensionMIME map a lowercased file
// extension to the exact MIME type embedded in the outgoing message.
// buildMediaMessage's dispatch is extension-based, not content-sniffed:
// WhatsApp media types are container formats a caller names by extension
// (unlike core.Service's own MIME sniffing, which only needs a rough
// type/size check for AttachmentPolicy validation, done independently in
// internal/core/service.go).
var imageExtensionMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
}

var videoExtensionMIME = map[string]string{
	".mp4":  "video/mp4",
	".3gp":  "video/3gpp",
	".3gpp": "video/3gpp",
}

var audioExtensionMIME = map[string]string{
	".ogg":  "audio/ogg",
	".opus": "audio/ogg",
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
}

// mediaAttachmentPolicy is the AttachmentPolicy every media type in this
// file (plus the existing image types) contributes to: image/video/audio
// at the WhatsApp per-message upload limit, and AnyMIME as the catch-all
// for every other type, sent as a DocumentMessage at WhatsApp's looser
// document limit.
func mediaAttachmentPolicy() core.AttachmentPolicy {
	limits := make(map[string]int64, len(imageExtensionMIME)+len(videoExtensionMIME)+len(audioExtensionMIME))
	for _, mime := range imageExtensionMIME {
		limits[mime] = maxMediaBytes
	}
	for _, mime := range videoExtensionMIME {
		limits[mime] = maxMediaBytes
	}
	for _, mime := range audioExtensionMIME {
		limits[mime] = maxMediaBytes
	}
	limits[core.AnyMIME] = maxDocumentBytes
	return core.AttachmentPolicy{MaxBytes: limits}
}

// readMediaFile stats path, rejects it outright when over max (so an
// oversized file is never even read into memory, let alone uploaded), and
// returns its full contents. It backs buildVideoMessage, buildAudioMessage
// and buildDocumentMessage; buildImageMessage (media.go) keeps its own
// pre-existing stat+read, unchanged.
func readMediaFile(path string, max int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read media %q: %w", path, err)
	}
	if info.Size() > max {
		return nil, fmt.Errorf("whatsapp: media %q is %d bytes, over the %d byte limit", path, info.Size(), max)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read media %q: %w", path, err)
	}
	return data, nil
}

// buildVideoMessage uploads path as MediaVideo and wraps the result in a
// waE2E.Message carrying caption, capped at maxMediaBytes.
func (a *Adapter) buildVideoMessage(ctx context.Context, path, caption, mimeType string) (*waE2E.Message, error) {
	data, err := readMediaFile(path, maxMediaBytes)
	if err != nil {
		return nil, err
	}
	up, err := a.cli.Upload(ctx, data, whatsmeow.MediaVideo)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: upload media %q: %w", path, err)
	}
	return &waE2E.Message{
		VideoMessage: &waE2E.VideoMessage{
			Caption:       ptrString(caption),
			Mimetype:      ptrString(mimeType),
			URL:           ptrString(up.URL),
			DirectPath:    ptrString(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    ptrUint64(up.FileLength),
		},
	}, nil
}

// buildAudioMessage uploads path as MediaAudio and wraps the result in a
// non-PTT (not a voice note) waE2E.Message, capped at maxMediaBytes.
// waE2E.AudioMessage has no Caption field at all — a real WhatsApp
// protocol constraint, not an omission here — so SendMedia sends out.Body
// as a separate text message right after a first-attachment audio.
func (a *Adapter) buildAudioMessage(ctx context.Context, path, mimeType string) (*waE2E.Message, error) {
	data, err := readMediaFile(path, maxMediaBytes)
	if err != nil {
		return nil, err
	}
	up, err := a.cli.Upload(ctx, data, whatsmeow.MediaAudio)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: upload media %q: %w", path, err)
	}
	return &waE2E.Message{
		AudioMessage: &waE2E.AudioMessage{
			PTT:           ptrBool(false),
			Mimetype:      ptrString(mimeType),
			URL:           ptrString(up.URL),
			DirectPath:    ptrString(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    ptrUint64(up.FileLength),
		},
	}, nil
}

// buildDocumentMessage is WhatsApp's catch-all for every attachment whose
// extension buildMediaMessage does not recognize as image/video/audio: it
// uploads path as MediaDocument and wraps the result in a waE2E.Message
// naming the file (FileName and Title, both the base name) and its
// content-sniffed Mimetype (falling back to the file extension, the same
// two-step core.Service's own inspectAttachment uses), capped at the
// looser maxDocumentBytes.
func (a *Adapter) buildDocumentMessage(ctx context.Context, path, caption string) (*waE2E.Message, error) {
	data, err := readMediaFile(path, maxDocumentBytes)
	if err != nil {
		return nil, err
	}
	mimeType := http.DetectContentType(data)
	if mimeType == "application/octet-stream" {
		if guessed := mime.TypeByExtension(filepath.Ext(path)); guessed != "" {
			mimeType = guessed
		}
	}
	name := filepath.Base(path)
	up, err := a.cli.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: upload media %q: %w", path, err)
	}
	return &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			Caption:       ptrString(caption),
			Mimetype:      ptrString(mimeType),
			Title:         ptrString(name),
			FileName:      ptrString(name),
			URL:           ptrString(up.URL),
			DirectPath:    ptrString(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    ptrUint64(up.FileLength),
		},
	}, nil
}

// buildMediaMessage is SendMedia's single dispatch point (T16a): it
// classifies path by its lowercased file extension and builds the
// matching waE2E message — image (delegating to buildImageMessage,
// unchanged, shared with PostStatus), video, audio (non-PTT), or, for
// every other extension, a document. This is the only place SendMedia's
// loop decides which message type an attachment becomes.
func (a *Adapter) buildMediaMessage(ctx context.Context, path, caption string) (*waE2E.Message, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if _, ok := imageExtensionMIME[ext]; ok {
		return a.buildImageMessage(ctx, path, caption)
	}
	if mimeType, ok := videoExtensionMIME[ext]; ok {
		return a.buildVideoMessage(ctx, path, caption, mimeType)
	}
	if mimeType, ok := audioExtensionMIME[ext]; ok {
		return a.buildAudioMessage(ctx, path, mimeType)
	}
	return a.buildDocumentMessage(ctx, path, caption)
}

// setContextInfo attaches ctxInfo (a reply's quote of the original
// message) to whichever media message type buildMediaMessage built for
// the first attachment — image, video, audio or document are the only
// ones SendMedia ever produces. It replaces the direct
// msg.ImageMessage.ContextInfo assignment SendMedia used before every
// attachment was necessarily an image.
func setContextInfo(msg *waE2E.Message, ctxInfo *waE2E.ContextInfo) {
	switch {
	case msg.ImageMessage != nil:
		msg.ImageMessage.ContextInfo = ctxInfo
	case msg.VideoMessage != nil:
		msg.VideoMessage.ContextInfo = ctxInfo
	case msg.AudioMessage != nil:
		msg.AudioMessage.ContextInfo = ctxInfo
	case msg.DocumentMessage != nil:
		msg.DocumentMessage.ContextInfo = ctxInfo
	}
}

func ptrBool(b bool) *bool { return &b }
