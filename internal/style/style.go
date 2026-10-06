// Package style holds the presentation constants `bunker render` and the
// interactive TUI (internal/tui) share: brand accent colors, styled Nerd
// Font channel glyphs, and config-driven glyph overrides. It depends only
// on internal/core, so both cmd/bunker (a package main) and internal/tui
// (which must never import store or channel adapters) can import it.
package style

import "github.com/reyer3/bunker-go/internal/core"

// Brand accent colors, also used by `bunker render --tmux/--ansi`: the
// tmux theme's blue for mail, WhatsApp green, Element green for Matrix.
// ColorDim marks an empty/zero channel or a de-emphasized (dim) element.
const (
	ColorMail     = "#4db0ff"
	ColorWhatsApp = "#25d366"
	ColorMatrix   = "#0dbd8b"
	ColorDim      = "#a3a09e"
)

// ChannelColors maps each channel to its brand accent.
var ChannelColors = map[core.Channel]string{
	core.ChannelMail:     ColorMail,
	core.ChannelWhatsApp: ColorWhatsApp,
	core.ChannelMatrix:   ColorMatrix,
}

// Glyphs are Nerd Font icons (md-email, md-whatsapp, md-matrix; all from
// the Material Design set so they render from the same font range).
var Glyphs = map[core.Channel]string{
	core.ChannelMail:     "\U000f01ee",
	core.ChannelWhatsApp: "\U000f05a3",
	core.ChannelMatrix:   "\U000f0628",
}

// ResolveGlyphs returns Glyphs with config overrides applied (keys:
// "mail", "whatsapp", "matrix"); unknown keys and empty values are
// ignored, and the package defaults are never mutated.
func ResolveGlyphs(overrides map[string]string) map[core.Channel]string {
	out := make(map[core.Channel]string, len(Glyphs))
	for ch, g := range Glyphs {
		out[ch] = g
	}
	for key, g := range overrides {
		ch := core.Channel(key)
		if _, known := out[ch]; known && g != "" {
			out[ch] = g
		}
	}
	return out
}

// Palette is a resolved set of accent colors: one per channel plus the dim
// color. The zero value is usable and yields the package defaults, so a
// caller that never loaded config still renders the brand colors.
type Palette struct {
	// Channels maps each channel to its accent ("#rrggbb").
	Channels map[core.Channel]string
	// Dim marks empty/zero channels and de-emphasized elements.
	Dim string
}

// DefaultPalette returns a fresh copy of the brand accents and ColorDim.
func DefaultPalette() Palette {
	p := Palette{Channels: make(map[core.Channel]string, len(ChannelColors)), Dim: ColorDim}
	for ch, hex := range ChannelColors {
		p.Channels[ch] = hex
	}
	return p
}

// Channel returns ch's accent, falling back to the brand default.
func (p Palette) Channel(ch core.Channel) string {
	if hex, ok := p.Channels[ch]; ok && hex != "" {
		return hex
	}
	return ChannelColors[ch]
}

// DimColor returns the dim color, falling back to ColorDim.
func (p Palette) DimColor() string {
	if p.Dim != "" {
		return p.Dim
	}
	return ColorDim
}

// paletteDimKey is the [render.colors] key for the dim color; every other
// recognized key is a channel name.
const paletteDimKey = "dim"

// ResolvePalette returns DefaultPalette with config overrides applied
// (keys: "mail", "whatsapp", "matrix", "dim"; values "#rrggbb"). Unknown
// keys and malformed values are ignored, and the package defaults are
// never mutated.
func ResolvePalette(overrides map[string]string) Palette {
	p := DefaultPalette()
	for key, hex := range overrides {
		if !isHexColor(hex) {
			continue
		}
		if key == paletteDimKey {
			p.Dim = hex
			continue
		}
		ch := core.Channel(key)
		if _, known := p.Channels[ch]; known {
			p.Channels[ch] = hex
		}
	}
	return p
}

// isHexColor reports whether s is exactly "#rrggbb" (either letter case).
func isHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
