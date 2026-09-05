package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The player must never open a website on its own: the only browser use in
// the whole program is the Apple Music sign-in (internal/auth) and the
// optional Last.fm login (cmd). This scan keeps the TUI packages free of the
// opener so a stray "donate"-style action cannot come back.
func TestTUINeverOpensABrowser(t *testing.T) {
	for _, dir := range []string{".", "views"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			src, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			for _, banned := range []string{"internal/openurl", "xdg-open", "ko-fi.com"} {
				if strings.Contains(string(src), banned) {
					t.Errorf("%s/%s mentions %q: the TUI must not open websites", dir, name, banned)
				}
			}
		}
	}
}
