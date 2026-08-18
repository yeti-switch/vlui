package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/yeti-switch/vlui/internal/config"
)

// The Go list validates what the YAML may say; the TypeScript map decides what
// is drawn. Neither can see the other, so a name added to one and forgotten in
// the other would be either a config error for an icon that exists, or a
// silently substituted shape. This is the only thing keeping them together.
func TestIconsMatchTheFrontend(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "icons.ts"))
	if err != nil {
		t.Fatalf("web/src/icons.ts: %v", err)
	}

	body := string(src)
	start := strings.Index(body, "export const ICONS")
	if start < 0 {
		t.Fatal("web/src/icons.ts no longer declares ICONS")
	}
	end := strings.Index(body[start:], "\n}")
	if end < 0 {
		t.Fatal("cannot find the end of the ICONS record")
	}

	keys := regexp.MustCompile(`(?m)^\s{2}([a-z][a-z0-9_]*):\s`).FindAllStringSubmatch(body[start:start+end], -1)
	var front []string
	for _, k := range keys {
		front = append(front, k[1])
	}
	if len(front) == 0 {
		t.Fatal("parsed no icon names out of web/src/icons.ts")
	}

	slices.Sort(front)
	back := slices.Clone(config.Icons)
	slices.Sort(back)

	if !slices.Equal(front, back) {
		t.Errorf("icon names have drifted:\n  config.Icons:     %v\n  web/src/icons.ts: %v", back, front)
	}
}
