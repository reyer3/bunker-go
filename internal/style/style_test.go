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
