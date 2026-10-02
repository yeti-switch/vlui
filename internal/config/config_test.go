package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeti-switch/vlui/internal/config"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEmptyPathYieldsDefaults(t *testing.T) {
	// `-config /dev/null` and the CI smoke test both depend on this.
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:8080" {
		t.Errorf("listen = %q", cfg.Listen)
	}
	// The exporter is opt-in: a config that never mentions metrics must not
	// open a second socket.
	if cfg.Metrics.Listen != "" {
		t.Errorf("metrics.listen = %q, want empty — the exporter must be off unless asked for", cfg.Metrics.Listen)
	}
}

// Three ways of not asking for an exporter, all of which must mean the same
// thing. The middle one is the trap: a `metrics:` block present for its path or
// probe interval, with no address, is still not a request for a listener.
func TestMetricsIsOptional(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"no metrics block", "listen: 127.0.0.1:8080\n"},
		{"block without listen", "metrics:\n  path: /metrics\n  probe_interval: 30s\n"},
		{"listen explicitly empty", "metrics:\n  listen: \"\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(write(t, tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Metrics.Listen != "" {
				t.Errorf("metrics.listen = %q, want empty", cfg.Metrics.Listen)
			}
		})
	}

	// And naming an address turns it on, keeping the other defaults.
	cfg, err := config.Load(write(t, "metrics:\n  listen: 127.0.0.1:9108\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metrics.Listen != "127.0.0.1:9108" {
		t.Errorf("metrics.listen = %q", cfg.Metrics.Listen)
	}
	if cfg.Metrics.Path != "/metrics" {
		t.Errorf("metrics.path = %q, want the default to survive", cfg.Metrics.Path)
	}
	if cfg.Metrics.ProbeInterval == 0 {
		t.Error("metrics.probe_interval lost its default")
	}
}

// A misspelled key is otherwise a silent no-op, and the operator finds out when
// the setting they thought they changed did nothing.
func TestUnknownKeyIsAnError(t *testing.T) {
	_, err := config.Load(write(t, "listen: 0.0.0.0:8080\nvictoria_logs:\n  url: http://x:9428\n"))
	if err == nil {
		t.Fatal("a typo in a key must not load silently")
	}
	if !strings.Contains(err.Error(), "victoria_logs") {
		t.Errorf("the error must name the offending key, got: %v", err)
	}
}

func TestDurationsAndOverrides(t *testing.T) {
	cfg, err := config.Load(write(t, `
listen: 0.0.0.0:9000
base_path: logs/
victorialogs:
  url: http://vl.example:9428/
  timeout: 90s
  default_range: 6h
  max_rows: 10000
  default_limit: 1000
  tenant:
    account_id: 12
    project_id: 34
metrics:
  probe_interval: 0s
  path: custom
queries:
  - name: SIP errors
    query: '_stream:{app="sems"} error'
`))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.VictoriaLogs.Timeout != 90*time.Second {
		t.Errorf("timeout = %s", cfg.VictoriaLogs.Timeout)
	}
	if cfg.VictoriaLogs.DefaultRange != 6*time.Hour {
		t.Errorf("default_range = %s", cfg.VictoriaLogs.DefaultRange)
	}
	// Every consumer concatenates the base path, so it is normalised once here.
	if cfg.BasePath != "/logs" {
		t.Errorf("base_path = %q, want /logs", cfg.BasePath)
	}
	// A trailing slash on the URL would produce "…9428//select/logsql/query".
	if cfg.VictoriaLogs.URL != "http://vl.example:9428" {
		t.Errorf("url = %q", cfg.VictoriaLogs.URL)
	}
	if cfg.Metrics.Path != "/custom" {
		t.Errorf("metrics.path = %q, want a leading slash", cfg.Metrics.Path)
	}
	if cfg.VictoriaLogs.Tenant.AccountID != 12 || cfg.VictoriaLogs.Tenant.ProjectID != 34 {
		t.Errorf("tenant = %+v", cfg.VictoriaLogs.Tenant)
	}
	// Explicitly zero, not the default: this is how probing is turned off.
	if cfg.Metrics.ProbeInterval != 0 {
		t.Errorf("probe_interval = %s, want 0", cfg.Metrics.ProbeInterval)
	}
	if len(cfg.Queries) != 1 || cfg.Queries[0].Name != "SIP errors" {
		t.Errorf("queries = %+v", cfg.Queries)
	}
}

func TestValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			// The UI would open asking for more rows than it is ever allowed.
			name: "default limit above the cap",
			body: "victorialogs:\n  max_rows: 100\n  default_limit: 500\n",
			want: "exceeds max_rows",
		},
		{
			name: "url without a scheme",
			body: "victorialogs:\n  url: vl.example:9428\n",
			want: "absolute http(s) URL",
		},
		{
			name: "preset with no query",
			body: "queries:\n  - name: broken\n",
			want: "both name and query",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, tc.body))
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestTools(t *testing.T) {
	cfg, err := config.Load(write(t, `
tools:
  - id: main
    tooltip: "main"
    icon: gear
  - id: yeti
    tooltip: "Yeti Logs"
    icon: yeti
    query: "named_tags.system: yeti"
  - id: apilogs
    tooltip: "API Logs"
    icon: bolt
    query: "system: api"
`))
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Tools) != 3 {
		t.Fatalf("got %d tools", len(cfg.Tools))
	}
	// The id is configured, and the order is the file's — the first tool is
	// what a request naming no tool gets, so both matter.
	want := []string{"main", "yeti", "apilogs"}
	for i, id := range want {
		if cfg.Tools[i].ID != id {
			t.Errorf("tools[%d].id = %q, want %q", i, cfg.Tools[i].ID, id)
		}
	}
	// A tool with no query is legitimate: it is the "everything" entry.
	if cfg.Tools[0].Query != "" {
		t.Errorf("tools[0].query = %q, want empty", cfg.Tools[0].Query)
	}
}

// The id is what the URL carries and what every request sends, so it cannot be
// optional and cannot repeat.
func TestToolIDs(t *testing.T) {
	cases := map[string]string{
		"tools:\n  - tooltip: X\n    icon: gear\n":                           "id must be set",
		"tools:\n  - id: a b\n    icon: gear\n":                              "use letters, digits",
		"tools:\n  - id: api/logs\n    icon: gear\n":                         "use letters, digits",
		"tools:\n  - id: sip\n    icon: gear\n  - id: sip\n    icon: bolt\n": "has to be unique",
		// The id is a path segment now, so it cannot be one the server answers
		// on itself: /api is the API and /healthz is the health check, and a
		// tool living there would be a link that returns anything but the UI.
		"tools:\n  - id: api\n    icon: gear\n":     `id "api" is one of the paths this app answers itself`,
		"tools:\n  - id: healthz\n    icon: gear\n": `id "healthz" is one of the paths this app answers itself`,
		"tools:\n  - id: assets\n    icon: gear\n":  `id "assets" is one of the paths this app answers itself`,
		// /icons is the sheet of every icon name this config accepts.
		"tools:\n  - id: icons\n    icon: gear\n": `id "icons" is one of the paths this app answers itself`,
	}
	for body, want := range cases {
		t.Run(want, func(t *testing.T) {
			_, err := config.Load(write(t, body))
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to mention %q", err, want)
			}
		})
	}

	// A tooltip is the label; without one the id is a perfectly good label.
	cfg, err := config.Load(write(t, "tools:\n  - id: billing\n    letters: B\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools[0].Tooltip != "billing" {
		t.Errorf("tooltip = %q, want it to fall back to the id", cfg.Tools[0].Tooltip)
	}

	// Non-ASCII ids are fine: they are percent-encoded in the URL.
	if _, err := config.Load(write(t, "tools:\n  - id: цод\n    letters: ЦОД\n")); err != nil {
		t.Errorf("a Cyrillic id was refused: %v", err)
	}
}

func TestToolValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			// Would render as a blank square with no way to tell why.
			name: "unknown icon",
			body: "tools:\n  - id: x\n    icon: sparkles\n",
			want: "unknown icon",
		},
		{
			// The filter is combined with the operator's query, so a pipe would
			// swallow it into a stage they cannot see.
			name: "pipe in query",
			body: "tools:\n  - id: x\n    icon: gear\n    query: 'error | stats count()'\n",
			want: "may not contain a pipe",
		},
		{
			// Nothing to match the groups against, so the restriction would let
			// everyone through — worse than no restriction, because it reads
			// like one.
			name: "groups without auth",
			body: "tools:\n  - id: x\n    icon: gear\n    query: 'a:b'\n    allowed_groups: [noc]\n",
			want: "needs auth.enabled",
		},
		{
			name: "groups on an unfiltered tool",
			body: "auth:\n  enabled: true\n  issuer: https://idp.example\n  client_id: x\n  redirect_url: https://x/cb\n  cookie_secret: 0123456789012345678901234567890123\ntools:\n  - id: x\n    icon: gear\n    allowed_groups: [noc]\n",
			want: "restricts nothing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, tc.body))
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestToolFields(t *testing.T) {
	cfg, err := config.Load(write(t, `
tools:
  - id: main
    tooltip: main
    icon: gear
  - id: yeti
    tooltip: Yeti Logs
    icon: yeti
    query: 'a:b'
    fields: [_time, level, host, _msg]
`))
	if err != nil {
		t.Fatal(err)
	}

	// Absent means the UI's own default, not an error: most tools want _time
	// and _msg like everything else.
	if cfg.Tools[0].Fields != nil {
		t.Errorf("tools[0].fields = %v, want nil", cfg.Tools[0].Fields)
	}
	want := []string{"_time", "level", "host", "_msg"}
	got := make([]string, 0, len(cfg.Tools[1].Fields))
	for _, f := range cfg.Tools[1].Fields {
		got = append(got, f.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tools[1].fields = %v, want %v", got, want)
	}

	// An empty entry would render as a column with no name and no values.
	if _, err := config.Load(write(t, "tools:\n  - id: x\n    tooltip: X\n    icon: gear\n    fields: ['_time', '']\n")); err == nil {
		t.Error("an empty field name must be refused")
	}
}

/* Value styles: how a field's values are drawn, and which value gets which
 * colour.
 *
 * Everything here is refused at startup rather than at render time — a colour
 * the stylesheet does not have, or a range written backwards, would otherwise
 * show up as an uncoloured value three screens into a result set. */
func TestValueStyles(t *testing.T) {
	cfg, err := config.Load(write(t, `
value_styles:
  http_status:
    rules:
      - {range: 200-399, color: ok}
      - {range: 400-499, color: warn}
      - {range: 500-599, color: error}
  level:
    type: tag
    rules:
      - {value: [error, fatal], color: error}
      - {value: warn, color: warn}
      - {prefix: "deb", color: muted}
      - {default: true, color: neutral}
  slow:
    type: text
    rules:
      - {range: 1000-, color: error, description: over a second}
      - {range: -999, color: ok}
  pop:
    rules:
      - {value: "1", text: Frankfurt}
      - {value: ["2", "3"], text: Amsterdam, color: info}

tools:
  - id: web
    tooltip: Web
    icon: globe
    fields:
      - _time
      - {name: payload.status, label: status, style: http_status}
      - {name: duration, style: slow}
`))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Tools[0].Fields[1].Style != "http_status" {
		t.Errorf("fields[1].style = %q", cfg.Tools[0].Fields[1].Style)
	}
	// A style that does not say how it draws gets the pill, filled in here so
	// the UI never has to know what the default is.
	if got := cfg.ValueStyles["http_status"].Type; got != config.StyleTag {
		t.Errorf("http_status.type = %q, want %q", got, config.StyleTag)
	}
	if got := cfg.ValueStyles["slow"].Type; got != config.StyleText {
		t.Errorf("slow.type = %q, want %q", got, config.StyleText)
	}

	if b, ok := cfg.ValueStyles["http_status"].Rules[1].Bounds(); !ok || b.Lo != 400 || b.Hi != 499 {
		t.Errorf("http_status.rules[1] bounds = %+v (%v), want 400-499", b, ok)
	}
	// A single value does not have to be written as a list of one.
	if got := cfg.ValueStyles["level"].Rules[1].Value; len(got) != 1 || got[0] != "warn" {
		t.Errorf("level.rules[1].value = %v, want [warn]", got)
	}
	// A rule with no range has no bounds, rather than 0-0.
	if _, ok := cfg.ValueStyles["level"].Rules[0].Bounds(); ok {
		t.Error("a value rule reports bounds")
	}

	// The description rides along with the colour: what the value MEANS, which
	// is the half a colour cannot carry.
	if got := cfg.ValueStyles["slow"].Rules[0].Description; got != "over a second" {
		t.Errorf("slow.rules[0].description = %q", got)
	}
	if got := cfg.ValueStyles["slow"].Rules[1].Description; got != "" {
		t.Errorf("slow.rules[1].description = %q, want empty", got)
	}

	// A rule may rename a value instead of — or as well as — colouring it.
	if r := cfg.ValueStyles["pop"].Rules[0]; r.Text != "Frankfurt" || r.Color != "" {
		t.Errorf("pop.rules[0] = %+v, want text Frankfurt and no color", r)
	}
	if r := cfg.ValueStyles["pop"].Rules[1]; r.Text != "Amsterdam" || r.Color != "info" {
		t.Errorf("pop.rules[1] = %+v, want text Amsterdam in info", r)
	}

	// A threshold: open at the top, because "slow past a second" has no upper
	// bound and inventing one loses the colour on the day it is exceeded.
	b, ok := cfg.ValueStyles["slow"].Rules[0].Bounds()
	if !ok || !b.HasLo || b.Lo != 1000 || b.HasHi {
		t.Errorf("slow.rules[0] bounds = %+v (%v), want 1000 and up", b, ok)
	}
	if b, _ := cfg.ValueStyles["slow"].Rules[1].Bounds(); b.HasLo || !b.HasHi || b.Hi != 999 {
		t.Errorf("slow.rules[1] bounds = %+v, want up to 999", b)
	}

	head := "tools:\n  - id: x\n    icon: gear\n    fields: [{name: a, style: s}]\nvalue_styles:\n  s:\n    rules:\n      - "
	cases := map[string]string{
		`{value: "200", color: chartreuse}`:      "not one this UI can draw",
		`{value: "200"}`:                         "no color and no text",
		`{value: "200", text: " "}`:              "no color and no text",
		`{value: "200", text: OK, color: teal}`:  "not one this UI can draw",
		`{color: ok}`:                            "no matcher",
		`{value: "200", prefix: "2", color: ok}`: "more than one",
		`{range: 599-500, color: ok}`:            "the low bound comes first",
		`{range: 2xx-3xx, color: ok}`:            "is not a number",
		`{range: "200", color: ok}`:              "want a dash",
		`{range: "-", color: ok}`:                "no bounds at all",
	}
	for rule, want := range cases {
		t.Run(want, func(t *testing.T) {
			_, err := config.Load(write(t, head+rule+"\n"))
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to mention %q", err, want)
			}
		})
	}

	// A type this UI cannot draw.
	if _, err := config.Load(write(t, "value_styles:\n  s:\n    type: pill\n    rules: [{default: true, color: ok}]\n")); err == nil ||
		!strings.Contains(err.Error(), `type is "pill"`) {
		t.Errorf("error = %v, want it to refuse an unknown type", err)
	}

	// A style with no rules draws nothing, which is not what anybody meant by
	// naming it on a field.
	if _, err := config.Load(write(t, "value_styles:\n  s:\n    type: text\n")); err == nil ||
		!strings.Contains(err.Error(), "no rules") {
		t.Errorf("error = %v, want it to refuse a style with no rules", err)
	}

	// A default that is not last leaves every rule after it dead.
	_, err = config.Load(write(t, "value_styles:\n  s:\n    rules:\n      - {default: true, color: ok}\n      - {value: x, color: warn}\n"))
	if err == nil || !strings.Contains(err.Error(), "default has to be the last rule") {
		t.Errorf("error = %v, want it to refuse a default that is not last", err)
	}

	// A field naming a style that does not exist, with the ones that do in the
	// message — the answer to "then what is there" belongs in the error.
	_, err = config.Load(write(t, "value_styles:\n  http_status:\n    rules: [{default: true, color: ok}]\ntools:\n  - id: x\n    icon: gear\n    fields: [{name: a, style: htpp_status}]\n"))
	if err == nil || !strings.Contains(err.Error(), "not in value_styles (http_status)") {
		t.Errorf("error = %v, want it to name the styles that exist", err)
	}
}

func TestToolLetters(t *testing.T) {
	cfg, err := config.Load(write(t, `
tools:
  - id: all
    tooltip: Everything
    icon: globe
  - id: apilogs
    tooltip: API Logs
    letters: API
    query: 'system: api'
  - id: цод
    tooltip: ЦОД
    letters: ЦОД
    query: 'system: dc'
`))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Tools[1].Letters != "API" || cfg.Tools[1].Icon != "" {
		t.Errorf("tools[1] = %+v", cfg.Tools[1])
	}
	// A tool named in Cyrillic is not doing anything unusual.
	if cfg.Tools[2].ID != "цод" {
		t.Errorf("tools[2].id = %q, want цод", cfg.Tools[2].ID)
	}
}

func TestToolIconOrLetters(t *testing.T) {
	cases := map[string]string{
		// Four characters means a clipped label, which is a worse legend than
		// none at all.
		"letters: TOOLONG":             "up to 3",
		"icon: gear\n    letters: API": "one or the other",
		"query: 'a:b'":                 "either icon or letters",
	}
	for body, want := range cases {
		t.Run(body, func(t *testing.T) {
			_, err := config.Load(write(t, "tools:\n  - id: x\n    "+body+"\n"))
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to mention %q", err, want)
			}
		})
	}

	// Counted in runes: three Cyrillic letters are six bytes and must be
	// accepted.
	if _, err := config.Load(write(t, "tools:\n  - id: dc\n    letters: ЦОД\n")); err != nil {
		t.Errorf("three multi-byte letters were refused: %v", err)
	}
}

