package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yeti-switch/vlui/internal/api"
	"github.com/yeti-switch/vlui/internal/config"
	"github.com/yeti-switch/vlui/internal/vl"
)

// fakeVL is a stand-in for VictoriaLogs. It records the form of the last
// request so a test can assert on what we asked for, not only on what we did
// with the answer.
type fakeVL struct {
	*httptest.Server

	lastPath string
	lastForm url.Values
	handler  func(w http.ResponseWriter, r *http.Request)
}

func newFakeVL(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *fakeVL {
	t.Helper()
	f := &fakeVL{handler: h}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.lastPath = r.URL.Path
		f.lastForm = r.PostForm
		f.handler(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func testServer(t *testing.T, upstream string, tweak func(*config.Config)) http.Handler {
	t.Helper()

	cfg := config.Default()
	cfg.VictoriaLogs.URL = upstream
	if tweak != nil {
		tweak(&cfg)
	}

	client := vl.New(vl.Options{URL: cfg.VictoriaLogs.URL, Timeout: cfg.VictoriaLogs.Timeout}, nil)
	log := slog.New(slog.DiscardHandler)

	// nil auth and nil metrics: both are the "disabled" case, and both must be
	// callable without a branch at every call site.
	return api.New(cfg, client, nil, nil, log, "test", "abc1234").Routes()
}

func TestQueryStreamsAndEnforcesItsOwnLimit(t *testing.T) {
	// The upstream ignores the limit arg entirely. That is the point: the row
	// cap protects the browser, so it cannot depend on the far end agreeing.
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		for i := range 10 {
			_, _ = w.Write([]byte(`{"_msg":"line ` + strconv.Itoa(i) + `"}` + "\n"))
		}
	})

	h := testServer(t, f.URL, func(c *config.Config) {
		c.VictoriaLogs.MaxRows = 3
		c.VictoriaLogs.DefaultLimit = 3
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/query", url.Values{"query": {"error"}, "limit": {"1000"}}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := f.lastForm.Get("limit"); got != "3" {
		t.Errorf("upstream limit = %q, want the clamped 3", got)
	}
	if lines := nonEmptyLines(rec.Body.String()); len(lines) != 3 {
		t.Errorf("got %d rows, want the cap of 3:\n%s", len(lines), rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Errorf("Content-Type = %q, want NDJSON", ct)
	}
	// nginx buffers proxied responses by default, which would hold every row
	// back until the query finished.
	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Error("X-Accel-Buffering: no is missing; nginx would buffer the stream")
	}
}

// A LogsQL syntax error is the most common failure in this application, and the
// upstream message names the offending token. Losing it would be losing the
// only useful part of the response.
func TestQueryPassesUpstreamErrorThrough(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "cannot parse query: unexpected token \"|~\"", http.StatusBadRequest)
	})

	h := testServer(t, f.URL, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/query", url.Values{"query": {"foo |~ bar"}}))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (the user's query is wrong, not our server)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unexpected token") {
		t.Errorf("upstream message did not survive: %s", rec.Body.String())
	}
}

// An upstream that is unwell is a 502 here, whatever it called itself.
func TestQueryUpstream5xxBecomesBadGateway(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
	})

	h := testServer(t, f.URL, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/query", url.Values{"query": {"*"}}))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestQueryDefaultsToTheConfiguredWindow(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {})

	h := testServer(t, f.URL, func(c *config.Config) {
		c.VictoriaLogs.DefaultRange = 90 * time.Minute
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/query", url.Values{"query": {"*"}}))

	start, err := time.Parse(time.RFC3339Nano, f.lastForm.Get("start"))
	if err != nil {
		t.Fatalf("start %q: %v", f.lastForm.Get("start"), err)
	}
	end, err := time.Parse(time.RFC3339Nano, f.lastForm.Get("end"))
	if err != nil {
		t.Fatalf("end %q: %v", f.lastForm.Get("end"), err)
	}

	// A bare query must never read the whole retention.
	if got := end.Sub(start); got != 90*time.Minute {
		t.Errorf("default window = %s, want 90m", got)
	}
}

func TestQueryRejectsAnInvertedRange(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called for a range that cannot match anything")
	})

	h := testServer(t, f.URL, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/query", url.Values{
		"query": {"*"},
		"start": {"2026-01-02T00:00:00Z"},
		"end":   {"2026-01-01T00:00:00Z"},
	}))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// Unix milliseconds, because that is what Date.getTime() in the SPA produces.
