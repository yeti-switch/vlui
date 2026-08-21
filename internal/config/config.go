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
	"unicode"
	"unicode/utf8"

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

	// UI is what the browser shows before it shows any logs: the tab's title
	// and its icon. Deployment identity — which of three vlui instances this
	// tab is — rather than anything functional.
	UI UI `yaml:"ui"`

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
	// ID names the tool. Required, and unique within the list.
	//
	// It is what the URL carries and what every request sends, so it is the one
	// part of a tool that must not change casually: a link to what somebody is
	// looking at is a link to this string. Deriving it from the tooltip — as an
	// earlier version did — meant renaming a tool silently broke every link to
	// it, and two tools whose names differed only in punctuation collided.
	//
	// Letters, digits, dashes and underscores.
	ID string `yaml:"id"`

	// Tooltip is the label shown on hover. Required: an icon-only rail with an
	// unlabelled icon is a guessing game.
	Tooltip string `yaml:"tooltip"`

	// Icon names one of the shapes the UI ships. Unknown names are refused at
	// startup rather than rendering as a blank square.
	//
	// Exactly one of icon or letters.
	Icon string `yaml:"icon"`

	// Letters is a short label drawn in place of an icon — "API", "SIP", "DB".
	//
	// It exists because a rail of a dozen tools runs out of shapes that mean
	// anything: the fourth abstract glyph is one nobody can tell from the
	// fifth, while three letters of the system's own name need no legend. Up to
	// three characters, which is what fits at a readable size.
	Letters string `yaml:"letters"`

	// Query is optional. A tool without one — "everything", usually first in
	// the list — selects nothing and filters nothing.
	//
	// It is applied by the SERVER, as an extra_filters constraint, not by the
	// browser: the API is reachable with curl by anyone holding a session, so a
	// filter the client composes is a suggestion rather than a restriction.
	Query string `yaml:"query"`

	// Fields are the columns the results table opens with for this tool. They
	// vary per slice of the logs: a SIP tool wants call_id and host, a billing
	// one wants neither. Empty falls back to _time and _msg.
	//
	// A default, not a restriction — whatever the operator selects afterwards is
	// remembered in their browser and wins.
	//
	// Each entry is either a field name or a name with a label:
	//
	//   fields:
	//     - _time
	//     - _msg
	//     - {name: payload.method, label: method}
	//
	// The label is what the column header shows. It exists because a field name
	// is often far wider than its values — "payload.response.status_code" is
	// 28 characters of header over three of data, and the column is sized to
	// whichever is wider. The full name is still shown in the field panel and
	// the log entry, so nothing is hidden, only shortened where it costs the
	// most.
	Fields []Field `yaml:"fields"`

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

// Field is one column: the log field to show, and optionally what to call it in
// the table header.
type Field struct {
	Name  string `yaml:"name"`
	Label string `yaml:"label"`
}

// UnmarshalYAML accepts either shape:
//
//	fields: [_time, _msg]
//	fields: [{name: payload.method, label: method}]
//
// A bare string is by far the common case, and requiring `{name: _time}` for
// every column to allow a label on one of them would be a poor trade.
func (f *Field) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		f.Name = node.Value
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a field is either a name or {name: ..., label: ...}", node.Line)
	}

	// Checked by hand because Node.Decode does not honour the decoder's
	// KnownFields setting, and a typo — `lable:` — would otherwise be accepted
	// and silently do nothing, which is the failure this project refuses
	// everywhere else.
	for i := 0; i < len(node.Content); i += 2 {
		switch key := node.Content[i].Value; key {
		case "name", "label":
		default:
			return fmt.Errorf("line %d: field has no %q setting; want name or label", node.Content[i].Line, key)
		}
	}

	type plain Field // a distinct type, or Decode would call this method again
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*f = Field(p)
	return nil
}

type UI struct {
	// Title is the browser tab's title. Empty keeps the default.
	//
	// It is written into index.html when the server starts rather than set by
	// the SPA, so the tab is right from the first byte — before the JavaScript
	// runs, and on the login page, which is served to people who have no
	// session and therefore cannot read the config API.
	Title string `yaml:"title"`

	// Favicon is a path to an image on disk — svg, png, ico, jpeg, gif or webp.
	// Empty keeps the one that ships with the SPA.
	//
	// A local file rather than a URL: the page's Content-Security-Policy allows
	// images from this origin only, so an icon hosted elsewhere would be
	// blocked by the browser and the tab would silently keep the default. The
	// file is read once at startup and served from memory.
	Favicon string `yaml:"favicon"`
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
	// have to care about either.
	//
	// Empty, which is the DEFAULT, means no exporter: no second socket, no
	// registry, and no health probe of VictoriaLogs. Opting in by naming an
	// address is deliberate — a process should not open a port nobody asked
	// for, and a deployment with no Prometheus has nothing to scrape it.
	Listen string `yaml:"listen"`
	Path   string `yaml:"path"`

	// ProbeInterval is how often VictoriaLogs is pinged to keep vlui_vl_up
	// fresh. Read only when the exporter is on — there is nowhere for the gauge
	// to be read from otherwise, and probing for nobody is pure noise against
	// VictoriaLogs.
	//
	// It exists because the gauge has to mean something on an idle instance:
	// updated only by user queries, it would report the state of whenever
	// somebody last looked.
	//
	// Probing on scrape instead would be worse — a hung VictoriaLogs would hang
	// the scrape and take every other metric down with it, exactly when they
	// are needed. Zero disables the probe and the gauge; alert on
	// rate(vlui_vl_requests_total{status="error"}[5m]) instead.
	ProbeInterval time.Duration `yaml:"probe_interval"`
}

