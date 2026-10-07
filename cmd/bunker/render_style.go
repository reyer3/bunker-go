package main

import (
	"fmt"
	"strings"
	"time"

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

// formatRender renders segments in style. With hideEmpty, it returns ""
// when no channel has unread items, so a status line can disappear. The
// colored styles take each channel's accent from palette (see
// style.ResolvePalette) and dim empty channels with its dim color.
func formatRender(segments []renderSegment, style renderStyle, hideEmpty bool, glyphs map[core.Channel]string, palette style.Palette) string {
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
		color := palette.Channel(seg.Channel)
		if seg.Unread == 0 {
			color = palette.DimColor()
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

// callColor is the ringing/live call accent: impossible to miss in a
// status line.
const callColor = "#e06c75"

// formatCallSegment renders the most relevant call for the status line
// (issue #15): a ringing incoming call first ("📞 Ana"), else a live one
// with its connected time ("📞 Ana 2:35"), else an outgoing call still
// ringing ("📞 → Ana"). A video call shows 📹 instead of 📞. It returns ""
// when there is no call.
func formatCallSegment(calls []core.Call, style renderStyle, now time.Time) string {
	var pick *core.Call
	rank := func(c core.Call) int {
		switch {
		case c.Direction == core.CallIncoming && c.State == core.CallStateRinging:
			return 3
		case c.State == core.CallStateActive || c.State == core.CallStateConnecting:
			return 2
		case c.State != core.CallStateEnded:
			return 1
		}
		return 0
	}
	for i := range calls {
		if rank(calls[i]) > 0 && (pick == nil || rank(calls[i]) > rank(*pick)) {
			pick = &calls[i]
		}
	}
	if pick == nil {
		return ""
	}
	name := pick.PeerName
	if name == "" {
		name = strings.SplitN(pick.Peer, "@", 2)[0]
	}
	name = strings.Map(func(r rune) rune {
		// The name is remote-controlled: keep tmux's #[...] and control
		// characters out of the status line.
		if r == '#' || r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	icon := "📞"
	if pick.Video {
		icon = "📹"
	}
	text := icon + " " + name
	switch rank(*pick) {
	case 2:
		if d := pick.Duration(now); d > 0 {
			text += " " + core.FormatCallDuration(d)
		}
	case 1:
		text = icon + " → " + name
	}
	switch style {
	case renderTmux:
		attr := ""
		if rank(*pick) == 3 {
			attr = ",bold,blink"
		}
		return fmt.Sprintf("#[fg=%s%s]%s#[default]", callColor, attr, text)
	case renderANSI:
		r, g, b := hexRGB(callColor)
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[0m", r, g, b, text)
	}
	return text
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

// styleColors returns the accent palette with [render.colors] overrides
// applied; invalid entries are ignored. Delegates to internal/style,
// shared with internal/tui.
func styleColors(overrides map[string]string) style.Palette {
	return style.ResolvePalette(overrides)
}