func TestQueryAcceptsUnixMilliseconds(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {})

	h := testServer(t, f.URL, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/query", url.Values{
		"query": {"*"},
		"start": {"1767225600000"}, // 2026-01-01T00:00:00Z
		"end":   {"1767229200000"}, // 2026-01-01T01:00:00Z
	}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.lastForm.Get("start"); got != "2026-01-01T00:00:00Z" {
		t.Errorf("start = %q, want the RFC3339 form of the epoch millis", got)
	}
}

func TestHitsChoosesAReadableStep(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hits":[{"fields":{},"timestamps":[],"values":[],"total":0}]}`))
	})

	h := testServer(t, f.URL, nil)
	rec := httptest.NewRecorder()
	// One hour over ~120 buckets is 30s, which is on the ladder.
	h.ServeHTTP(rec, get("/hits", url.Values{
		"query": {"*"},
		"start": {"2026-01-01T00:00:00Z"},
		"end":   {"2026-01-01T01:00:00Z"},
	}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.lastForm.Get("step"); got != "30000ms" {
		t.Errorf("step = %q, want 30000ms", got)
	}

	var out struct {
		StepSeconds float64 `json:"step_seconds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// The chart has to draw bars of a known width, so the step it got must be
	// in the response rather than guessed at from the timestamps.
	if out.StepSeconds != 30 {
		t.Errorf("step_seconds = %v, want 30", out.StepSeconds)
	}
}

func TestFieldValuesRequiresAField(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called without a field")
	})

	h := testServer(t, f.URL, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, get("/field_values", url.Values{"query": {"*"}}))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// Autocomplete opens with an empty box, and "everything" is a legitimate thing
// to ask about.
func TestFieldNamesDefaultsToTheEverythingQuery(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"value":"_msg","hits":3}]}`))
	})

	h := testServer(t, f.URL, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, get("/field_names", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.lastForm.Get("query"); got != "*" {
		t.Errorf("query = %q, want *", got)
	}
}

func TestTailDeliversSSE(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"_msg":"first"}` + "\n" + `{"_msg":"second"}` + "\n"))
	})

	h := testServer(t, f.URL, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, get("/tail", url.Values{"query": {"error"}}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	body := rec.Body.String()
	for _, want := range []string{`data: {"_msg":"first"}`, `data: {"_msg":"second"}`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	// Without a backfill the pane sits empty until something new is ingested,
	// which reads as a broken tail.
	if f.lastForm.Get("start_offset") == "" {
		t.Error("tail must backfill by default")
	}
}

/* The backfill is trimmed HERE, to the caller's row cap.
 *
 * Otherwise "follow this, starting from the hour I was looking at" is a request
 * for every line of that hour, sent to a browser that keeps the newest few
 * hundred and drops the rest on the floor. */
func TestTailTrimsTheBackfillToTheRowCap(t *testing.T) {
	old := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)

	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		// Ten rows of history, oldest first, then one live row.
		for i := range 10 {
			fmt.Fprintf(w, `{"_time":%q,"_msg":"history %d"}`+"\n", old, i)
		}
		fmt.Fprintf(w, `{"_time":%q,"_msg":"live"}`+"\n",
			time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano))
	})

	h := testServer(t, f.URL, func(c *config.Config) { c.VictoriaLogs.MaxRows = 5000 })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, get("/tail", url.Values{"query": {"error"}, "limit": {"3"}}))

	body := rec.Body.String()

	// The newest three of the history, and none of the older seven.
	for _, want := range []string{"history 7", "history 8", "history 9", "live"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	for _, gone := range []string{"history 0", "history 6"} {
		if strings.Contains(body, gone) {
			t.Errorf("%q should have been trimmed from the backfill:\n%s", gone, body)
		}
	}
	// Order is preserved: history first, oldest of the kept rows first, then
	// what arrived live.
	if i, j := strings.Index(body, "history 7"), strings.Index(body, "history 9"); i > j {
		t.Error("the kept history came out in the wrong order")
	}
	if strings.Index(body, "live") < strings.Index(body, "history 9") {
		t.Error("the live row came out before the history")
	}
}

// A row this process cannot read a timestamp out of is a row the browser still
// gets: showing too much beats silently holding it back.
func TestTailPassesRowsWithoutATimestamp(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"_msg":"no timestamp here"}` + "\n"))
	})

	h := testServer(t, f.URL, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, get("/tail", url.Values{"query": {"error"}}))

	if !strings.Contains(rec.Body.String(), "no timestamp here") {
		t.Errorf("row was dropped:\n%s", rec.Body.String())
	}
}

