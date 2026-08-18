package vl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Endpoint paths, one place, so a typo is a compile-time concern and the
// metrics labels and the requests can never disagree.
const (
	EndpointQuery       = "/select/logsql/query"
	EndpointTail        = "/select/logsql/tail"
	EndpointHits        = "/select/logsql/hits"
	EndpointFacets      = "/select/logsql/facets"
	EndpointFieldNames  = "/select/logsql/field_names"
	EndpointFieldValues = "/select/logsql/field_values"
	EndpointHealth      = "/health"
)

// Filters are constraints the caller may not opt out of, sent as
// extra_filters. VictoriaLogs propagates them into every subquery — inside
// `| join (...)`, `| union(...)`, `:in(<query>)` — which plain concatenation
// cannot do: a query that composed to `tenant:a (whatever they typed)` can still
// reach other tenants through a subquery, and the far end is the only place
// that knows about subqueries at all.
//
// This is what makes a tool's filter a constraint rather than a suggestion.
type Filters []string

func (f Filters) apply(form url.Values) {
	for _, filter := range f {
		if strings.TrimSpace(filter) == "" {
			continue
		}
		form.Add("extra_filters", filter)
	}
}

// Range is a half-open [Start, End) window. A zero bound is omitted, which
// VictoriaLogs reads as "the oldest/newest log there is".
type Range struct {
	Start time.Time
	End   time.Time
}

func (r Range) apply(form url.Values) {
	// RFC3339 with nanoseconds: VictoriaLogs accepts several formats, and this
	// is the one that is unambiguous about both the timezone and the precision.
	if !r.Start.IsZero() {
		form.Set("start", r.Start.UTC().Format(time.RFC3339Nano))
	}
	if !r.End.IsZero() {
		form.Set("end", r.End.UTC().Format(time.RFC3339Nano))
	}
}

type QueryParams struct {
	Query   string
	Range   Range
	Filters Filters

	// Limit asks VictoriaLogs for the N most recent matching entries. It is
	// what makes the result sorted: without it, lines are streamed in the order
	// they are found, which is not time order.
	Limit int
}

