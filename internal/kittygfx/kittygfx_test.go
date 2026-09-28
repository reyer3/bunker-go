package kittygfx

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestDetect(t *testing.T) {
	cases := []struct {
		vars map[string]string
		want Mode
	}{
		{map[string]string{"TERM_PROGRAM": "ghostty"}, Kitty},
		{map[string]string{"TERM": "xterm-ghostty"}, Kitty},
		{map[string]string{"TERM": "xterm-kitty"}, Kitty},
		{map[string]string{"KITTY_WINDOW_ID": "1"}, Kitty},
		{map[string]string{"TERM": "xterm-256color"}, None},
		{map[string]string{"TERM_PROGRAM": "ghostty", "TMUX": "/tmp/tmux"}, None},
		{map[string]string{"TERM_PROGRAM": "ghostty", "BUNKER_GRAPHICS": "none"}, None},
		{map[string]string{"TMUX": "/tmp/tmux", "BUNKER_GRAPHICS": "kitty"}, Kitty},
	}
	for _, c := range cases {
		if got := Detect(env(c.vars)); got != c.want {
			t.Errorf("Detect(%v) = %v, want %v", c.vars, got, c.want)
		}
	}
}

func TestLinesWidthAndID(t *testing.T) {
	lines := Lines(0x0a0b0c, 12, 3)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != 12 {
			t.Errorf("line %d width = %d, want 12", i, w)
		}
		if !strings.Contains(line, string(diacritics[i])) {
			t.Errorf("line %d lacks its row diacritic", i)
		}
		id, ok := ImageID(line)
		if !ok || id != 0x0a0b0c {
			t.Errorf("ImageID(line %d) = %x, %v", i, id, ok)
		}
	}
	if _, ok := ImageID("plain text"); ok {
		t.Error("ImageID found an id in plain text")
	}
	if Lines(1, 0, 3) != nil || Lines(1, 3, 0) != nil {
		t.Error("Lines with an empty box returned lines")
	}
}

func TestTransmitChunks(t *testing.T) {
	data := bytes.Repeat([]byte{0xAB}, 10000) // > one 4096-byte base64 chunk
	out := Transmit(7, data, 5, 2)
	if !strings.HasPrefix(out, "\x1b_Ga=T,U=1,f=100,t=d,i=7,c=5,r=2,q=2,m=1;") {
		t.Fatalf("unexpected first chunk: %q", out[:60])
	}
	chunks := strings.Split(strings.TrimSuffix(out, "\x1b\\"), "\x1b\\")
	var payload strings.Builder
	for i, c := range chunks {
		semi := strings.IndexByte(c, ';')
		header := c[:semi]
		last := i == len(chunks)-1
		if last != strings.HasSuffix(header, "m=0") {
			t.Errorf("chunk %d header %q: wrong m flag", i, header)
		}
		payload.WriteString(c[semi+1:])
	}
	got, err := base64.StdEncoding.DecodeString(payload.String())
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("payload does not round-trip: %v", err)
	}
	if !strings.Contains(Delete(7), "a=d,d=I,i=7") {
		t.Error("Delete escape malformed")
	}
}

func pngOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{255, 0, 0, 255})
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestFit(t *testing.T) {
	out, cols, rows, err := Fit(pngOf(1000, 500), 30, 10)
	if err != nil {
		t.Fatal(err)
	}
	// 1000x500 at 10x20 cells: 30 cols need 30*10*500/1000/20 = 7.5 → 8 rows.
	if cols != 30 || rows != 8 {
		t.Errorf("wide image: %dx%d cells, want 30x8", cols, rows)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() > 300 || b.Dy() > 160 {
		t.Errorf("thumbnail %v exceeds its cell box", b)
	}

	_, cols, rows, _ = Fit(pngOf(400, 2000), 30, 10)
	if rows != 10 || cols > 30 {
		t.Errorf("tall image: %dx%d cells, want 10 rows", cols, rows)
	}

	_, cols, rows, _ = Fit(pngOf(40, 40), 30, 10)
	if cols != 4 || rows != 2 {
		t.Errorf("small image: %dx%d cells, want its natural 4x2", cols, rows)
	}

	if _, _, _, err := Fit([]byte("not an image"), 30, 10); err == nil {
		t.Error("Fit accepted garbage")
	}
}
