package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yeti-switch/vlui/internal/config"
	"github.com/yeti-switch/vlui/internal/vl"
)

// queryFor is the query to send upstream, which is not always the one the
// caller typed.
//
// An empty box is legitimate once a tool is selected: the tool's filter IS the
// query, and "show me everything this tool covers" is the obvious first thing
// to ask. LogsQL has no empty query, so it becomes `*` — and the tool's filter
// still rides along as extra_filters, so this is bounded by the tool and by the
// time range, not a read of the whole retention.
//
// Without a tool filter, an empty query stays an error: there would be nothing
// at all narrowing it, and a request with no query is more likely a bug in a
// caller than an intention.
func queryFor(r *http.Request, tool *config.Tool) (string, error) {
	if q := strings.TrimSpace(r.FormValue("query")); q != "" {
		return q, nil
	}
	if tool != nil && tool.Query != "" {
		return "*", nil
	}
	return "", errors.New("query is required, unless the selected tool defines one")
}

// timeRange reads start/end. Both are optional: a missing end means now, and a
// missing start means end minus the configured default range, so a bare query
// from a fresh tab is bounded rather than reading the whole retention.
func (s *Server) timeRange(r *http.Request) (vl.Range, error) {
	end, err := parseTime(r.FormValue("end"))
	if err != nil {
		return vl.Range{}, fmt.Errorf("end: %w", err)
	}
	if end.IsZero() {
		end = time.Now()
	}

	start, err := parseTime(r.FormValue("start"))
	if err != nil {
		return vl.Range{}, fmt.Errorf("start: %w", err)
	}
	if start.IsZero() {
		start = end.Add(-s.cfg.VictoriaLogs.DefaultRange)
	}

	if !start.Before(end) {
		return vl.Range{}, fmt.Errorf("start (%s) must be before end (%s)",
			start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
	}
	return vl.Range{Start: start, End: end}, nil
}

// parseTime accepts RFC3339 or unix milliseconds. Milliseconds because that is
// what Date.getTime() hands the SPA, RFC3339 because that is what a human pastes
// out of a log line.
func parseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.UnixMilli(ms), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is neither RFC3339 nor unix milliseconds", v)
	}
	return t, nil
}

// limit clamps to max_rows. Clamped rather than rejected: an operator who typed
// 100000 wants as much as they can have, and failing the query instead would
// only teach them to retype it.
func (s *Server) limit(r *http.Request) int {
	max := s.cfg.VictoriaLogs.MaxRows

	v := r.FormValue("limit")
	if v == "" {
		return s.cfg.VictoriaLogs.DefaultLimit
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return s.cfg.VictoriaLogs.DefaultLimit
	}
	if n > max {
		return max
	}
	return n
}

// optDuration reads a duration argument like "5m", falling back to def.
func optDuration(r *http.Request, name string, def time.Duration) time.Duration {
	v := r.FormValue(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func optInt(r *http.Request, name string, def int) int {
	n, err := strconv.Atoi(r.FormValue(name))
	if err != nil || n <= 0 {
		return def
	}
	return n
}
