//go:build !linux && !darwin

package tui

// cellAspect returns the conventional cell height/width ratio on platforms
// without a TIOCGWINSZ query. See the unix build for details.
func cellAspect() float64 { return 2.0 }

// cellPixels is unknown on platforms without TIOCGWINSZ pixel geometry.
func cellPixels() (int, int) { return 0, 0 }
