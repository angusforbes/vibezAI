package views

import (
	"strings"
	"testing"
)

func TestLyrics_NoSadFaceWhenUnavailable(t *testing.T) {
	l := NewLyrics()
	l.SetSize(40, 10)
	l.errMsg = "not found"
	if v := l.View(); strings.Contains(v, ":(") || !strings.Contains(v, "no lyrics available") {
		t.Fatalf("unavailable lyrics should read plainly, got %q", v)
	}
}
