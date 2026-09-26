package core

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// solidPNG encodes a wxh image filled with c as PNG, for resize tests
// where a single, easily-asserted color makes nearest-neighbor sampling's
// output trivial to check.
func solidPNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode %dx%d test PNG: %v", w, h, err)
	}
	return buf.Bytes()
}

func TestNearestNeighborResizeProducesExactRequestedDimensions(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 200, 100))
	dst := nearestNeighborResize(src, avatarSize, avatarSize)
	if got := dst.Bounds(); got.Dx() != avatarSize || got.Dy() != avatarSize {
		t.Fatalf("resize bounds = %v, want %dx%d", got, avatarSize, avatarSize)
	}
}

func TestResizeToAvatarPNGShrinksAnOversizedImageToAvatarSize(t *testing.T) {
	c := color.NRGBA{R: 200, G: 100, B: 50, A: 255}
	data := solidPNG(t, 300, 300, c)

	out, err := resizeToAvatarPNG(data)
	if err != nil {
		t.Fatalf("resizeToAvatarPNG() error = %v", err)
	}

	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode resized PNG: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() > avatarSize || bounds.Dy() > avatarSize {
		t.Fatalf("resized bounds = %v, want at most %dx%d", bounds, avatarSize, avatarSize)
	}
	// A solid-color source survives nearest-neighbor sampling exactly:
	// every output pixel should still be c.
	r, g, b, a := img.At(bounds.Min.X, bounds.Min.Y).RGBA()
	if uint8(r>>8) != c.R || uint8(g>>8) != c.G || uint8(b>>8) != c.B || uint8(a>>8) != c.A {
		t.Errorf("resized corner pixel = (%d,%d,%d,%d), want %+v", r>>8, g>>8, b>>8, a>>8, c)
	}
}

func TestResizeToAvatarPNGLeavesASmallerImageUntouched(t *testing.T) {
	data := solidPNG(t, 40, 40, color.NRGBA{R: 1, G: 2, B: 3, A: 255})

	out, err := resizeToAvatarPNG(data)
	if err != nil {
		t.Fatalf("resizeToAvatarPNG() error = %v", err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	if bounds := img.Bounds(); bounds.Dx() != 40 || bounds.Dy() != 40 {
		t.Errorf("bounds = %v, want unchanged 40x40 (never upsample)", bounds)
	}
}

func TestResizeToAvatarPNGReturnsErrorForUndecodableData(t *testing.T) {
	if _, err := resizeToAvatarPNG([]byte("not an image")); err == nil {
		t.Fatal("resizeToAvatarPNG() error = nil, want a decode error")
	}
}
