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
func Handler(dist fs.FS, base string) http.Handler {
	files := http.FileServer(http.FS(dist))
	index := loadIndex(dist, base)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")

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
func loadIndex(dist fs.FS, base string) []byte {
	b, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // not built; serveIndex says so
		}
		return nil
	}

	// A trailing slash is required: <base href="/logs"> would resolve
	// "./assets/x.js" against "/" and drop the sub-directory entirely.
	href := "/"
	if base != "" {
		href = base + "/"
	}
	tag := []byte(`<base href="` + href + `">`)

	// Injected first inside <head>, so it precedes anything that could resolve
	// a relative URL.
	if i := bytes.Index(b, []byte("<head>")); i >= 0 {
		out := make([]byte, 0, len(b)+len(tag))
		out = append(out, b[:i+len("<head>")]...)
		out = append(out, tag...)
		out = append(out, b[i+len("<head>"):]...)
		return out
	}
	return b
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