func TestConfigTellsTheSPAWhatItCannotKnow(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {})

	h := testServer(t, f.URL, func(c *config.Config) {
		c.BasePath = "/logs"
		c.Queries = []config.Preset{{Name: "SIP errors", Query: `_stream:{app="sems"} error`}}
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, get("/config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		BasePath    string `json:"base_path"`
		AuthEnabled bool   `json:"auth_enabled"`
		User        any    `json:"user"`
		MaxRows     int    `json:"max_rows"`
		Queries     []struct {
			Name  string `json:"name"`
			Query string `json:"query"`
		} `json:"queries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	if out.BasePath != "/logs" {
		t.Errorf("base_path = %q", out.BasePath)
	}
	if out.AuthEnabled {
		t.Error("auth_enabled must be false when no Auth was wired in")
	}
	if out.User != nil {
		t.Error("there is no user when authentication is off")
	}
	if out.MaxRows != config.Default().VictoriaLogs.MaxRows {
		t.Errorf("max_rows = %d", out.MaxRows)
	}
	if len(out.Queries) != 1 || out.Queries[0].Name != "SIP errors" {
		t.Errorf("presets did not survive: %+v", out.Queries)
	}
}

func TestQueryRequiresAQuery(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called without a query")
	})

	h := testServer(t, f.URL, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postForm("/query", url.Values{}))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// --- helpers ----------------------------------------------------------------

func postForm(path string, form url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func get(path string, q url.Values) *http.Request {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return httptest.NewRequest(http.MethodGet, path, nil)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

var _ = io.Discard

// A rule that renames a value reaches the browser with its text, and without a
// colour when it has none — an empty "color" would be a class the stylesheet
// does not have, rather than the plain text the config asked for.
func TestConfigPublishesValueStyleText(t *testing.T) {
	f := newFakeVL(t, func(w http.ResponseWriter, r *http.Request) {})

	h := testServer(t, f.URL, func(c *config.Config) {
		c.ValueStyles = map[string]config.ValueStyle{
			"pop": {Type: config.StyleTag, Rules: []config.StyleRule{
				{Value: config.StringList{"1"}, Text: "Frankfurt"},
				{Value: config.StringList{"2"}, Text: "Amsterdam", Color: "info"},
			}},
		}
		c.Tools = []config.Tool{{
			ID: "sip", Tooltip: "SIP", Icon: "phone",
			Fields: []config.Field{{Name: "pop_id", Style: "pop"}},
		}}
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, get("/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		ValueStyles map[string]struct {
			Rules []map[string]any `json:"rules"`
		} `json:"value_styles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	rules := out.ValueStyles["pop"].Rules
	if len(rules) != 2 {
		t.Fatalf("pop rules = %v, want 2", rules)
	}
	if rules[0]["text"] != "Frankfurt" {
		t.Errorf("rules[0].text = %v, want Frankfurt", rules[0]["text"])
	}
	if _, ok := rules[0]["color"]; ok {
		t.Errorf("rules[0] carries a color it was not given: %v", rules[0])
	}
	if rules[1]["text"] != "Amsterdam" || rules[1]["color"] != "info" {
		t.Errorf("rules[1] = %v, want text Amsterdam in info", rules[1])
	}
}