// maxToolLetters is what fits in the rail's 40px button at a size anyone can
// read. Four characters means either an unreadable font or a clipped label, and
// a clipped label is a worse legend than no label.
const maxToolLetters = 3

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
		// No listen address by default: the exporter is opt-in, so a config
		// that never mentions metrics opens no second socket. The other two
		// only matter once a listen address turns it on.
		Metrics: Metrics{
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

	c.UI.Title = strings.TrimSpace(c.UI.Title)
	c.UI.Favicon = strings.TrimSpace(c.UI.Favicon)

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

		t.ID = strings.TrimSpace(t.ID)
		if t.ID == "" {
			return fmt.Errorf("tools[%d] (%s): id must be set — it is what the URL carries and what each request sends", i, orUnnamed(t.Tooltip))
		}
		if bad := firstBadIDRune(t.ID); bad != 0 {
			return fmt.Errorf("tools[%d]: id %q contains %q; use letters, digits, dashes and underscores", i, t.ID, bad)
		}
		if first, dup := seen[t.ID]; dup {
			return fmt.Errorf("tools[%d] and tools[%d]: both use the id %q, which has to be unique — it is how a request names one tool rather than the other",
				i, first, t.ID)
		}
		seen[t.ID] = i

		// The tooltip defaults to the id: a tool called "api" needs no second
		// name to hover over, and an unlabelled icon would be a guessing game.
		if t.Tooltip == "" {
			t.Tooltip = t.ID
		}
		t.Letters = strings.TrimSpace(t.Letters)

		switch {
		case t.Icon == "" && t.Letters == "":
			return fmt.Errorf("tools[%d] (%s): set either icon or letters; icons available: %s",
				i, t.Tooltip, strings.Join(Icons, ", "))
		case t.Icon != "" && t.Letters != "":
			// Both would leave the UI picking one, and whichever it picked would
			// be the wrong one for somebody.
			return fmt.Errorf("tools[%d] (%s): icon %q and letters %q are both set; a tool has one or the other",
				i, t.Tooltip, t.Icon, t.Letters)
		case t.Icon != "" && !slices.Contains(Icons, t.Icon):
			return fmt.Errorf("tools[%d] (%s): unknown icon %q; available: %s",
				i, t.Tooltip, t.Icon, strings.Join(Icons, ", "))
		case t.Letters != "":
			// Counted in runes: "ЦОД" is three letters and six bytes, and
			// refusing it would be a bug rather than a limit.
			if n := utf8.RuneCountInString(t.Letters); n > maxToolLetters {
				return fmt.Errorf("tools[%d] (%s): letters %q is %d characters; up to %d fit in the rail",
					i, t.Tooltip, t.Letters, n, maxToolLetters)
			}
		}
		// The query is prepended, so a pipe in it would swallow everything the
		// operator types into a stage they cannot see.
		if strings.Contains(t.Query, "|") {
			return fmt.Errorf("tools[%d] (%s): query may not contain a pipe — it is prepended to what the operator types, so `%s` would apply to their filter too",
				i, t.Tooltip, t.Query)
		}

		for j := range t.Fields {
			f := &t.Fields[j]
			f.Name = strings.TrimSpace(f.Name)
			f.Label = strings.TrimSpace(f.Label)
			if f.Name == "" {
				return fmt.Errorf("tools[%d] (%s): fields[%d] has no name", i, t.Tooltip, j)
			}
		}

		if len(t.AllowedGroups) > 0 && !c.Auth.Enabled {
			return fmt.Errorf("tools[%d] (%s): allowed_groups needs auth.enabled — with authentication off there are no groups to check and the restriction would admit everyone",
				i, t.Tooltip)
		}
		if len(t.AllowedGroups) > 0 && t.Query == "" {
			return fmt.Errorf("tools[%d] (%s): allowed_groups on a tool with no query restricts nothing — the tool selects every log either way",
				i, t.Tooltip)
		}

	}

	return nil
}

// firstBadIDRune reports the first character an id may not contain, or zero.
//
// The id ends up in a URL and in a query parameter, so it stays to characters
// that need no encoding to read — with the exception of letters outside ASCII,
// which are percent-encoded by the browser and perfectly legible in a config.
func firstBadIDRune(id string) rune {
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return r
	}
	return 0
}

// orUnnamed keeps an error message readable when the tool has no tooltip either.
func orUnnamed(tooltip string) string {
	if tooltip == "" {
		return "unnamed"
	}
	return tooltip
}
