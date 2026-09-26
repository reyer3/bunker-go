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
