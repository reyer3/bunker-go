package core

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// avatarSize is both the width and height, in pixels, of every avatar
// bunker-go caches or generates: the fallback circle drawn here, and the
// resized thumbnail Service.Avatar caches for a fetched picture (see
// resizeThumbnail in avatar.go). It matches the doc's "≤96x96" cap.
const avatarSize = 96

// channelBrandColor mirrors cmd/bunker/render_style.go's channelColors
// (kept as a separate copy: core must not import cmd/bunker) so the
// generated fallback's circle agrees with the tmux/ANSI status line's
// per-channel accent.
var channelBrandColor = map[Channel]color.NRGBA{
	ChannelMail:     {R: 0x4d, G: 0xb0, B: 0xff, A: 0xff},
	ChannelWhatsApp: {R: 0x25, G: 0xd3, B: 0x66, A: 0xff},
	ChannelMatrix:   {R: 0x0d, G: 0xbd, B: 0x8b, A: 0xff},
}

// defaultBrandColor is used for a channel absent from channelBrandColor
// (none exist today, but a future channel would rather get a neutral
// color than an invisible one).
var defaultBrandColor = color.NRGBA{R: 0xa3, G: 0xa0, B: 0x9e, A: 0xff}

// generateAvatarPNG deterministically renders channel's brand-color
// circle with displayName's first letter or digit (uppercased) centered
// in white, encoded as an avatarSize x avatarSize PNG with a transparent
// background outside the circle. The same (channel, displayName) always
// produces byte-identical output: it never touches the network, a clock
// or any other non-deterministic input, so callers can cache it
// indefinitely and a test can assert on its bytes/pixels directly.
func generateAvatarPNG(channel Channel, displayName string) []byte {
	bg, ok := channelBrandColor[channel]
	if !ok {
		bg = defaultBrandColor
	}

	img := image.NewNRGBA(image.Rect(0, 0, avatarSize, avatarSize))
	center := float64(avatarSize) / 2
	radius := center
	for y := 0; y < avatarSize; y++ {
		for x := 0; x < avatarSize; x++ {
			dx := float64(x) + 0.5 - center
			dy := float64(y) + 0.5 - center
			if dx*dx+dy*dy <= radius*radius {
				img.SetNRGBA(x, y, bg)
			}
		}
	}

	if initial := avatarInitial(displayName); initial != 0 {
		drawAvatarInitial(img, initial)
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img) // only fails on a write error; buf.Write never fails.
	return buf.Bytes()
}

// avatarInitial returns the first letter or digit in name, uppercased, or
// 0 when name has none (empty, or made only of punctuation/symbols) — the
// caller then draws no initial at all, just the brand-color circle.
func avatarInitial(name string) rune {
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToUpper(r)
		}
	}
	return 0
}

// glyphScale enlarges basicfont's fixed 7x13 bitmap glyphs so a single
// character reads clearly at avatarSize instead of looking like a speck
// in the corner of a 96px circle.
const glyphScale = 4

// drawAvatarInitial centers r, rendered with golang.org/x/image/font/
// basicfont's Face7x13 (a pure-Go, no-external-font-file bitmap face —
// exactly what "the standard library or golang.org/x/image (pure Go)"
// allows per the feature doc), onto dst in solid white.
func drawAvatarInitial(dst draw.Image, r rune) {
	const glyphW, glyphH = 7, 13
	mask := image.NewAlpha(image.Rect(0, 0, glyphW, glyphH))
	drawer := &font.Drawer{
		Dst:  mask,
		Src:  image.NewUniform(color.Alpha{A: 0xff}),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(0, glyphH-3), // leaves a little descender room; every initial here is uppercase or a digit, neither of which uses it.
	}
	drawer.DrawString(string(r))

	scaledW, scaledH := glyphW*glyphScale, glyphH*glyphScale
	offsetX := (avatarSize - scaledW) / 2
	offsetY := (avatarSize - scaledH) / 2
	white := color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	for y := 0; y < glyphH; y++ {
		for x := 0; x < glyphW; x++ {
			if mask.AlphaAt(x, y).A == 0 {
				continue
			}
			for sy := 0; sy < glyphScale; sy++ {
				for sx := 0; sx < glyphScale; sx++ {
					dst.Set(offsetX+x*glyphScale+sx, offsetY+y*glyphScale+sy, white)
				}
			}
		}
	}
}
