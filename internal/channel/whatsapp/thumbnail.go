package whatsapp

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // registered for image.DecodeConfig: JPEGThumbnail
	_ "image/png"  // registered for image.DecodeConfig: sticker PngThumbnail
	"log"

	"github.com/reyer3/bunker-go/internal/core"
)

// Embedded thumbnails (issue #18): image, video and document messages
// carry a small JPEG preview inline (JPEGThumbnail) and stickers a PNG
// one (PngThumbnail). Keeping it on the Attachment lets the TUI preview
// the media without downloading it. Only the inline bytes are used: a
// video's larger thumbnailDirectPath is another media download, which a
// real client does not make just to list a chat.

// maxThumbnailSide bounds an embedded thumbnail's decoded dimensions. The
// inline previews WhatsApp sends are well under this; a larger header
// means the bytes are not a thumbnail (and would cost memory to decode).
const maxThumbnailSide = 1024

// logf is where a rejected thumbnail is reported. A variable so tests can
// capture it instead of swapping the global logger.
var logf = log.Printf

// validThumbnail returns thumb when it is a small, decodable JPEG or PNG,
// and nil otherwise. It only reads the image header: rendering is the
// client's business, this just keeps junk out of the store.
func validThumbnail(thumb []byte) ([]byte, error) {
	if len(thumb) == 0 {
		return nil, nil
	}
	if len(thumb) > core.MaxThumbnailBytes {
		return nil, fmt.Errorf("whatsapp: thumbnail too large: %d bytes (max %d)", len(thumb), core.MaxThumbnailBytes)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(thumb))
	if err != nil {
		return nil, fmt.Errorf("whatsapp: decode thumbnail: %w", err)
	}
	if format != "jpeg" && format != "png" {
		return nil, fmt.Errorf("whatsapp: thumbnail format %q not supported", format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxThumbnailSide || cfg.Height > maxThumbnailSide {
		return nil, fmt.Errorf("whatsapp: thumbnail size %dx%d out of range", cfg.Width, cfg.Height)
	}
	// Copy: the protobuf message owns thumb's backing array.
	return bytes.Clone(thumb), nil
}
