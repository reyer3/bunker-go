package tui

import (
	"io"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/style"
)

// TestRowStylesUseConfiguredPalette checks [render.colors] overrides reach
// the inbox styles: the channel glyph and the dim style use the configured
// hex instead of the brand defaults.
func TestRowStylesUseConfiguredPalette(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	pal := style.ResolvePalette(map[string]string{"mail": "#7aa2f7", "dim": "#565f89"})
	rs := newRowStyles(r, pal)

	// Compare against lipgloss's own rendering of the expected hex: its
	// color conversion may round a channel by one.
	fg := func(hex string) string { return r.NewStyle().Foreground(lipgloss.Color(hex)).Render("x") }
	if got, want := rs.glyph[core.ChannelMail].Render("x"), fg("#7aa2f7"); got != want {
		t.Errorf("mail glyph = %q, want the configured #7aa2f7 (%q)", got, want)
	}
	if got, want := rs.dim.Render("x"), fg("#565f89"); got != want {
		t.Errorf("dim = %q, want the configured #565f89 (%q)", got, want)
	}
	if got, want := rs.glyph[core.ChannelWhatsApp].Render("x"), fg(style.ColorWhatsApp); got != want {
		t.Errorf("whatsapp glyph = %q, want the default (%q)", got, want)
	}
	if got := fg("#7aa2f7"); got == fg(style.ColorMail) {
		t.Fatal("test colors render identically; pick distinct hex values")
	}
}

// TestModelWithPaletteColorsOwnMatrixBubble checks the chat view's own
// Matrix bubble follows the configured matrix accent.
func TestModelWithPaletteColorsOwnMatrixBubble(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	now := time.Now()
	pal := style.ResolvePalette(map[string]string{"matrix": "#bb9af7"})
	item := core.Item{Body: "hi", FromMe: true, Channel: core.ChannelMatrix, Timestamp: now}

	got := chatBubbleLinesFocus(r, pal, item, 40, false, now, false, "", nil, false)[0]
	def := chatBubbleLinesFocus(r, style.DefaultPalette(), item, 40, false, now, false, "", nil, false)[0]
	if got == def {
		t.Errorf("own matrix bubble rendered with the default accent; want the configured #bb9af7")
	}
	// The default renders as the original package-level call did.
	if legacy := chatBubbleLines(r, item, 40, false, now, false, "", nil)[0]; legacy != def {
		t.Errorf("chatBubbleLines = %q, want the default-palette rendering %q", legacy, def)
	}
	if m := NewModel(nil).withColors(pal); m.colors().Channel(core.ChannelMatrix) != "#bb9af7" {
		t.Errorf("model palette matrix = %q, want #bb9af7", m.colors().Channel(core.ChannelMatrix))
	}
	if m := NewModel(nil); m.colors().Channel(core.ChannelMatrix) != style.ColorMatrix {
		t.Errorf("unconfigured model palette matrix = %q, want the default", m.colors().Channel(core.ChannelMatrix))
	}
}
