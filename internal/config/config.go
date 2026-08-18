// Package config is the single YAML file the whole application is configured
// from. There is no database and no second source of truth: presets, auth and
// upstream all come from here, and everything that survives a restart is either
// in this file or in VictoriaLogs itself.
package config

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/yeti-switch/vlui/internal/auth"
)

type Config struct {
	// Listen is the address the UI and its API are served on. nginx terminates
	// TLS in front of it, so loopback is the normal value; a container must
	// listen on 0.0.0.0 instead.
	Listen string `yaml:"listen"`

	// BasePath mounts the whole app — SPA and API — under a sub-directory, so
	// it can share a domain with something else. Empty serves at the root.
	// Applied at runtime rather than baked into the build, so one binary works
	// at either.
	BasePath string `yaml:"base_path"`

	VictoriaLogs VictoriaLogs `yaml:"victorialogs"`
	Auth         auth.Config  `yaml:"auth"`
	Metrics      Metrics      `yaml:"metrics"`

	// Queries are preset LogsQL queries offered in a dropdown next to the query
	// line. Deployment knowledge — "how do I find SIP errors here" — belongs
	// with the deployment, not in every operator's browser history.
	Queries []Preset `yaml:"queries"`

	// Tools are the icons in the left rail. Each one narrows the whole session
	// to a slice of the logs — one system, one environment — by prepending its
	// query to whatever the operator types.
	Tools []Tool `yaml:"tools"`
}

// Tool is one icon in the rail.
//
// The query is a filter, not a whole query: it is prepended to what the
// operator types, and LogsQL ANDs adjacent filters. That is also why it may not
// contain a pipe — `error | stats count()` prepended to a user's query would
// put a pipe in the middle and change what every following stage operates on.
type Tool struct {
	// ID is derived from the tooltip, not configured. It is what the URL
	// carries, so a link to what you are looking at survives a reordering of
	// this list.
	ID string `yaml:"-"`

	// Tooltip is the label shown on hover. Required: an icon-only rail with an
	// unlabelled icon is a guessing game.
	Tooltip string `yaml:"tooltip"`

	// Icon names one of the shapes the UI ships. Unknown names are refused at
	// startup rather than rendering as a blank square.
	Icon string `yaml:"icon"`

	// Query is optional. A tool without one — "everything", usually first in
	// the list — selects nothing and filters nothing.
	//
	// It is applied by the SERVER, as an extra_filters constraint, not by the
	// browser: the API is reachable with curl by anyone holding a session, so a
	// filter the client composes is a suggestion rather than a restriction.
	Query string `yaml:"query"`

	// AllowedGroups, when set, hides this tool from anyone whose id_token does
	// not carry one of these in auth.groups_claim, and refuses its filter to
	// them if they ask for it by id anyway.
	//
	// This is what turns the rail from navigation into a boundary. Without it
	// every signed-in account may select every tool — which is the right
	// default when the tools are just shortcuts, and the wrong one when a tool
	// is the only thing standing between an operator and another team's logs.
	//
	// Requires auth.enabled; with authentication off there are no groups to
	// match and a tool carrying this is refused at startup rather than silently
	// admitting everyone.
	AllowedGroups []string `yaml:"allowed_groups"`
}

type Preset struct {
	Name  string `yaml:"name"`
	Query string `yaml:"query"`
}

type VictoriaLogs struct {
	// URL is the VictoriaLogs base URL, without the /select path.
	URL string `yaml:"url"`

	// Timeout bounds a single upstream request. It must be generous: a wide
	// range over a lot of data is a slow query, not a broken one.
	Timeout time.Duration `yaml:"timeout"`

	// BasicAuth is for a VictoriaLogs behind vmauth or a reverse proxy.
	BasicAuth BasicAuth `yaml:"basic_auth"`

	// Tenant is sent as the AccountID / ProjectID headers on every upstream
	// request. Fixed for the whole process: which tenant this UI reads is a
	// property of the deployment, not of the user looking at it.
	Tenant Tenant `yaml:"tenant"`

	// MaxRows caps every result set regardless of what the UI asks for. The
	// browser has to render these rows, and the process has to hold the ones
	// it has not flushed yet — neither should be at the mercy of a typo in the
	// limit box.
	MaxRows int `yaml:"max_rows"`

	// DefaultLimit and DefaultRange are what the UI starts with.
	DefaultLimit int           `yaml:"default_limit"`
	DefaultRange time.Duration `yaml:"default_range"`

	// TailMaxDuration bounds a live-tail connection. A forgotten browser tab
	// otherwise holds an upstream stream open forever; the UI reconnects, so
	// the user never sees the cut.
	TailMaxDuration time.Duration `yaml:"tail_max_duration"`
}

type BasicAuth struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Tenant addresses one VictoriaLogs tenant. Zero/zero is the default tenant,
// which is also what a single-tenant install uses.
type Tenant struct {
	AccountID int `yaml:"account_id"`
	ProjectID int `yaml:"project_id"`
}

type Metrics struct {
	// Listen is the exporter's own socket — never a route on the app above,
	// which sits behind OIDC and may sit under base_path. A scraper should not
	// have to care about either. Empty disables the exporter entirely.
	Listen string `yaml:"listen"`
	Path   string `yaml:"path"`

	// ProbeInterval is how often VictoriaLogs is pinged to keep vlui_vl_up
	// fresh. It exists because the gauge has to mean something on an idle
	// instance: updated only by user queries, it would report the state of
	// whenever somebody last looked.
	//
	// Probing on scrape instead would be worse — a hung VictoriaLogs would hang
	// the scrape and take every other metric down with it, exactly when they
	// are needed. Zero disables the probe and the gauge; alert on
	// rate(vlui_vl_requests_total{status="error"}[5m]) instead.
	ProbeInterval time.Duration `yaml:"probe_interval"`
}

