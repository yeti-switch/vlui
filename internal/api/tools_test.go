package api_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/yeti-switch/vlui/internal/api"
	"github.com/yeti-switch/vlui/internal/auth"
	"github.com/yeti-switch/vlui/internal/config"
	"github.com/yeti-switch/vlui/internal/vl"
)

// toolCfg is the shape the examples in the README use: a wide "main" tool
// first, then narrower ones.
func toolCfg(upstream string) config.Config {
	cfg := config.Default()
	cfg.VictoriaLogs.URL = upstream
	cfg.Tools = []config.Tool{
		{ID: "yeti-logs", Tooltip: "Yeti Logs", Icon: "yeti", Query: "named_tags.system:yeti"},
		{ID: "api-logs", Tooltip: "API Logs", Icon: "bolt", Query: "system:api"},
		{ID: "everything", Tooltip: "Everything", Icon: "globe"},
	}
	return cfg
}

func toolServer(t *testing.T, cfg config.Config, user *auth.User) http.Handler {
	t.Helper()

	client := vl.New(vl.Options{URL: cfg.VictoriaLogs.URL, Timeout: cfg.VictoriaLogs.Timeout}, nil)
	h := api.New(cfg, client, nil, nil, slog.New(slog.DiscardHandler), "test", "").Routes()

	if user == nil {
		return h
	}
	// Stands in for the auth middleware, which is what puts the user on the
	// context in a configuration that has authentication switched on.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), *user)))
	})
}

// The filter must reach VictoriaLogs as extra_filters, which it propagates into
// subqueries. Concatenating it into the query string instead would be
// escapable through `| union(...)` and friends.
func TestToolFilterIsSentAsExtraFilters(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {})
	h := toolServer(t, toolCfg(f.URL), nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/query", url.Values{"query": {"error"}, "tool": {"api-logs"}}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.lastForm["extra_filters"]; len(got) != 1 || got[0] != "system:api" {
		t.Errorf("extra_filters = %v, want [system:api]", got)
	}
	// The user's own query must arrive unmangled — the constraint is separate.
	if got := f.lastForm.Get("query"); got != "error" {
		t.Errorf("query = %q, want it passed through untouched", got)
	}
}

// The bypass this whole design exists to stop: a request that simply leaves the
// tool out must not read everything.
func TestOmittingTheToolFallsBackToTheFirstOneNotToNoFilter(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {})
	h := toolServer(t, toolCfg(f.URL), nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/query", url.Values{"query": {"*"}}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.lastForm["extra_filters"]; len(got) != 1 || got[0] != "named_tags.system:yeti" {
		t.Errorf("extra_filters = %v, want the first tool's filter", got)
	}
}

