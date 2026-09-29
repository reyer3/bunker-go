package tui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/kittygfx"
)

// Issue #18: a message's own embedded thumbnail previews it without a
// download or ffmpeg.

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return buf.Bytes()
}

func TestEmbeddedThumbnailPriority(t *testing.T) {
	thumb := []byte{0xff, 0xd8, 0xff}
	cases := []struct {
		name string
		a    core.Attachment
		full bool
		want bool
	}{
		{"image thumbnail", core.Attachment{MIME: "image/jpeg", Thumbnail: thumb}, false, true},
		{"video thumbnail", core.Attachment{MIME: "video/mp4", Thumbnail: thumb}, false, true},
		{"document thumbnail", core.Attachment{MIME: "application/pdf", Thumbnail: thumb}, false, true},
		{"no thumbnail", core.Attachment{MIME: "image/jpeg"}, false, false},
		{"oversized thumbnail", core.Attachment{MIME: "image/jpeg", Thumbnail: make([]byte, core.MaxThumbnailBytes+1)}, false, false},
		// The full-size view of a photo or video shows the real media.
		{"full image", core.Attachment{MIME: "image/jpeg", Thumbnail: thumb}, true, false},
		{"full video", core.Attachment{MIME: "video/mp4", Thumbnail: thumb}, true, false},
		// A document has no real image: its thumbnail is all there is.
		{"full document", core.Attachment{MIME: "application/pdf", Thumbnail: thumb}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := embeddedThumbnail(tc.a, tc.full)
			if ok != tc.want {
				t.Fatalf("embeddedThumbnail ok = %v, want %v", ok, tc.want)
			}
			if ok && !bytes.Equal(got, tc.a.Thumbnail) {
				t.Errorf("embeddedThumbnail = %v, want the attachment's own bytes", got)
			}
		})
	}
}

func TestChatImageFromEmbeddedThumbnailSkipsDownload(t *testing.T) {
	model, client, out := imageChatModelWith(t, kittygfx.Kitty,
		core.Attachment{Name: "foto.jpg", MIME: "image/jpeg", Size: 1234, Thumbnail: testJPEG(t, 64, 48)})
	if len(client.downloads) != 0 {
		t.Fatalf("downloads = %v, want none: the embedded thumbnail is enough", client.downloads)
	}
	if !strings.Contains(out.String(), "\x1b_Ga=T,U=1") {
		t.Fatal("thumbnail was not uploaded to the terminal")
	}
	if !strings.ContainsRune(model.View(), kittygfx.Placeholder) {
		t.Fatal("chat view shows no image placeholder")
	}
}

func TestChatVideoFromEmbeddedThumbnailSkipsFFmpeg(t *testing.T) {
	old := videoFrame
	videoFrame = func(context.Context, string) ([]byte, error) {
		t.Error("ffmpeg ran although the message carried a thumbnail")
		return nil, errors.New("unexpected")
	}
	t.Cleanup(func() { videoFrame = old })

	model, client, _ := imageChatModelWith(t, kittygfx.Kitty,
		core.Attachment{Name: "clip.mp4", MIME: "video/mp4", Size: 4096, Thumbnail: testJPEG(t, 64, 36)})
	if len(client.downloads) != 0 {
		t.Fatalf("downloads = %v, want none", client.downloads)
	}
	view := model.View()
	if !strings.ContainsRune(view, kittygfx.Placeholder) || !strings.Contains(view, "▶ clip.mp4") {
		t.Fatalf("video bubble lacks its thumbnail and ▶ caption:\n%s", view)
	}
}

func TestChatDocumentFromEmbeddedThumbnail(t *testing.T) {
	model, client, _ := imageChatModelWith(t, kittygfx.Kitty,
		core.Attachment{Name: "informe.pdf", MIME: "application/pdf", Size: 999, Thumbnail: testJPEG(t, 48, 64)})
	if len(client.downloads) != 0 {
		t.Fatalf("downloads = %v, want none", client.downloads)
	}
	view := model.View()
	if !strings.ContainsRune(view, kittygfx.Placeholder) || !strings.Contains(view, "informe.pdf") {
		t.Fatalf("document bubble lacks its thumbnail and name:\n%s", view)
	}
}

func TestChatInvalidEmbeddedThumbnailFallsBackToDownload(t *testing.T) {
	model, client, _ := imageChatModelWith(t, kittygfx.Kitty,
		core.Attachment{Name: "foto.jpg", MIME: "image/jpeg", Size: 1234, Thumbnail: []byte("no es una imagen")})
	if len(client.downloads) != 1 {
		t.Fatalf("downloads = %v, want one: an undecodable thumbnail falls back", client.downloads)
	}
	if !strings.ContainsRune(model.View(), kittygfx.Placeholder) {
		t.Fatal("chat view shows no image placeholder after the fallback")
	}
}

func TestChatDocumentWithoutThumbnailIsNotPreviewed(t *testing.T) {
	model, client, _ := imageChatModelWith(t, kittygfx.Kitty,
		core.Attachment{Name: "informe.pdf", MIME: "application/pdf", Size: 999})
	if len(client.downloads) != 0 {
		t.Fatalf("downloads = %v, want none for a plain document", client.downloads)
	}
	view := model.View()
	if strings.ContainsRune(view, kittygfx.Placeholder) || !strings.Contains(view, "📎 informe.pdf") {
		t.Fatalf("plain document should stay a text row:\n%s", view)
	}
}