func Default() Config {
	return Config{
		Listen: "127.0.0.1:8080",
		VictoriaLogs: VictoriaLogs{
			URL:             "http://127.0.0.1:9428",
			Timeout:         60 * time.Second,
			MaxRows:         5000,
			DefaultLimit:    500,
			DefaultRange:    time.Hour,
			TailMaxDuration: time.Hour,
		},
		Metrics: Metrics{
			Listen:        "127.0.0.1:9108",
			Path:          "/metrics",
			ProbeInterval: 15 * time.Second,
		},
	}
}

// Load reads the file over the defaults. An empty path yields the defaults
// alone, which is what the CI smoke test and `-config /dev/null` rely on.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}

	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	// KnownFields: a misspelled key is a silent no-op otherwise, and the
	// operator finds out when the setting they thought they changed did
	// nothing. An empty file decodes to io.EOF, which is not an error here.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && err.Error() != "EOF" {
		return cfg, fmt.Errorf("parse config: %w", err)
	}

	cfg.normalize()
	return cfg, cfg.validate()
}

func (c *Config) normalize() {
	c.BasePath = normalizeBase(c.BasePath)
	c.VictoriaLogs.URL = strings.TrimRight(c.VictoriaLogs.URL, "/")

	if c.Metrics.Path == "" {
		c.Metrics.Path = "/metrics"
	}
	if !strings.HasPrefix(c.Metrics.Path, "/") {
		c.Metrics.Path = "/" + c.Metrics.Path
	}
}

// normalizeBase turns "stats", "/stats/" and "/stats" all into "/stats", and
// "" or "/" into "". Every consumer can then concatenate without thinking.
func normalizeBase(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return "/" + p
}

func (c *Config) validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen must be set")
	}

	if c.VictoriaLogs.URL == "" {
		return fmt.Errorf("victorialogs.url must be set")
	}
	u, err := url.Parse(c.VictoriaLogs.URL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("victorialogs.url %q is not an absolute http(s) URL", c.VictoriaLogs.URL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("victorialogs.url scheme %q: want http or https", u.Scheme)
	}

	if c.VictoriaLogs.MaxRows <= 0 {
		return fmt.Errorf("victorialogs.max_rows must be positive")
	}
	if c.VictoriaLogs.DefaultLimit <= 0 {
		return fmt.Errorf("victorialogs.default_limit must be positive")
	}
	if c.VictoriaLogs.DefaultLimit > c.VictoriaLogs.MaxRows {
		return fmt.Errorf("victorialogs.default_limit (%d) exceeds max_rows (%d)",
			c.VictoriaLogs.DefaultLimit, c.VictoriaLogs.MaxRows)
	}
	if c.VictoriaLogs.Timeout <= 0 {
		return fmt.Errorf("victorialogs.timeout must be positive")
	}

	for i, q := range c.Queries {
		if q.Name == "" || q.Query == "" {
			return fmt.Errorf("queries[%d]: both name and query must be set", i)
		}
	}

	if err := c.validateTools(); err != nil {
		return err
	}

	return nil
}

func (c *Config) validateTools() error {
	seen := make(map[string]int, len(c.Tools))

	for i := range c.Tools {
		t := &c.Tools[i]

		t.Tooltip = strings.TrimSpace(t.Tooltip)
		t.Icon = strings.TrimSpace(t.Icon)
		t.Query = strings.TrimSpace(t.Query)

		if t.Tooltip == "" {
			return fmt.Errorf("tools[%d]: tooltip must be set — it is the only label an icon has", i)
		}
		if t.Icon == "" {
			return fmt.Errorf("tools[%d] (%s): icon must be set; available: %s",
				i, t.Tooltip, strings.Join(Icons, ", "))
		}
		if !slices.Contains(Icons, t.Icon) {
			return fmt.Errorf("tools[%d] (%s): unknown icon %q; available: %s",
				i, t.Tooltip, t.Icon, strings.Join(Icons, ", "))
		}
		// The query is prepended, so a pipe in it would swallow everything the
		// operator types into a stage they cannot see.
		if strings.Contains(t.Query, "|") {
			return fmt.Errorf("tools[%d] (%s): query may not contain a pipe — it is prepended to what the operator types, so `%s` would apply to their filter too",
				i, t.Tooltip, t.Query)
		}

		if len(t.AllowedGroups) > 0 && !c.Auth.Enabled {
			return fmt.Errorf("tools[%d] (%s): allowed_groups needs auth.enabled — with authentication off there are no groups to check and the restriction would admit everyone",
				i, t.Tooltip)
		}
		if len(t.AllowedGroups) > 0 && t.Query == "" {
			return fmt.Errorf("tools[%d] (%s): allowed_groups on a tool with no query restricts nothing — the tool selects every log either way",
				i, t.Tooltip)
		}

		t.ID = slug(t.Tooltip)
		if t.ID == "" {
			return fmt.Errorf("tools[%d]: tooltip %q has no letters or digits to make an id from", i, t.Tooltip)
		}
		if first, dup := seen[t.ID]; dup {
			return fmt.Errorf("tools[%d] (%s) and tools[%d]: both reduce to the id %q; give them distinguishable tooltips",
				i, t.Tooltip, first, t.ID)
		}
		seen[t.ID] = i
	}

	return nil
}

// slug turns a tooltip into the id the URL carries: "Yeti Logs" -> "yeti-logs".
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		default:
			// Runs of punctuation collapse to one dash, and leading ones are
			// dropped entirely.
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
