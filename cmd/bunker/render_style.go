package main

import (
	"fmt"
	"strings"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/style"
)

// renderStyle selects how render prints its segment line.
type renderStyle int

const (
	renderPlain renderStyle = iota // "✉ 3  💬 5  ⌘ 2", the documented default
	renderTmux                     // tmux #[fg=...] colors and Nerd Font glyphs
	renderANSI                     // 24-bit ANSI colors and Nerd Font glyphs
)

// styledGlyphs are Nerd Font icons (md-email, md-whatsapp, md-matrix; all
// from the Material Design set so they render from the same font range)
// used by the colored styles; plain output keeps channelGlyphs. Shared
// with internal/tui via internal/style so both surfaces use the same
// icons.
var styledGlyphs = style.Glyphs

// channelColors are the per-channel accents: the tmux theme's blue for mail,
// WhatsApp green, Element green for Matrix. zeroColor dims empty channels.
// Shared with internal/tui via internal/style.
var channelColors = style.ChannelColors

const zeroColor = style.ColorDim

// formatRender renders segments in style. With hideEmpty, it returns ""
// when no channel has unread items, so a status line can disappear.
func formatRender(segments []renderSegment, style renderStyle, hideEmpty bool, glyphs map[core.Channel]string) string {
	if hideEmpty {
		any := false
		for _, seg := range segments {
			if seg.Unread > 0 {
				any = true
				break
			}
		}
		if !any {
			return ""
		}
	}

	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		if style == renderPlain {
			parts = append(parts, fmt.Sprintf("%s %d", seg.Glyph, seg.Unread))
			continue
		}
		color := channelColors[seg.Channel]
		if seg.Unread == 0 {
			color = zeroColor
		}
		text := fmt.Sprintf("%s %d", glyphs[seg.Channel], seg.Unread)
		if style == renderTmux {
			parts = append(parts, fmt.Sprintf("#[fg=%s]%s#[default]", color, text))
		} else {
			r, g, b := hexRGB(color)
			parts = append(parts, fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[0m", r, g, b, text))
		}
	}
	return strings.Join(parts, "  ")
}

// hexRGB parses a "#rrggbb" color; malformed input yields black.
func hexRGB(hex string) (r, g, b int) {
	fmt.Sscanf(strings.TrimPrefix(hex, "#"), "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

// resolveGlyphs returns the styled glyphs with config overrides applied
// (keys: "mail", "whatsapp", "matrix"); unknown keys are ignored. Delegates
// to internal/style, shared with internal/tui.
func resolveGlyphs(overrides map[string]string) map[core.Channel]string {
	return style.ResolveGlyphs(overrides)
}
