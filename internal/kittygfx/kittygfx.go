// Package kittygfx draws images in a terminal that speaks the kitty
// graphics protocol (Ghostty, kitty), through unicode placeholders.
//
// Placeholders keep images compatible with a cell-based UI such as
// bubbletea: an image is uploaded once (Transmit), and then occupies
// ordinary text cells (Lines) that the UI lays out, pads, scrolls and
// redraws like any other text. The terminal paints the image over the
// cells whose foreground color names its id.
package kittygfx

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Mode is whether the terminal can show images.
type Mode int

const (
	// None draws no images: the UI keeps its plain text rendering.
	None Mode = iota
	// Kitty draws images through kitty graphics unicode placeholders.
	Kitty
)

// Detect picks the Mode for the current terminal from its environment.
// BUNKER_GRAPHICS=kitty|none overrides detection. Inside tmux it returns
// None: tmux does not forward the graphics protocol by default.
func Detect(getenv func(string) string) Mode {
	switch strings.ToLower(strings.TrimSpace(getenv("BUNKER_GRAPHICS"))) {
	case "kitty":
		return Kitty
	case "none", "off", "0":
		return None
	}
	if getenv("TMUX") != "" {
		return None
	}
	if strings.EqualFold(getenv("TERM_PROGRAM"), "ghostty") || getenv("KITTY_WINDOW_ID") != "" {
		return Kitty
	}
	switch getenv("TERM") {
	case "xterm-ghostty", "xterm-kitty":
		return Kitty
	}
	return None
}

// Placeholder is the kitty graphics unicode placeholder character.
const Placeholder = '\U0010EEEE'

// MaxID is the largest image id Lines can encode: the id is carried in
// the placeholders' 24-bit foreground color.
const MaxID = 1<<24 - 1

// MaxRows is the tallest placement Lines can encode (one row diacritic
// per row).
const MaxRows = len(diacritics)

// chunkSize is the protocol's maximum base64 payload per escape.
const chunkSize = 4096

// Transmit returns the escapes that upload png (PNG-encoded bytes) as
// image id and create its virtual placement of cols×rows cells, ready
// for Lines to display. q=2 keeps the terminal from answering, since the
// UI never reads replies.
func Transmit(id uint32, png []byte, cols, rows int) string {
	payload := base64.StdEncoding.EncodeToString(png)
	var b strings.Builder
	first := true
	for {
		chunk := payload
		if len(chunk) > chunkSize {
			chunk = chunk[:chunkSize]
		}
		payload = payload[len(chunk):]
		more := 0
		if payload != "" {
			more = 1
		}
		if first {
			fmt.Fprintf(&b, "\x1b_Ga=T,U=1,f=100,t=d,i=%d,c=%d,r=%d,q=2,m=%d;%s\x1b\\", id, cols, rows, more, chunk)
			first = false
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
		if more == 0 {
			return b.String()
		}
	}
}

// Delete returns the escape that frees image id and its placements.
func Delete(id uint32) string {
	return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
}

// Lines returns rows lines of cols placeholder cells showing image id's
// virtual placement. Each line is exactly cols cells wide and restores
// the default foreground at its end; the caller may wrap it in a
// background color. Only the first cell of a line carries its row and
// column diacritics: the terminal infers the rest of the row from them.
func Lines(id uint32, cols, rows int) []string {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	if rows > MaxRows {
		rows = MaxRows
	}
	fg := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", id>>16&0xff, id>>8&0xff, id&0xff)
	rest := strings.Repeat(string(Placeholder), cols-1)
	lines := make([]string, rows)
	for r := range lines {
		lines[r] = fg + string(Placeholder) + string(diacritics[r]) + string(diacritics[0]) + rest + "\x1b[39m"
	}
	return lines
}

// ImageID extracts the image id from a line produced by Lines, reporting
// false when line contains no placeholder. The UI uses it to map a click
// on a rendered line back to the image under it.
func ImageID(line string) (uint32, bool) {
	at := strings.IndexRune(line, Placeholder)
	if at < 0 {
		return 0, false
	}
	start := strings.LastIndex(line[:at], "\x1b[38;2;")
	if start < 0 {
		return 0, false
	}
	var r, g, b uint32
	if _, err := fmt.Sscanf(line[start:at], "\x1b[38;2;%d;%d;%dm", &r, &g, &b); err != nil {
		return 0, false
	}
	return r<<16 | g<<8 | b, true
}
