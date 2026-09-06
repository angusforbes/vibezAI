package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The player opens exactly one website, and only on purpose: Ctrl+Shift+D in
// the About panel opens the donation page (views/about.go). Everything else
// that reaches a browser is an explicit CLI command (the Apple Music sign-in
// in internal/auth, the Last.fm login in cmd). This scan keeps every other
// TUI source file free of the opener, so a stray "donate"-style action cannot
// come back on a key that means something else.
func TestOnlyAboutOpensABrowser(t *testing.T) {
	allowed := filepath.Join("views", "about.go")
	sawOpener := false
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
			path := filepath.Join(dir, name)
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, banned := range []string{"internal/openurl", "xdg-open", "ko-fi.com"} {
				if !strings.Contains(string(src), banned) {
					continue
				}
				if path == allowed {
					sawOpener = true
					continue
				}
				t.Errorf("%s mentions %q: only %s may open a website", path, banned, allowed)
			}
		}
	}
	if !sawOpener {
		t.Errorf("%s no longer holds the browser opener; move this exception with it", allowed)
	}
}