// Every endpoint that reads logs, not just the one that returns rows: field
// values would otherwise enumerate the contents of logs the tool excludes.
func TestEveryReadingEndpointCarriesTheFilter(t *testing.T) {
	cases := []struct {
		name string
		req  func() *http.Request
	}{
		{"query", func() *http.Request {
			return postForm("/query", url.Values{"query": {"*"}, "tool": {"api-logs"}})
		}},
		{"hits", func() *http.Request {
			return get("/hits", url.Values{"query": {"*"}, "tool": {"api-logs"}})
		}},
		{"facets", func() *http.Request {
			return get("/facets", url.Values{"query": {"*"}, "tool": {"api-logs"}})
		}},
		{"field_names", func() *http.Request {
			return get("/field_names", url.Values{"tool": {"api-logs"}})
		}},
		{"field_values", func() *http.Request {
			return get("/field_values", url.Values{"field": {"host"}, "tool": {"api-logs"}})
		}},
		{"tail", func() *http.Request {
			return get("/tail", url.Values{"query": {"*"}, "tool": {"api-logs"}})
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"values":[],"hits":[],"facets":[]}`))
			})
			h := toolServer(t, toolCfg(f.URL), nil)

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, tc.req())

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			if got := f.lastForm["extra_filters"]; len(got) != 1 || got[0] != "system:api" {
				t.Errorf("extra_filters = %v, want [system:api]", got)
			}
		})
	}
}

func TestUnknownToolIsRefused(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called for an unknown tool")
	})
	h := toolServer(t, toolCfg(f.URL), nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/query", url.Values{"query": {"*"}, "tool": {"made-up"}}))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// A restricted tool must refuse the caller who is not in its groups — including
// when they name it by id, which is what curl would do.
func TestRestrictedToolChecksGroups(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {})

	cfg := toolCfg(f.URL)
	cfg.Auth.Enabled = true
	cfg.Tools[1].AllowedGroups = []string{"api-team"}

	t.Run("outsider is refused", func(t *testing.T) {
		h := toolServer(t, cfg, &auth.User{Subject: "u1", Groups: []string{"noc"}})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postForm("/query", url.Values{"query": {"*"}, "tool": {"api-logs"}}))

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("member is allowed", func(t *testing.T) {
		h := toolServer(t, cfg, &auth.User{Subject: "u2", Groups: []string{"api-team"}})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postForm("/query", url.Values{"query": {"*"}, "tool": {"api-logs"}}))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if got := f.lastForm["extra_filters"]; len(got) != 1 || got[0] != "system:api" {
			t.Errorf("extra_filters = %v", got)
		}
	})
}

// The rail should not offer what the API would refuse.
func TestConfigListsOnlyPermittedTools(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {})

	cfg := toolCfg(f.URL)
	cfg.Auth.Enabled = true
	cfg.Tools[1].AllowedGroups = []string{"api-team"}

	h := toolServer(t, cfg, &auth.User{Subject: "u1", Groups: []string{"noc"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, get("/config", nil))

	var out struct {
		Tools []struct {
			ID    string `json:"id"`
			Query string `json:"query"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	var ids []string
	for _, tool := range out.Tools {
		ids = append(ids, tool.ID)
	}
	if strings.Join(ids, ",") != "yeti-logs,everything" {
		t.Errorf("tools = %v, want the restricted one left out", ids)
	}
}

// With no tools configured nothing is constrained and nothing is sent — the
// deployments that do not use the rail are unaffected.
func TestNoToolsMeansNoFilter(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {})
	h := testServer(t, f.URL, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/query", url.Values{"query": {"*"}}))

	if got := f.lastForm["extra_filters"]; len(got) != 0 {
		t.Errorf("extra_filters = %v, want none", got)
	}
}

// An empty box with a tool selected is the obvious first thing to ask: show me
// everything this tool covers.
func TestEmptyQueryIsAllowedWhenTheToolDefinesOne(t *testing.T) {
	for _, endpoint := range []string{"query", "hits", "facets", "tail"} {
		t.Run(endpoint, func(t *testing.T) {
			f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"hits":[],"facets":[]}`))
			})
			h := toolServer(t, toolCfg(f.URL), nil)

			rec := httptest.NewRecorder()
			if endpoint == "query" {
				h.ServeHTTP(rec, postForm("/query", url.Values{"query": {""}, "tool": {"api-logs"}}))
			} else {
				h.ServeHTTP(rec, get("/"+endpoint, url.Values{"query": {""}, "tool": {"api-logs"}}))
			}

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			// LogsQL has no empty query, so it becomes the everything filter —
			// with the tool's own filter still constraining it.
			if got := f.lastForm.Get("query"); got != "*" {
				t.Errorf("query = %q, want *", got)
			}
			if got := f.lastForm["extra_filters"]; len(got) != 1 || got[0] != "system:api" {
				t.Errorf("extra_filters = %v, want the tool's filter", got)
			}
		})
	}
}

// A whitespace-only box is an empty one; it must not reach VictoriaLogs as a
// query made of spaces.
func TestBlankQueryCountsAsEmpty(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {})
	h := toolServer(t, toolCfg(f.URL), nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/query", url.Values{"query": {"   "}, "tool": {"yeti-logs"}}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.lastForm.Get("query"); got != "*" {
		t.Errorf("query = %q, want *", got)
	}
}

// Without a filter from anywhere there is nothing narrowing the read, so the
// error stays — and says what would make it acceptable.
func TestEmptyQueryStillFailsWithoutAToolFilter(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called")
	})

	t.Run("no tools configured", func(t *testing.T) {
		h := testServer(t, f.URL, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postForm("/query", url.Values{"query": {""}}))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "unless the selected tool defines one") {
			t.Errorf("error does not say what would make it work: %s", rec.Body.String())
		}
	})

	t.Run("tool without a query", func(t *testing.T) {
		h := toolServer(t, toolCfg(f.URL), nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postForm("/query", url.Values{"query": {""}, "tool": {"everything"}}))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
}
