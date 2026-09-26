package core

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func decodePNG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode generated avatar PNG: %v", err)
	}
	return img
}

func TestGenerateAvatarPNGIsDeterministicAndBounded(t *testing.T) {
	a := generateAvatarPNG(ChannelWhatsApp, "Widget Team")
	b := generateAvatarPNG(ChannelWhatsApp, "Widget Team")
	if !bytes.Equal(a, b) {
		t.Fatalf("generateAvatarPNG(%q) is not deterministic: two calls produced different bytes", "Widget Team")
	}

	img := decodePNG(t, a)
	bounds := img.Bounds()
	if bounds.Dx() > avatarSize || bounds.Dy() > avatarSize {
		t.Fatalf("generated avatar is %dx%d, want at most %dx%d", bounds.Dx(), bounds.Dy(), avatarSize, avatarSize)
	}
}

func TestGenerateAvatarPNGDrawsAWhiteInitialOverTheBrandCircle(t *testing.T) {
	img := decodePNG(t, generateAvatarPNG(ChannelWhatsApp, "Widget Team"))
	bounds := img.Bounds()

	var sawWhite, sawBrand, sawTransparentCorner bool
	brand := channelBrandColor[ChannelWhatsApp]
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			switch {
			case a == 0:
				continue
			case r>>8 == 0xff && g>>8 == 0xff && b>>8 == 0xff:
				sawWhite = true
			case uint8(r>>8) == brand.R && uint8(g>>8) == brand.G && uint8(b>>8) == brand.B:
				sawBrand = true
			}
		}
	}
	if r, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatalf("corner pixel (0,0) = alpha %d (rgba r=%d), want fully transparent (outside the circle)", a, r)
	} else {
		sawTransparentCorner = true
	}

	if !sawWhite {
		t.Error("generated avatar has no white pixel: the initial letter was not drawn")
	}
	if !sawBrand {
		t.Error("generated avatar has no brand-color pixel: the circle background was not drawn")
	}
	if !sawTransparentCorner {
		t.Error("generated avatar corner is not transparent")
	}
}

func TestGenerateAvatarPNGUsesADifferentBrandColorPerChannel(t *testing.T) {
	mail := decodePNG(t, generateAvatarPNG(ChannelMail, "Inbox"))
	wa := decodePNG(t, generateAvatarPNG(ChannelWhatsApp, "Inbox"))

	center := avatarSize / 2
	// The exact center pixel may be covered by the initial letter; sample a
	// point inside the circle but away from a single centered glyph.
	mr, mg, mb, _ := mail.At(6, center).RGBA()
	wr, wg, wb, _ := wa.At(6, center).RGBA()
	if mr>>8 == wr>>8 && mg>>8 == wg>>8 && mb>>8 == wb>>8 {
		t.Error("mail and whatsapp generated avatars use the same background color, want distinct per-channel brand colors")
	}
}

func TestGenerateAvatarPNGVariesWithDisplayName(t *testing.T) {
	a := generateAvatarPNG(ChannelMatrix, "Alice")
	b := generateAvatarPNG(ChannelMatrix, "Bob")
	if bytes.Equal(a, b) {
		t.Error("generateAvatarPNG produced identical bytes for different display names' initials")
	}
}

func TestAvatarInitialPicksFirstLetterOrDigitUppercased(t *testing.T) {
	cases := map[string]rune{
		"widget team": 'W',
		"123 group":   '1',
		"":            0,
		"!!!":         0,
	}

	for name, want := range cases {
		if got := avatarInitial(name); got != want {
			t.Errorf("avatarInitial(%q) = %q, want %q", name, got, want)
		}
	}
}
