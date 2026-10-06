package style

import (
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestChannelColorsMatchBrand(t *testing.T) {
	want := map[core.Channel]string{
		core.ChannelMail:     "#4db0ff",
		core.ChannelWhatsApp: "#25d366",
		core.ChannelMatrix:   "#0dbd8b",
	}
	for ch, hex := range want {
		if ChannelColors[ch] != hex {
			t.Errorf("ChannelColors[%s] = %q, want %q", ch, ChannelColors[ch], hex)
		}
	}
	if ColorDim != "#a3a09e" {
		t.Errorf("ColorDim = %q, want #a3a09e", ColorDim)
	}
}

func TestResolveGlyphsAppliesOverridesWithoutMutatingDefaults(t *testing.T) {
	g := ResolveGlyphs(map[string]string{"matrix": "\U00100000", "bogus": "x", "mail": ""})
	if g[core.ChannelMatrix] != "\U00100000" {
		t.Errorf("matrix glyph = %q, want the configured override", g[core.ChannelMatrix])
	}
	if g[core.ChannelMail] != Glyphs[core.ChannelMail] {
		t.Errorf("mail glyph = %q, want the default (empty override ignored)", g[core.ChannelMail])
	}
	if Glyphs[core.ChannelMatrix] == "\U00100000" {
		t.Error("ResolveGlyphs mutated the package defaults")
	}
	if _, known := g[core.Channel("bogus")]; known {
		t.Error("ResolveGlyphs added an unknown channel key")
	}
}

func TestDefaultPaletteMatchesBrandColors(t *testing.T) {
	p := DefaultPalette()
	for ch, hex := range ChannelColors {
		if got := p.Channel(ch); got != hex {
			t.Errorf("DefaultPalette().Channel(%s) = %q, want %q", ch, got, hex)
		}
	}
	if p.Dim != ColorDim {
		t.Errorf("DefaultPalette().Dim = %q, want %q", p.Dim, ColorDim)
	}
}

func TestResolvePaletteAppliesValidOverridesWithoutMutatingDefaults(t *testing.T) {
	p := ResolvePalette(map[string]string{
		"mail":     "#112233",
		"whatsapp": "not-a-color",
		"matrix":   "#ABCDEF",
		"dim":      "#445566",
		"bogus":    "#000000",
	})
	if got := p.Channel(core.ChannelMail); got != "#112233" {
		t.Errorf("mail = %q, want the configured override", got)
	}
	if got := p.Channel(core.ChannelWhatsApp); got != ColorWhatsApp {
		t.Errorf("whatsapp = %q, want the default (invalid override ignored)", got)
	}
	if got := p.Channel(core.ChannelMatrix); got != "#ABCDEF" {
		t.Errorf("matrix = %q, want the configured override", got)
	}
	if p.Dim != "#445566" {
		t.Errorf("dim = %q, want the configured override", p.Dim)
	}
	if _, known := p.Channels[core.Channel("bogus")]; known {
		t.Error("ResolvePalette added an unknown channel key")
	}
	if ChannelColors[core.ChannelMail] != "#4db0ff" || ColorDim != "#a3a09e" {
		t.Error("ResolvePalette mutated the package defaults")
	}
}

func TestResolvePaletteRejectsMalformedHex(t *testing.T) {
	for _, bad := range []string{"", "4db0ff", "#4db0f", "#4db0ff0", "#ggggggg", "#gggggg", "red", " #4db0ff"} {
		p := ResolvePalette(map[string]string{"mail": bad, "dim": bad})
		if p.Channel(core.ChannelMail) != ColorMail || p.Dim != ColorDim {
			t.Errorf("override %q was applied; want it ignored", bad)
		}
	}
}

func TestZeroPaletteFallsBackToDefaults(t *testing.T) {
	var p Palette
	if got := p.Channel(core.ChannelWhatsApp); got != ColorWhatsApp {
		t.Errorf("zero Palette.Channel(whatsapp) = %q, want %q", got, ColorWhatsApp)
	}
	if got := p.DimColor(); got != ColorDim {
		t.Errorf("zero Palette.DimColor() = %q, want %q", got, ColorDim)
	}
}
