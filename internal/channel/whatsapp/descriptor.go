package whatsapp

import (
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
)

// mediaDescriptor is WhatsApp's media download descriptor for one
// attachment (D2): DirectPath/MediaKey/FileSHA256/FileEncSHA256 plus
// enough metadata to pick whatsmeow's decryption scheme. It is
// persisted as JSON through core.Sink's Cursor/SetCursor — 0600
// adapter-private state, never exposed through core.Item, RPC or JSON
// API output — and implements whatsmeow's DownloadableMessage and
// MediaTypeable interfaces directly, so DownloadAttachment can hand a
// stored descriptor straight to cli.Download without reconstructing a
// waE2E message.
type mediaDescriptor struct {
	DirectPath    string `json:"direct_path"`
	MediaKey      []byte `json:"media_key"`
	FileSHA256    []byte `json:"file_sha256"`
	FileEncSHA256 []byte `json:"file_enc_sha256"`
	Mimetype      string `json:"mimetype"`
	FileLength    uint64 `json:"file_length"`
	// Kind is one of "image", "video", "audio", "document" or "sticker",
	// mirroring bodyAndMedia's dispatch (message.go); it picks the
	// whatsmeow.MediaType GetMediaType returns.
	Kind string `json:"kind"`
}

func (d mediaDescriptor) GetDirectPath() string    { return d.DirectPath }
func (d mediaDescriptor) GetMediaKey() []byte      { return d.MediaKey }
func (d mediaDescriptor) GetFileSHA256() []byte    { return d.FileSHA256 }
func (d mediaDescriptor) GetFileEncSHA256() []byte { return d.FileEncSHA256 }
func (d mediaDescriptor) GetMediaType() whatsmeow.MediaType {
	return mediaKindType[d.Kind]
}

// mediaKindType maps a persisted Kind to whatsmeow's MediaType, the same
// way whatsmeow's own classToMediaType maps a waE2E message's proto name
// (stickers download with the image key, like whatsmeow's own map).
var mediaKindType = map[string]whatsmeow.MediaType{
	"image":    whatsmeow.MediaImage,
	"video":    whatsmeow.MediaVideo,
	"audio":    whatsmeow.MediaAudio,
	"document": whatsmeow.MediaDocument,
	"sticker":  whatsmeow.MediaImage,
}

var (
	_ whatsmeow.DownloadableMessage = mediaDescriptor{}
	_ whatsmeow.MediaTypeable       = mediaDescriptor{}
)

// descriptorFromMessage extracts a mediaDescriptor from msg's media
// sub-message (image/video/audio/document/sticker), mirroring
// bodyAndMedia's dispatch in message.go but returning the decryption
// material mediaMeta deliberately never carries. ok is false when msg
// carries no downloadable media at all (text, receipts, app-state
// notifications, ...).
func descriptorFromMessage(msg *waE2E.Message) (mediaDescriptor, bool) {
	if msg == nil {
		return mediaDescriptor{}, false
	}
	if img := msg.GetImageMessage(); img != nil {
		return mediaDescriptor{
			DirectPath: img.GetDirectPath(), MediaKey: img.GetMediaKey(),
			FileSHA256: img.GetFileSHA256(), FileEncSHA256: img.GetFileEncSHA256(),
			Mimetype: img.GetMimetype(), FileLength: img.GetFileLength(), Kind: "image",
		}, true
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return mediaDescriptor{
			DirectPath: vid.GetDirectPath(), MediaKey: vid.GetMediaKey(),
			FileSHA256: vid.GetFileSHA256(), FileEncSHA256: vid.GetFileEncSHA256(),
			Mimetype: vid.GetMimetype(), FileLength: vid.GetFileLength(), Kind: "video",
		}, true
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		return mediaDescriptor{
			DirectPath: doc.GetDirectPath(), MediaKey: doc.GetMediaKey(),
			FileSHA256: doc.GetFileSHA256(), FileEncSHA256: doc.GetFileEncSHA256(),
			Mimetype: doc.GetMimetype(), FileLength: doc.GetFileLength(), Kind: "document",
		}, true
	}
	if aud := msg.GetAudioMessage(); aud != nil {
		return mediaDescriptor{
			DirectPath: aud.GetDirectPath(), MediaKey: aud.GetMediaKey(),
			FileSHA256: aud.GetFileSHA256(), FileEncSHA256: aud.GetFileEncSHA256(),
			Mimetype: aud.GetMimetype(), FileLength: aud.GetFileLength(), Kind: "audio",
		}, true
	}
	if sticker := msg.GetStickerMessage(); sticker != nil {
		return mediaDescriptor{
			DirectPath: sticker.GetDirectPath(), MediaKey: sticker.GetMediaKey(),
			FileSHA256: sticker.GetFileSHA256(), FileEncSHA256: sticker.GetFileEncSHA256(),
			Mimetype: sticker.GetMimetype(), FileLength: sticker.GetFileLength(), Kind: "sticker",
		}, true
	}
	return mediaDescriptor{}, false
}

// mediaDescriptorKey is the core.Sink cursor key persisting item id's
// attachment at index's download descriptor: 0600 state private to this
// adapter (see internal/store/store.go's cursors table), never a
// core.Item field, so it can never reach RPC or JSON output.
func mediaDescriptorKey(account, itemID string, index int) string {
	return fmt.Sprintf("whatsapp:%s:media:%s:%d", account, itemID, index)
}
