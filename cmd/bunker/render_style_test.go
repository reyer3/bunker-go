package main

import (
	"strings"
	"testing"
	"time"

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

func TestFormatCallSegment(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	ringing := core.Call{ID: "r", Direction: core.CallIncoming, State: core.CallStateRinging, PeerName: "Ana", Peer: "519@s.whatsapp.net"}
	active := core.Call{ID: "a", Direction: core.CallOutgoing, State: core.CallStateActive, PeerName: "Beto", ConnectedAt: now.Add(-155 * time.Second)}
	calling := core.Call{ID: "c", Direction: core.CallOutgoing, State: core.CallStateCalling, Peer: "51999@s.whatsapp.net"}

	if got := formatCallSegment(nil, renderPlain, now); got != "" {
		t.Errorf("no calls = %q, want empty", got)
	}
	if got := formatCallSegment([]core.Call{active, ringing}, renderPlain, now); got != "📞 Ana" {
		t.Errorf("ringing wins: got %q", got)
	}
	if got := formatCallSegment([]core.Call{active, calling}, renderPlain, now); got != "📞 Beto 2:35" {
		t.Errorf("active: got %q", got)
	}
	if got := formatCallSegment([]core.Call{calling}, renderPlain, now); got != "📞 → 51999" {
		t.Errorf("outgoing ringing: got %q", got)
	}
	if got := formatCallSegment([]core.Call{ringing}, renderTmux, now); !strings.Contains(got, "bold,blink]📞 Ana#[default]") {
		t.Errorf("tmux ringing: got %q", got)
	}
	evil := core.Call{Direction: core.CallIncoming, State: core.CallStateRinging, PeerName: "#[fg=red]x\x1b"}
	if got := formatCallSegment([]core.Call{evil}, renderPlain, now); strings.ContainsAny(got, "#\x1b") {
		t.Errorf("peer name not sanitized: %q", got)
	}
}
