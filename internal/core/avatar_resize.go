package core

import "image"

// nearestNeighborResize downsamples src to exactly dstW x dstH using
// nearest-neighbor sampling. WhatsApp's "preview" profile pictures and
// Matrix's resized-on-upload avatars are already small (typically
// 96-640px), so a simple, dependency-free algorithm is enough — no need
// for golang.org/x/image/draw's higher-quality (and heavier) scalers just
// to shrink an already-small thumbnail further.
func nearestNeighborResize(src image.Image, dstW, dstH int) *image.NRGBA {
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))

	for y := 0; y < dstH; y++ {
		srcY := bounds.Min.Y + y*srcH/dstH
		for x := 0; x < dstW; x++ {
			srcX := bounds.Min.X + x*srcW/dstW
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}
	return dst
}
