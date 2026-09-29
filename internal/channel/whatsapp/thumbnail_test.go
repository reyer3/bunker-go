package whatsapp

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/reyer3/bunker-go/internal/core"
)

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// captureLogf records what toItem logs for the duration of the test.
func captureLogf(t *testing.T) *[]string {
	t.Helper()
	var lines []string
	old := logf
	logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { logf = old })
	return &lines
}

func mediaEvent(t *testing.T, id string, msg *waE2E.Message) *events.Message {
	t.Helper()
	chat := mustJID(t, "1234@s.whatsapp.net")
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            types.MessageID(id),
			Timestamp:     time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC),
		},
		Message: msg,
	}
}

func TestToItemKeepsEmbeddedThumbnail(t *testing.T) {
	jpg := encodeJPEG(t, 32, 24)
	cases := []struct {
		name string
		msg  *waE2E.Message
		want []byte
	}{
		{"image", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Mimetype: strPtr("image/jpeg"), DirectPath: strPtr("/v/img"), JPEGThumbnail: jpg,
		}}, jpg},
		{"video", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			Mimetype: strPtr("video/mp4"), DirectPath: strPtr("/v/vid"), JPEGThumbnail: jpg,
			// A larger thumbnail behind its own media path is not
			// fetched: only the inline bytes are kept.
			ThumbnailDirectPath: strPtr("/v/vid-thumb"),
		}}, jpg},
		{"document", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			Mimetype: strPtr("application/pdf"), FileName: strPtr("informe.pdf"), DirectPath: strPtr("/v/doc"), JPEGThumbnail: jpg,
		}}, jpg},
	}
	pngThumb := encodePNG(t, 16, 16)
	cases = append(cases, struct {
		name string
		msg  *waE2E.Message
		want []byte
	}{"sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
		Mimetype: strPtr("image/webp"), DirectPath: strPtr("/v/stk"), PngThumbnail: pngThumb,
	}}, pngThumb})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logged := captureLogf(t)
			item := toItem("personal", mediaEvent(t, "M-"+tc.name, tc.msg))
			if len(item.Attachments) != 1 {
				t.Fatalf("Attachments = %+v, want one", item.Attachments)
			}
			if got := item.Attachments[0].Thumbnail; !bytes.Equal(got, tc.want) {
				t.Errorf("Thumbnail = %d bytes, want the message's own %d", len(got), len(tc.want))
			}
			if len(*logged) != 0 {
				t.Errorf("logged %q for a valid thumbnail", *logged)
			}
		})
	}
}

func TestToItemWithoutThumbnailIsUnchanged(t *testing.T) {
	logged := captureLogf(t)
	item := toItem("personal", mediaEvent(t, "M-plain", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Mimetype: strPtr("image/jpeg"), FileLength: uint64Ptr(2048), DirectPath: strPtr("/v/img"),
	}}))
	want := core.Attachment{Name: "image", MIME: "image/jpeg", Size: 2048, Ref: "/v/img"}
	if len(item.Attachments) != 1 || !item.Attachments[0].Equal(want) {
		t.Fatalf("Attachments = %+v, want [%+v]", item.Attachments, want)
	}
	if len(*logged) != 0 {
		t.Errorf("logged %q for a message without a thumbnail", *logged)
	}
}

func TestToItemIgnoresBadThumbnail(t *testing.T) {
	cases := []struct {
		name  string
		thumb []byte
		want  string
	}{
		{"oversized", append(encodeJPEG(t, 8, 8), make([]byte, core.MaxThumbnailBytes)...), "too large"},
		{"not an image", []byte("no es una imagen"), "decode thumbnail"},
		{"too many pixels", encodePNG(t, maxThumbnailSide+1, 1), "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logged := captureLogf(t)
			item := toItem("personal", mediaEvent(t, "M-bad", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
				Mimetype: strPtr("video/mp4"), DirectPath: strPtr("/v/vid"), JPEGThumbnail: tc.thumb,
			}}))
			// The message itself is kept: only the preview is lost.
			if len(item.Attachments) != 1 || item.Attachments[0].Ref != "/v/vid" {
				t.Fatalf("Attachments = %+v, want the video kept", item.Attachments)
			}
			if item.Attachments[0].Thumbnail != nil {
				t.Errorf("Thumbnail = %d bytes, want it dropped", len(item.Attachments[0].Thumbnail))
			}
			if len(*logged) != 1 || !strings.Contains((*logged)[0], tc.want) || !strings.Contains((*logged)[0], item.ID) {
				t.Errorf("logged %q, want one line naming %q and the item", *logged, tc.want)
			}
		})
	}
}

// TestRunStoresEmbeddedThumbnailWithoutDownloading drives a live event
// through the fake client: the thumbnail reaches the sink, and nothing
// is downloaded to get it (a normal client would not fetch media just to
// list a chat).
func TestRunStoresEmbeddedThumbnailWithoutDownloading(t *testing.T) {
	cli := newFakeWAClient()
	cli.linked = true
	sink := newSpySink()
	a := newTestAdapter("personal", cli)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx, sink)
	waitFor(t, func() bool { return cli.IsConnected() })

	jpg := encodeJPEG(t, 32, 24)
	cli.emit(mediaEvent(t, "M-live-thumb", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Mimetype: strPtr("image/jpeg"), FileLength: uint64Ptr(4096), DirectPath: strPtr("/v/img"), JPEGThumbnail: jpg,
	}}))
	cli.emit(mediaEvent(t, "M-live-plain", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Mimetype: strPtr("image/jpeg"), FileLength: uint64Ptr(4096), DirectPath: strPtr("/v/img2"),
	}}))
	waitFor(t, func() bool { return len(sink.items()) == 2 })

	byRef := map[string]core.Attachment{}
	for _, item := range sink.items() {
		for _, att := range item.Attachments {
			byRef[att.Ref] = att
		}
	}
	if got := byRef["/v/img"].Thumbnail; !bytes.Equal(got, jpg) {
		t.Errorf("stored Thumbnail = %d bytes, want the embedded %d", len(got), len(jpg))
	}
	if got := byRef["/v/img2"].Thumbnail; got != nil {
		t.Errorf("stored Thumbnail = %d bytes, want none for a message without one", len(got))
	}
	cli.mu.Lock()
	downloaded := cli.lastDownload
	cli.mu.Unlock()
	if downloaded != nil {
		t.Errorf("media was downloaded (%T) just to store a preview", downloaded)
	}
}
