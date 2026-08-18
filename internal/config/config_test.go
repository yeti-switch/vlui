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
  - tooltip: "main"
    icon: gear
  - tooltip: "Yeti Logs"
    icon: yeti
    query: "named_tags.system: yeti"
  - tooltip: "API Logs"
    icon: bolt
    query: "system: api"
`))
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Tools) != 3 {
		t.Fatalf("got %d tools", len(cfg.Tools))
	}
	// The id is derived, not configured, so a link to a tool survives the list
	// being reordered.
	want := []string{"main", "yeti-logs", "api-logs"}
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

func TestToolValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			// Would render as a blank square with no way to tell why.
			name: "unknown icon",
			body: "tools:\n  - tooltip: X\n    icon: sparkles\n",
			want: "unknown icon",
		},
		{
			name: "no tooltip",
			body: "tools:\n  - icon: gear\n",
			want: "tooltip must be set",
		},
		{
			// The filter is prepended, so a pipe would swallow the operator's
			// own query into a stage they cannot see.
			name: "pipe in query",
			body: "tools:\n  - tooltip: X\n    icon: gear\n    query: 'error | stats count()'\n",
			want: "may not contain a pipe",
		},
		{
			name: "colliding ids",
			body: "tools:\n  - tooltip: 'API Logs'\n    icon: gear\n  - tooltip: 'api logs'\n    icon: bolt\n",
			want: "distinguishable tooltips",
		},
		{
			// Nothing to match the groups against, so the restriction would let
			// everyone through — worse than no restriction, because it reads
			// like one.
			name: "groups without auth",
			body: "tools:\n  - tooltip: X\n    icon: gear\n    query: 'a:b'\n    allowed_groups: [noc]\n",
			want: "needs auth.enabled",
		},
		{
			name: "groups on an unfiltered tool",
			body: "auth:\n  enabled: true\n  issuer: https://idp.example\n  client_id: x\n  redirect_url: https://x/cb\n  cookie_secret: 0123456789012345678901234567890123\ntools:\n  - tooltip: X\n    icon: gear\n    allowed_groups: [noc]\n",
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
