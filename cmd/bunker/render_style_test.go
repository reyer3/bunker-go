package main

import (
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func testSegments(mail, wa, mx int) []renderSegment {
	return []renderSegment{
		{Channel: core.ChannelMail, Glyph: channelGlyphs[core.ChannelMail], Unread: mail},
		{Channel: core.ChannelWhatsApp, Glyph: channelGlyphs[core.ChannelWhatsApp], Unread: wa},
		{Channel: core.ChannelMatrix, Glyph: channelGlyphs[core.ChannelMatrix], Unread: mx},
	}
}

func TestFormatRenderPlainIsUnchanged(t *testing.T) {
	if got := formatRender(testSegments(3, 0, 2), renderPlain, false, styledGlyphs); got != "✉ 3  💬 0  ⌘ 2" {
		t.Fatalf("plain = %q, want the documented default", got)
	}
}

func TestFormatRenderTmuxColorsEachChannelAndDimsZero(t *testing.T) {
	got := formatRender(testSegments(3, 0, 53), renderTmux, false, styledGlyphs)
	for _, want := range []string{
		"#[fg=#4db0ff]\U000f01ee 3#[default]",
		"#[fg=#a3a09e]\U000f05a3 0#[default]",
		"#[fg=#0dbd8b]\U000f0628 53#[default]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("tmux = %q, want it to contain %q", got, want)
		}
	}
}

func TestFormatRenderANSIUsesTruecolor(t *testing.T) {
	got := formatRender(testSegments(0, 2, 0), renderANSI, false, styledGlyphs)
	if !strings.Contains(got, "\x1b[38;2;37;211;102m\U000f05a3 2\x1b[0m") {
		t.Errorf("ansi = %q, want WhatsApp green truecolor segment", got)
	}
}

func TestFormatRenderHideEmptyPrintsNothingWhenAllZero(t *testing.T) {
	for _, style := range []renderStyle{renderPlain, renderTmux, renderANSI} {
		if got := formatRender(testSegments(0, 0, 0), style, true, styledGlyphs); got != "" {
			t.Errorf("style %v hide-empty all zero = %q, want empty", style, got)
		}
		if got := formatRender(testSegments(1, 0, 0), style, true, styledGlyphs); got == "" {
			t.Errorf("style %v hide-empty with unread = empty, want output", style)
		}
	}
}

func TestResolveGlyphsAppliesConfigOverrides(t *testing.T) {
	g := resolveGlyphs(map[string]string{"matrix": "\U00100000", "bogus": "x"})
	if g[core.ChannelMatrix] != "\U00100000" {
		t.Errorf("matrix glyph = %q, want the configured override", g[core.ChannelMatrix])
	}
	if g[core.ChannelMail] != styledGlyphs[core.ChannelMail] {
		t.Errorf("mail glyph = %q, want the default when not overridden", g[core.ChannelMail])
	}
	if styledGlyphs[core.ChannelMatrix] == "\U00100000" {
		t.Error("resolveGlyphs mutated the package defaults")
	}
}
