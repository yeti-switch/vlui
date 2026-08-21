// Package webui serves the Vue SPA out of the Go binary.
//
// The SPA is embedded rather than served from an nginx root so that the
// frontend and the API that feeds it are the same artifact and cannot drift
// out of version with each other. nginx still fronts this process for TLS,
// Let's Encrypt and the IP allowlist.
package webui

import (
	"bytes"
	"errors"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Handler serves the built SPA, mounted at base ("" for the root, or "/logs").
//
// Two things matter here and are easy to get wrong:
//
//   - History fallback. Any path that is not an existing asset must return
//     index.html, or refreshing the browser on a deep link 404s. We
//     decide by looking the file up, not by sniffing the extension.
//
//   - Cache headers. Vite emits content-hashed asset filenames, so those are
//     immutable and cacheable forever. index.html must never be cached, or
//     browsers pin themselves to a stale build that references assets which no
//     longer exist.
//
// The base path is applied here, at serve time, not at build time. Vite is
// configured to emit *relative* asset URLs ("./assets/…"), and we inject a
// <base href> into index.html telling the browser what to resolve them against.
// So the same binary runs at "/" or at "/logs" with no rebuild — the mount
// point is deployment configuration, which is where it belongs.
// Options are what the deployment gets to change about the page itself, as
// opposed to what is in it.
type Options struct {
	// Base is the sub-path the app is mounted under: "" or "/logs".
	Base string

	// Title replaces the tab's title. Empty keeps the one in index.html.
	Title string

	// Favicon replaces the tab's icon. Nil keeps the one the SPA ships with.
	Favicon *Favicon
}

func Handler(dist fs.FS, opts Options) http.Handler {
	files := http.FileServer(http.FS(dist))
	index := loadIndex(dist, opts)

	// The configured icon is served from memory at its hashed path, and also at
	// the bare /favicon.ico a browser asks for on its own when a page names no
	// icon — some do, and answering that request with the SPA's HTML (which the
	// history fallback below would otherwise do) is worse than answering it
	// with the icon.
	favicon := opts.Favicon
	faviconPath := ""
	if favicon != nil {
		faviconPath = strings.TrimPrefix(favicon.Path, opts.Base)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")

		if favicon != nil && ("/"+p == faviconPath || p == "favicon.ico") {
			w.Header().Set("Content-Type", favicon.ContentType)
			// The name carries a hash of the contents, so this can never be
			// stale: a different icon is a different URL.
			if "/"+p == faviconPath {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			_, _ = w.Write(favicon.Body)
			return
		}

		if p == "" || !exists(dist, p) {
			serveIndex(w, index)
			return
		}

		// Hashed assets: safe to cache indefinitely.
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

// loadIndex reads index.html once and injects the <base href> the SPA needs to
// resolve its assets, its API calls and its router paths.
func loadIndex(dist fs.FS, opts Options) []byte {
	b, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // not built; serveIndex says so
		}
		return nil
	}

	b = setTitle(b, opts.Title)

	// A trailing slash is required: <base href="/logs"> would resolve
	// "./assets/x.js" against "/" and drop the sub-directory entirely.
	href := "/"
	if opts.Base != "" {
		href = opts.Base + "/"
	}
	head := []byte(`<base href="` + href + `">`)

	// The icon is given as an absolute path rather than left to <base>: it is
	// already built with the base in it, and a relative href would be resolved
	// against the current URL on a deep link.
	if opts.Favicon != nil {
		// The SPA's own link is removed rather than shadowed. With two icon
		// links a browser picks one by its own rules — commonly the last, which
		// would be the default — so leaving it in place would make the
		// configured icon a coin toss between engines.
		b = dropIconLinks(b)
		head = append(head, []byte(`<link rel="icon" type="`+opts.Favicon.ContentType+`" href="`+opts.Favicon.Path+`">`)...)
	}

	// Injected first inside <head>, so it precedes anything that could resolve
	// a relative URL — and, for the icon, so it precedes the SPA's own link and
	// wins on the duplicate.
	if i := bytes.Index(b, []byte("<head>")); i >= 0 {
		out := make([]byte, 0, len(b)+len(head))
		out = append(out, b[:i+len("<head>")]...)
		out = append(out, head...)
		out = append(out, b[i+len("<head>"):]...)
		return out
	}
	return b
}

// dropIconLinks removes every <link rel="icon"> already in the document.
//
// Deliberately narrow: it matches the tag this project's own index.html emits,
// and leaves anything it does not recognise alone rather than trying to be an
// HTML parser. If the SPA's template changes shape, the worst case is two icon
// links again — which is why there is a test.
func dropIconLinks(b []byte) []byte {
	for {
		start := bytes.Index(b, []byte(`<link rel="icon"`))
		if start < 0 {
			return b
		}
		end := bytes.IndexByte(b[start:], '>')
		if end < 0 {
			return b
		}
		b = append(b[:start:start], b[start+end+1:]...)
	}
}

// setTitle replaces the contents of the <title> element.
//
// Rewritten rather than appended: two <title> tags are not an error a browser
// reports, it simply uses the first, so appending would leave the configured
// title silently ignored.
func setTitle(b []byte, title string) []byte {
	if title == "" {
		return b
	}

	open := bytes.Index(b, []byte("<title>"))
	if open < 0 {
		return b
	}
	close := bytes.Index(b[open:], []byte("</title>"))
	if close < 0 {
		return b
	}

	out := make([]byte, 0, len(b)+len(title))
	out = append(out, b[:open+len("<title>")]...)
	// Escaped: a title is deployment-controlled rather than user-controlled, but
	// a stray "<" would still break the parse and take the page with it.
	out = append(out, []byte(html.EscapeString(title))...)
	out = append(out, b[open+close:]...)
	return out
}

func serveIndex(w http.ResponseWriter, index []byte) {
	if index == nil {
		http.Error(w, "SPA not built: run `make web`", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(index)
}

func exists(dist fs.FS, p string) bool {
	f, err := dist.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	return err == nil && !st.IsDir()
}
