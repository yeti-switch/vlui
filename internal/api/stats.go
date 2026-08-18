package api

import (
	"context"
	"net/http"
	"time"

	"github.com/yeti-switch/vlui/internal/vl"
)

// handleHits feeds the histogram strip above the table.
func (s *Server) handleHits(w http.ResponseWriter, r *http.Request) {
	tool, err := s.resolveTool(r)
	if err != nil {
		s.toolFailed(w, r, err)
		return
	}
	query, err := queryFor(r, tool)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	rng, err := s.timeRange(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}

	// The step is chosen here rather than left to VictoriaLogs, so the chart
	// has a known number of bars and zooming changes the resolution
	// predictably instead of the far end's default doing it invisibly.
	step := optDuration(r, "step", niceStep(rng.End.Sub(rng.Start), targetBuckets))

	out, err := s.vl.Hits(r.Context(), vl.HitsParams{
		Query:   query,
		Range:   rng,
		Filters: filters(tool),
		Step:    step,
		Field:   r.FormValue("field"),
	})
	if err != nil {
		s.upstream(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, struct {
		vl.Hits
		StepSeconds float64 `json:"step_seconds"`
	}{Hits: out, StepSeconds: step.Seconds()})
}

// handleFacets feeds the field sidebar: the most frequent values per field
// across whatever the current query selects.
func (s *Server) handleFacets(w http.ResponseWriter, r *http.Request) {
	tool, err := s.resolveTool(r)
	if err != nil {
		s.toolFailed(w, r, err)
		return
	}
	query, err := queryFor(r, tool)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	rng, err := s.timeRange(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}

	out, err := s.vl.Facets(r.Context(), vl.FacetsParams{
		Query:   query,
		Range:   rng,
		Filters: filters(tool),
		Limit:   optInt(r, "limit", defaultFacetValues),
	})
	if err != nil {
		s.upstream(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFieldNames and handleFieldValues back the query line's autocomplete.
func (s *Server) handleFieldNames(w http.ResponseWriter, r *http.Request) {
	s.serveValues(w, r, func(ctx context.Context, p vl.ValuesParams) (vl.Values, error) {
		return s.vl.FieldNames(ctx, p)
	})
}

func (s *Server) handleFieldValues(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("field") == "" {
		s.badRequest(w, r, errFieldRequired)
		return
	}
	s.serveValues(w, r, func(ctx context.Context, p vl.ValuesParams) (vl.Values, error) {
		return s.vl.FieldValues(ctx, p)
	})
}

func (s *Server) serveValues(w http.ResponseWriter, r *http.Request, call func(context.Context, vl.ValuesParams) (vl.Values, error)) {
	// Autocomplete asks about the whole selection, so an empty query means
	// "every log", spelled the way LogsQL spells it.
	query := r.FormValue("query")
	if query == "" {
		query = "*"
	}
	rng, err := s.timeRange(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}

	tool, err := s.resolveTool(r)
	if err != nil {
		s.toolFailed(w, r, err)
		return
	}

	// Autocomplete is a read of the logs like any other: without the tool's
	// filter it would list field values from logs the tool excludes.
	out, err := call(r.Context(), vl.ValuesParams{
		Query:   query,
		Range:   rng,
		Filters: filters(tool),
		Field:   r.FormValue("field"),
		Filter:  r.FormValue("filter"),
		Limit:   optInt(r, "limit", defaultValueLimit),
	})
	if err != nil {
		s.upstream(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

const (
	// targetBuckets is how many bars the histogram aims for. Enough to show
	// shape, few enough that each stays wider than a couple of pixels.
	targetBuckets = 120

	defaultFacetValues = 10
	defaultValueLimit  = 200
)

var errFieldRequired = errorString("field is required")

type errorString string

func (e errorString) Error() string { return string(e) }

// niceStep rounds the ideal bucket width up to the next value a human reads
// without arithmetic. A 47-second bucket is technically optimal and tells
// nobody anything; a minute does.
func niceStep(window time.Duration, buckets int) time.Duration {
	if window <= 0 || buckets <= 0 {
		return time.Minute
	}
	ideal := window / time.Duration(buckets)

	ladder := []time.Duration{
		time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second,
		15 * time.Second, 30 * time.Second,
		time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute,
		15 * time.Minute, 30 * time.Minute,
		time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour,
		24 * time.Hour, 7 * 24 * time.Hour,
	}
	for _, d := range ladder {
		if ideal <= d {
			return d
		}
	}
	return ladder[len(ladder)-1]
}
