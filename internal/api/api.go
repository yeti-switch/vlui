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

	type fieldJSON struct {
		Name string `json:"name"`
		// Empty means the header shows the name.
		Label string `json:"label,omitempty"`
		// Names one of the value_styles below, which decides how this column's
		// values are drawn. Empty means plain text.
		Style string `json:"style,omitempty"`
	}

	// One rule of a value style, as the browser needs it: the range already
	// parsed into numbers, because the server has validated it and doing it
	// twice is how the two ends come to disagree about what "200-399" means.
	// Either end may be open — "1000-" is a threshold, not a band — so both are
	// pointers: an absent bound is absent, not zero. (It also keeps infinities
	// out of the JSON, which cannot carry them.)
	type rangeJSON struct {
		Lo *float64 `json:"lo,omitempty"`
		Hi *float64 `json:"hi,omitempty"`
	}
	type styleRuleJSON struct {
		Value   []string   `json:"value,omitempty"`
		Range   *rangeJSON `json:"range,omitempty"`
		Prefix  string     `json:"prefix,omitempty"`
		Default bool       `json:"default,omitempty"`
		// Absent when the rule only renames the value.
		Color string `json:"color,omitempty"`
		// What a matching value is shown as instead of itself. Absent when it
		// is shown as logged.
		Text string `json:"text,omitempty"`
		// What a matching value means, for the tooltip. Absent when the value
		// speaks for itself.
		Description string `json:"description,omitempty"`
	}
	type valueStyleJSON struct {
		// "tag" or "text"; the server has already filled in the default.
		Type  string          `json:"type"`
		Rules []styleRuleJSON `json:"rules"`
	}

	type toolJSON struct {
		ID      string      `json:"id"`
		Tooltip string      `json:"tooltip"`
		Icon    string      `json:"icon"`
		Letters string      `json:"letters"`
		Query   string      `json:"query"`
		Fields  []fieldJSON `json:"fields"`
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
		// Only the styles the tools above actually refer to: a style nobody on
		// this rail can reach is not this caller's business.
		ValueStyles map[string]valueStyleJSON `json:"value_styles,omitempty"`
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
		fields := make([]fieldJSON, 0, len(t.Fields))
		for _, f := range t.Fields {
			fields = append(fields, fieldJSON{Name: f.Name, Label: f.Label, Style: f.Style})
			if _, done := out.ValueStyles[f.Style]; f.Style == "" || done {
				continue
			}
			style := s.cfg.ValueStyles[f.Style]
			rules := make([]styleRuleJSON, 0, len(style.Rules))
			for _, r := range style.Rules {
				rule := styleRuleJSON{
					Value: r.Value, Prefix: r.Prefix, Default: r.Default,
					Color: r.Color, Text: r.Text, Description: r.Description,
				}
				if b, ok := r.Bounds(); ok {
					rule.Range = &rangeJSON{}
					if b.HasLo {
						rule.Range.Lo = &b.Lo
					}
					if b.HasHi {
						rule.Range.Hi = &b.Hi
					}
				}
				rules = append(rules, rule)
			}
			if out.ValueStyles == nil {
				out.ValueStyles = make(map[string]valueStyleJSON, 1)
			}
			out.ValueStyles[f.Style] = valueStyleJSON{Type: style.Type, Rules: rules}
		}
		out.Tools = append(out.Tools, toolJSON{
			ID: t.ID, Tooltip: t.Tooltip, Icon: t.Icon, Letters: t.Letters,
			Query: t.Query, Fields: fields,
		})
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
