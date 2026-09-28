package kittygfx

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif" // registered decoders for image.Decode
	_ "image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // WhatsApp stickers are WebP
)

// Cell is the assumed pixel size of one terminal cell. Placements are
// sized in cells and the terminal scales the image into them, so this
// only sets the thumbnail's resolution and its cell aspect ratio: a
// typical monospace cell is about twice as tall as it is wide.
const (
	CellWidth  = 10
	CellHeight = 20
)

// Fit decodes data (JPEG, PNG, GIF or WebP) and returns it re-encoded as
// a PNG sized to fit within maxCols×maxRows cells, together with the
// cells it occupies, keeping the image's aspect ratio. It never upscales
// past the image's own size.
func Fit(data []byte, maxCols, maxRows int) (out []byte, cols, rows int, err error) {
	if maxCols <= 0 || maxRows <= 0 {
		return nil, 0, 0, fmt.Errorf("kittygfx: fit: empty box %dx%d", maxCols, maxRows)
	}
	if maxRows > MaxRows {
		maxRows = MaxRows
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("kittygfx: decode: %w", err)
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	if w <= 0 || h <= 0 {
		return nil, 0, 0, fmt.Errorf("kittygfx: decode: empty image")
	}
	cols, rows = fitCells(w, h, maxCols, maxRows)

	pw, ph := cols*CellWidth, rows*CellHeight
	// Keep the aspect ratio inside the cell box instead of stretching.
	if pw*h > ph*w {
		pw = ph * w / h
	} else {
		ph = pw * h / w
	}
	if pw > w || ph > h {
		pw, ph = w, h
	}
	pw, ph = max(pw, 1), max(ph, 1)

	dst := image.NewRGBA(image.Rect(0, 0, pw, ph))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, dst); err != nil {
		return nil, 0, 0, fmt.Errorf("kittygfx: encode: %w", err)
	}
	return buf.Bytes(), cols, rows, nil
}

// fitCells picks the largest cols×rows within maxCols×maxRows matching a
// w×h image's aspect ratio, in CellWidth×CellHeight cells.
func fitCells(w, h, maxCols, maxRows int) (cols, rows int) {
	cols = maxCols
	rows = (cols*CellWidth*h + w*CellHeight - 1) / (w * CellHeight)
	if rows > maxRows {
		rows = maxRows
		cols = rows * CellHeight * w / (h * CellWidth)
	}
	// Small images keep their natural size instead of filling the box.
	if natural := (w + CellWidth - 1) / CellWidth; cols > natural {
		cols = natural
		rows = (cols*CellWidth*h + w*CellHeight - 1) / (w * CellHeight)
	}
	return max(cols, 1), max(min(rows, maxRows), 1)
}