// Query returns the open NDJSON stream: one JSON log entry per line. The caller
// closes it, and closing it early is the documented way to abandon the query.
func (c *Client) Query(ctx context.Context, p QueryParams) (io.ReadCloser, error) {
	form := url.Values{}
	form.Set("query", p.Query)
	p.Range.apply(form)
	p.Filters.apply(form)
	if p.Limit > 0 {
		form.Set("limit", strconv.Itoa(p.Limit))
	}

	resp, err := c.post(ctx, EndpointQuery, form)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

type TailParams struct {
	Query   string
	Filters Filters

	// StartOffset backfills: how far back to read before following. Without it
	// the pane stays empty until something new is ingested, which reads as
	// broken rather than as quiet.
	StartOffset time.Duration

	// Offset is VictoriaLogs' own delivery delay, giving collectors time to
	// ship. Zero leaves its default (5s) alone.
	Offset time.Duration

	// RefreshInterval is how often the far end looks for new logs. Zero leaves
	// its default (1s) alone.
	RefreshInterval time.Duration
}

// Tail returns the open NDJSON stream of a live tail. It has no deadline of its
// own: the caller's context ends it.
func (c *Client) Tail(ctx context.Context, p TailParams) (io.ReadCloser, error) {
	form := url.Values{}
	form.Set("query", p.Query)
	p.Filters.apply(form)
	if p.StartOffset > 0 {
		form.Set("start_offset", durationArg(p.StartOffset))
	}
	if p.Offset > 0 {
		form.Set("offset", durationArg(p.Offset))
	}
	if p.RefreshInterval > 0 {
		form.Set("refresh_interval", durationArg(p.RefreshInterval))
	}

	resp, err := c.post(ctx, EndpointTail, form)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Hits is the histogram: bucket counts over the range, one series per group.
type Hits struct {
	Hits []HitsSeries `json:"hits"`
}

type HitsSeries struct {
	Fields     map[string]string `json:"fields"`
	Timestamps []time.Time       `json:"timestamps"`
	Values     []int64           `json:"values"`
	Total      int64             `json:"total"`
}

type HitsParams struct {
	Query   string
	Range   Range
	Filters Filters

	// Step is the bucket width. Required in practice: without it VictoriaLogs
	// picks its own, and the chart would silently change resolution.
	Step time.Duration

	// Field groups the buckets by a log field, e.g. "level". Optional.
	Field string
}

func (c *Client) Hits(ctx context.Context, p HitsParams) (Hits, error) {
	form := url.Values{}
	form.Set("query", p.Query)
	p.Range.apply(form)
	p.Filters.apply(form)
	if p.Step > 0 {
		form.Set("step", durationArg(p.Step))
	}
	if p.Field != "" {
		form.Set("field", p.Field)
	}

	var out Hits
	err := c.getJSON(ctx, EndpointHits, form, &out)
	return out, err
}

// Facets is what the sidebar shows: the most frequent values per field across
// the logs the current query selects.
type Facets struct {
	Facets []Facet `json:"facets"`
}

type Facet struct {
	FieldName string       `json:"field_name"`
	Values    []FacetValue `json:"values"`
}

type FacetValue struct {
	FieldValue string `json:"field_value"`
	Hits       int64  `json:"hits"`
}

type FacetsParams struct {
	Query   string
	Range   Range
	Filters Filters

	// Limit is values per field, not fields.
	Limit int
}

func (c *Client) Facets(ctx context.Context, p FacetsParams) (Facets, error) {
	form := url.Values{}
	form.Set("query", p.Query)
	p.Range.apply(form)
	p.Filters.apply(form)
	if p.Limit > 0 {
		form.Set("limit", strconv.Itoa(p.Limit))
	}

	var out Facets
	err := c.getJSON(ctx, EndpointFacets, form, &out)
	return out, err
}

// Values is the shape both field_names and field_values answer with.
type Values struct {
	Values []Value `json:"values"`
}

type Value struct {
	Value string `json:"value"`
	Hits  int64  `json:"hits"`
}

type ValuesParams struct {
	Query   string
	Range   Range
	Filters Filters

	// Field is required by field_values and ignored by field_names.
	Field string

	// Filter narrows to names/values containing this substring — the typing in
	// the autocomplete box.
	Filter string
	Limit  int
}

func (c *Client) FieldNames(ctx context.Context, p ValuesParams) (Values, error) {
	return c.values(ctx, EndpointFieldNames, p)
}

func (c *Client) FieldValues(ctx context.Context, p ValuesParams) (Values, error) {
	if p.Field == "" {
		return Values{}, fmt.Errorf("field_values: field is required")
	}
	return c.values(ctx, EndpointFieldValues, p)
}

func (c *Client) values(ctx context.Context, endpoint string, p ValuesParams) (Values, error) {
	form := url.Values{}
	form.Set("query", p.Query)
	p.Range.apply(form)
	p.Filters.apply(form)
	if p.Field != "" {
		form.Set("field", p.Field)
	}
	if p.Filter != "" {
		form.Set("filter", p.Filter)
	}
	if p.Limit > 0 {
		form.Set("limit", strconv.Itoa(p.Limit))
	}

	var out Values
	err := c.getJSON(ctx, endpoint, form, &out)
	return out, err
}

// Ping is the liveness probe behind vlui_vl_up. GET, not POST: /health is the
// one endpoint here that is not a query.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.opt.Timeout)
	defer cancel()

	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+EndpointHealth, nil)
	if err != nil {
		return err
	}
	c.applyAuth(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		c.report(EndpointHealth, "error", started)
		return fmt.Errorf("victorialogs %s: %w", EndpointHealth, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrBody))

	if resp.StatusCode != http.StatusOK {
		c.report(EndpointHealth, strconv.Itoa(resp.StatusCode), started)
		return &Error{Endpoint: EndpointHealth, StatusCode: resp.StatusCode}
	}
	c.report(EndpointHealth, "ok", started)
	return nil
}

// durationArg renders a duration the way VictoriaLogs parses it. Go's own
// String() emits things like "1h0m0s" and "1.5s", both of which it accepts, but
// millisecond precision is all any of these args need and whole numbers are
// what an operator sees in a log line.
func durationArg(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
}
