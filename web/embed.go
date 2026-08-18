// Package web embeds the built SPA into the binary.
//
// The embed lives here, next to dist/, because go:embed paths cannot escape
// their own directory with "..".
package web

import (
	"embed"
	"io/fs"
)

// all: is required so the embed still resolves when dist/ holds only .gitkeep
// (i.e. before `make web` has ever run) — the default embed pattern skips
// dotfiles and would fail on an otherwise-empty directory.
//
//go:embed all:dist
var distFS embed.FS

// Dist returns the built SPA rooted at "/", so index.html is at "index.html"
// rather than "dist/index.html".
func Dist() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // only reachable if the embed directive above is wrong
	}
	return sub
}
