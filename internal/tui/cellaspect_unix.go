//go:build linux || darwin

package tui

import (
	"os"

	"golang.org/x/sys/unix"
)

// cellAspect returns the terminal's cell height/width ratio. Album art is sized
// to this ratio so a square cover renders as a true square regardless of the
// font's cell proportions (a wide monospace font like JetBrains Mono has cells
// noticeably less than twice as tall as wide).
//
// It queries the terminal's pixel geometry via TIOCGWINSZ. When that is
// unavailable — e.g. inside tmux or a terminal that reports zero pixels — it
// falls back to the conventional 2.0 (cells roughly twice as tall as wide).
func cellAspect() float64 {
	const fallback = 2.0
	//nolint:gosec // a file descriptor fits an int; os.Stdout.Fd is 1.
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Xpixel == 0 || ws.Ypixel == 0 || ws.Col == 0 || ws.Row == 0 {
		return fallback
	}
	cw := float64(ws.Xpixel) / float64(ws.Col)
	ch := float64(ws.Ypixel) / float64(ws.Row)
	if cw <= 0 || ch <= 0 {
		return fallback
	}
	// Clamp to a sane range so a bogus report can't distort the layout.
	return clampCellAspect(ch / cw)
}

// cellPixels returns the size of one terminal cell in pixels, or zeros when the
// terminal does not report its pixel geometry. A cover drawn as a picture is
// scaled to cells, so without this it stays half-blocks.
func cellPixels() (w, h int) {
	//nolint:gosec // a file descriptor fits an int; os.Stdout.Fd is 1.
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Xpixel == 0 || ws.Ypixel == 0 || ws.Col == 0 || ws.Row == 0 {
		return 0, 0
	}
	return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row)
}
