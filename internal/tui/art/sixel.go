package art

import (
	"bytes"
	"fmt"
	"image"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/sixel"
)

// Sixel encodes img as a sixel graphic sized for a cols × rows cell area whose
// cells are cellW × cellH pixels: the picture is scaled to the largest size
// that fits that box with its aspect ratio kept, anchored at the top-left, and
// returned as a complete DCS … ST sequence to be written at the cursor. This
// is the cover at the terminal's real pixel resolution, where half-blocks can
// only manage two pixels per cell.
func Sixel(img image.Image, cols, rows, cellW, cellH int) (string, error) {
	if img == nil {
		return "", fmt.Errorf("sixel: nil image")
	}
	if cols <= 0 || rows <= 0 || cellW <= 0 || cellH <= 0 {
		return "", fmt.Errorf("sixel: bad geometry: %dx%d cells of %dx%d px", cols, rows, cellW, cellH)
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return "", fmt.Errorf("sixel: empty image")
	}
	w, h := fitInto(b.Dx(), b.Dy(), cols*cellW, rows*cellH)
	scaled := Resize(img, w, h)
	var payload bytes.Buffer
	var enc sixel.Encoder
	if err := enc.Encode(&payload, scaled); err != nil {
		return "", fmt.Errorf("sixel: %w", err)
	}
	// p2 = 1 keeps unset pixels at the background colour; 0 draws them black
	// on the terminals that matter here.
	return ansi.SixelGraphics(0, 1, 0, payload.Bytes()), nil
}

// fitInto scales srcW × srcH to the largest size that fits boxW × boxH
// without changing its aspect ratio; never smaller than one pixel a side.
func fitInto(srcW, srcH, boxW, boxH int) (int, int) {
	w, h := boxW, srcH*boxW/srcW
	if h > boxH {
		h = boxH
		w = srcW * boxH / srcH
	}
	return max(1, w), max(1, h)
}
