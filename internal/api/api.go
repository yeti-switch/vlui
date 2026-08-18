// Package api is the HTTP surface the SPA talks to.
//
// Everything under /api is a thin, opinionated wrapper over VictoriaLogs:
// parameters are validated and clamped here, and the answers are forwarded as
// they arrive. Nothing is stored, cached or aggregated — this process holds no
// state beyond the configuration it started with.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/yeti-switch/vlui/internal/auth"
	"github.com/yeti-switch/vlui/internal/config"
	"github.com/yeti-switch/vlui/internal/metrics"
	"github.com/yeti-switch/vlui/internal/vl"
)

type Server struct {
	cfg  config.Config
	vl   *vl.Client
	auth *auth.Auth // nil when authentication is disabled
	m    *metrics.Metrics
	log  *slog.Logger

	version string
	commit  string
}

func New(cfg config.Config, client *vl.Client, a *auth.Auth, m *metrics.Metrics, log *slog.Logger, version, commit string) *Server {
	return &Server{cfg: cfg, vl: client, auth: a, m: m, log: log, version: version, commit: commit}
}

// Routes returns the /api subtree. The caller mounts it under base_path.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(s.observe)

	// Auth endpoints sit OUTSIDE the middleware below: logging in cannot
	// require being logged in.
	if s.auth != nil {
		r.Mount("/auth", s.auth.Routes())
	}

	r.Group(func(r chi.Router) {
		if s.auth != nil {
			r.Use(s.auth.Middleware)
		}

		// Bootstrap for the SPA. Inside the guarded group: what queries this
		// deployment presets is not information for an anonymous caller.
		r.Get("/config", s.handleConfig)

		// POST, because a LogsQL query is easily longer than a URL may be.
		r.Post("/query", s.handleQuery)

		r.Get("/hits", s.handleHits)
		r.Get("/facets", s.handleFacets)
		r.Get("/field_names", s.handleFieldNames)
		r.Get("/field_values", s.handleFieldValues)

		// GET, because EventSource cannot POST. Tail queries are short.
		r.Get("/tail", s.handleTail)
	})

	return r
}

// observe records every served request under its route pattern rather than its
// path, so a thousand distinct field names cannot become a thousand series.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		route := "unknown"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		s.m.ObserveHTTP(route, strconv.Itoa(ww.Status()), time.Since(started))
	})
}

// handleConfig tells the SPA everything it cannot know for itself: where it is
// mounted, whether anyone is signed in, what the defaults and caps are, and
// which preset queries this deployment offers.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	type presetJSON struct {
		Name  string `json:"name"`
		Query string `json:"query"`
	}

	type toolJSON struct {
		ID      string `json:"id"`
		Tooltip string `json:"tooltip"`
		Icon    string `json:"icon"`
		Query   string `json:"query"`
	}

	out := struct {
		Version      string       `json:"version"`
		Commit       string       `json:"commit"`
		BasePath     string       `json:"base_path"`
		AuthEnabled  bool         `json:"auth_enabled"`
		User         *auth.User   `json:"user"`
		DefaultLimit int          `json:"default_limit"`
		MaxRows      int          `json:"max_rows"`
		DefaultRange float64      `json:"default_range_seconds"`
		TailMaxSecs  float64      `json:"tail_max_seconds"`
		Queries      []presetJSON `json:"queries"`
		Tools        []toolJSON   `json:"tools"`
	}{
		Version:      s.version,
		Commit:       s.commit,
		BasePath:     s.cfg.BasePath,
		AuthEnabled:  s.auth != nil,
		DefaultLimit: s.cfg.VictoriaLogs.DefaultLimit,
		MaxRows:      s.cfg.VictoriaLogs.MaxRows,
		DefaultRange: s.cfg.VictoriaLogs.DefaultRange.Seconds(),
		TailMaxSecs:  s.cfg.VictoriaLogs.TailMaxDuration.Seconds(),
		Queries:      make([]presetJSON, 0, len(s.cfg.Queries)),
		Tools:        make([]toolJSON, 0, len(s.cfg.Tools)),
	}
	if u, ok := auth.UserFrom(r.Context()); ok {
		out.User = &u
	}
	for _, q := range s.cfg.Queries {
		out.Queries = append(out.Queries, presetJSON{Name: q.Name, Query: q.Query})
	}
	// The query is published so the SPA can show it beside the input as the
	// prefix it is — but it is the SERVER that applies it, on every request,
	// from the tool id. What the browser knows here is a label, not a control.
	//
	// Only the tools this caller may actually select are listed: a rail full of
	// icons that answer 403 would be a worse experience than a shorter rail.
	for i := range s.cfg.Tools {
		t := &s.cfg.Tools[i]
		if !s.permitted(r, t) {
			continue
		}
		out.Tools = append(out.Tools, toolJSON{ID: t.ID, Tooltip: t.Tooltip, Icon: t.Icon, Query: t.Query})
	}

	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":"encode"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}
