package art

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi/sixel"
)

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 255 / max(1, w-1)), G: uint8(y * 255 / max(1, h-1)), B: 128, A: 255})
		}
	}
	return img
}

// Sixel wraps the encoder's payload in a DCS … ST sequence and scales the
// picture to fit the cell box: a square cover in an 18×8 box of 14×32 px cells
// comes out 252×252, the largest square that fits 252×256.
func TestSixel_FitsTheCellBox(t *testing.T) {
	seq, err := Sixel(gradient(60, 60), 18, 8, 14, 32)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(seq, "\x1bP0;1q") || !strings.HasSuffix(seq, "\x1b\\") {
		t.Fatalf("not a DCS sixel sequence: %q…%q", seq[:min(12, len(seq))], seq[len(seq)-4:])
	}
	payload := seq[strings.IndexByte(seq, 'q')+1 : len(seq)-2]
	var dec sixel.Decoder
	img, err := dec.Decode(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("decoding the payload back: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 252 || b.Dy() != 252 {
		t.Fatalf("decoded size = %dx%d, want 252x252", b.Dx(), b.Dy())
	}
}

func TestSixel_RejectsWhatItCannotDraw(t *testing.T) {
	if _, err := Sixel(nil, 4, 2, 10, 20); err == nil {
		t.Fatal("nil image accepted")
	}
	if _, err := Sixel(gradient(4, 4), 0, 2, 10, 20); err == nil {
		t.Fatal("zero columns accepted")
	}
	if _, err := Sixel(gradient(4, 4), 4, 2, 0, 20); err == nil {
		t.Fatal("unknown cell size accepted")
	}
}

func TestFitInto_KeepsTheAspectRatio(t *testing.T) {
	for _, c := range []struct{ sw, sh, bw, bh, w, h int }{
		{600, 600, 252, 256, 252, 252}, // square cover, slightly tall box
		{600, 600, 260, 128, 128, 128}, // wide box: height limits
		{300, 100, 90, 90, 90, 30},     // wide picture: width limits
		{1, 1, 10, 10, 10, 10},
	} {
		if w, h := fitInto(c.sw, c.sh, c.bw, c.bh); w != c.w || h != c.h {
			t.Errorf("fitInto(%dx%d into %dx%d) = %dx%d, want %dx%d", c.sw, c.sh, c.bw, c.bh, w, h, c.w, c.h)
		}
	}
}

// Resize averages the source pixels behind each target pixel: a 2×2
// black-and-white checkerboard shrunk to one pixel is mid grey.
func TestResize_Averages(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, color.RGBA{A: 255})
	img.SetRGBA(1, 1, color.RGBA{A: 255})
	img.SetRGBA(1, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	img.SetRGBA(0, 1, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	out := Resize(img, 1, 1)
	if out == nil {
		t.Fatal("nil result")
	}
	if c := out.RGBAAt(0, 0); c.R != 127 || c.G != 127 || c.B != 127 {
		t.Fatalf("averaged pixel = %+v, want 127 grey", c)
	}
	if Resize(nil, 1, 1) != nil || Resize(img, 0, 1) != nil {
		t.Fatal("bad input must give nil")
	}
}
