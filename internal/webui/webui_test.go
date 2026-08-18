package webui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yeti-switch/vlui/internal/webui"
	"github.com/yeti-switch/vlui/web"
)

func get(t *testing.T, base, path string) *http.Response {
	t.Helper()
	h := webui.Handler(web.Dist(), base)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Result()
}

func body(t *testing.T, res *http.Response) string {
	t.Helper()
	b := make([]byte, 4096)
	n, _ := res.Body.Read(b)
	return string(b[:n])
}

func TestServesIndexAtRoot(t *testing.T) {
	res := get(t, "", "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (did `make web` run?)", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
}

// The history fallback. Without it, refreshing the browser on a deep link 404s
// — the single most common way an embedded SPA breaks.
func TestDeepLinkFallsBackToIndex(t *testing.T) {
	res := get(t, "", "/traffic/gateway-statistics")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: a deep link must serve index.html", res.StatusCode)
	}
}

// index.html must never be cached, or a browser pins itself to a stale build
// whose hashed assets no longer exist.
func TestIndexIsNotCached(t *testing.T) {
	res := get(t, "", "/")
	if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

// Vite emits content-hashed asset names, so those are safe to cache forever.
func TestHashedAssetsAreImmutable(t *testing.T) {
	asset := findAsset(t)
	res := get(t, "", "/"+asset)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d for %s, want 200", res.StatusCode, asset)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want an immutable asset header", cc)
	}
}

func findAsset(t *testing.T) string {
	t.Helper()
	entries, err := fsGlob(web.Dist(), "assets")
	if err != nil || len(entries) == 0 {
		t.Skip("no built assets; run `make web`")
	}
	return "assets/" + entries[0]
}

// --- base path --------------------------------------------------------------

// Mounted under a sub-directory, index.html must carry a <base href> so the
// browser resolves the relative asset URLs Vite emits ("./assets/…") against the
// sub-directory — and so the SPA knows where to send its API calls.
//
// The expectation is derived from the mount point rather than written out, and
// several are tried: asserting a literal "/logs" against an input of "/logs"
// would still pass if the handler ignored the base and hardcoded it.
//
// The trailing slash is not cosmetic either: <base href="/logs"> would resolve
// "./assets/x.js" against "/" and drop the sub-directory, 404ing every asset. So
// the root gets <base href="/"> too, and the SPA has one rule, not two.
func TestBaseHrefIsDerivedFromTheMountPoint(t *testing.T) {
	cases := map[string]string{
		"":          "/",
		"/logs":     "/logs/",
		"/a/b":      "/a/b/",
		"/ops/logs": "/ops/logs/",
	}
	for base, href := range cases {
		got := body(t, get(t, base, "/"))
		want := `<base href="` + href + `">`
		if !strings.Contains(got, want) {
			t.Errorf("base %q: want %s in the served index", base, want)
		}
	}
}

// The mount prefix is stripped before the handler runs, so it sees root-relative
// paths — a deep link must still fall back to the same base-aware index.
func TestDeepLinkUnderBasePathFallsBackToIndex(t *testing.T) {
	for _, base := range []string{"", "/logs", "/ops/logs"} {
		res := get(t, base, "/traffic/gateway-statistics")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("base %q: status = %d, want 200", base, res.StatusCode)
		}
		href := "/"
		if base != "" {
			href = base + "/"
		}
		if !strings.Contains(body(t, res), `<base href="`+href+`">`) {
			t.Errorf("base %q: a deep link should serve the base-aware index", base)
		}
	}
}