// A field is either a name or a name with a label. Both shapes in one list,
// because requiring {name: _time} for every column to label one of them would
// be a poor trade.
func TestToolFieldLabels(t *testing.T) {
	cfg, err := config.Load(write(t, `
tools:
  - id: http
    icon: globe
    fields:
      - _time
      - _msg
      - {name: payload.method, label: method}
      - name: payload.response.status_code
        label: status
`))
	if err != nil {
		t.Fatal(err)
	}

	want := []config.Field{
		{Name: "_time"},
		{Name: "_msg"},
		{Name: "payload.method", Label: "method"},
		{Name: "payload.response.status_code", Label: "status"},
	}
	for i, w := range want {
		if cfg.Tools[0].Fields[i] != w {
			t.Errorf("fields[%d] = %+v, want %+v", i, cfg.Tools[0].Fields[i], w)
		}
	}
}

func TestToolFieldValidation(t *testing.T) {
	cases := map[string]string{
		// A typo in a key is otherwise a silent no-op: the label simply never
		// appears and nothing says why.
		"tools:\n  - id: x\n    icon: gear\n    fields:\n      - {name: a, lable: b}\n": "want name, label or style",
		"tools:\n  - id: x\n    icon: gear\n    fields:\n      - {label: nameless}\n":   "has no name",
		"tools:\n  - id: x\n    icon: gear\n    fields:\n      - [a, b]\n":              "either a name or",
	}
	for body, want := range cases {
		t.Run(want, func(t *testing.T) {
			_, err := config.Load(write(t, body))
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to mention %q", err, want)
			}
		})
	}
}
