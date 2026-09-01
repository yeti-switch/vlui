// Package config is the single YAML file the whole application is configured
// from. There is no database and no second source of truth: presets, auth and
// upstream all come from here, and everything that survives a restart is either
// in this file or in VictoriaLogs itself.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
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

	// ValueStyles are named ways of drawing a field's values, keyed by the name
	// a field's `style:` refers to.
	//
	// Defined here rather than on the field so one style serves every field
	// that means the same thing: an HTTP status is an HTTP status whether it
	// arrives as payload.status on one tool or response.code on another, and a
	// colour scheme copied per column is a colour scheme that drifts per
	// column.
	ValueStyles map[string]ValueStyle `yaml:"value_styles"`
}

/* ValueStyle is one named way of drawing values: how they are drawn, and which
 * value gets which colour.
 *
 * The two travel together because they are one decision. A style is written for
 * a KIND of field — "this is an HTTP status", "this is a duration in
 * milliseconds" — and what that kind of value should look like is settled at
 * the same moment as which of its values are alarming. Kept apart, every field
 * naming the style would have to repeat how to draw it, and the day one of them
 * disagreed would be a Tuesday nobody enjoyed. */
type ValueStyle struct {
	// Type is how the colour lands: StyleTag (the default) draws the value in a
	// small tinted pill, StyleText colours the value itself.
	//
	// A status is a CATEGORY — one of a handful of values, worth reading as a
	// badge. A duration is a MEASUREMENT that happens to be alarming past some
	// point; a column of pills around numbers reads as a category it is not,
	// and the digits stop lining up to the eye. Colour on its own says "look at
	// this one" without saying "this is a kind of thing".
	Type string `yaml:"type"`

	// Rules are tried IN ORDER and the first match wins, which is the only
	// precedence anybody has to remember.
	Rules []StyleRule `yaml:"rules"`
}

// The values ValueStyle.Type takes.
const (
	StyleTag  = "tag"
	StyleText = "text"
)

// StyleTypes are those values, for the error a typo produces.
var StyleTypes = []string{StyleTag, StyleText}

/* StyleRule is one line of a style's rules: what to match, and what colour the
 * value is drawn in when it matches.
 *
 * A rule carries exactly one matcher — two would be an intersection nobody
 * asked for, and the error says so.
 *
 * Modelled on how yeti-web has always drawn these: 2xx-3xx green, 4xx amber,
 * 5xx red, anything else plain. That mapping is a RANGE, which is why ranges
 * are here alongside plain values. */
type StyleRule struct {
	// Value matches the field's value exactly. Either one value or a list:
	//
	//   {value: "200", color: ok}
	//   {value: [error, fatal], color: error}
	Value StringList `yaml:"value"`

	// Range matches a NUMERIC value, inclusive, written "200-399". A value that
	// is not a number simply does not match — the field carrying the range is
	// the one place a log value's stringiness shows through.
	//
	// Either end may be left off: "1000-" is everything from a thousand up,
	// "-100" everything to a hundred. That is what a threshold looks like — "a
	// duration is bad past a second" has no upper bound, and inventing one
	// invites the day a value sails over it and loses its colour.
	Range string `yaml:"range"`

	// Prefix matches values that start with it. The cheap half of a regular
	// expression, which is deliberately not here: a pattern language in a
	// config file is a debugger nobody has.
	Prefix string `yaml:"prefix"`

	// Default matches anything the rules above did not. Only on the last rule
	// of a set, where it reads as what it is.
	Default bool `yaml:"default"`

	// Color is one of StyleColors. Names, not hex: the table is drawn in two
	// themes, and a colour that works in one is unreadable in the other.
	Color string `yaml:"color"`

	// Description says what a matching value MEANS, on the second line of the
	// cell's tooltip.
	//
	// A colour tells the reader that a value is worth their attention and
	// nothing more; the amber is a question ("what is 429?") that the deployment
	// already knows the answer to. Optional, because "500" needs no gloss to
	// anyone reading a log, and a tooltip repeating the obvious is noise.
	Description string `yaml:"description"`

	// bounds is Range parsed once, at startup.
	bounds RuleRange
}

// RuleRange is a parsed Range. An absent end is an open one, not a zero.
type RuleRange struct {
	Lo, Hi       float64
	HasLo, HasHi bool
}

// Bounds returns the parsed Range, and whether this rule has one at all.
func (r StyleRule) Bounds() (RuleRange, bool) {
	return r.bounds, r.Range != ""
}

// StyleColors are the colours a rule may name. Each is a token the stylesheet
// defines in both themes; anything else is refused at startup rather than
// rendering as an invisible tag in one of them.
var StyleColors = []string{"ok", "warn", "error", "info", "neutral", "muted"}

// StringList accepts either one string or a list of them, so a rule matching a
// single value does not have to be written as a list of one.
type StringList []string

func (l *StringList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*l = StringList{node.Value}
		return nil
	}
	var many []string
	if err := node.Decode(&many); err != nil {
		return fmt.Errorf("line %d: want a value or a list of values", node.Line)
	}
	*l = many
	return nil
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
	// It is the URL — the tool is a path segment under the mount point, so this
	// tool is at /<id> — and it is what every request sends. That makes it the
	// one part of a tool that must not change casually: a link to what somebody
	// is looking at is a link to this string. Deriving it from the tooltip — as
	// an earlier version did — meant renaming a tool silently broke every link
	// to it, and two tools whose names differed only in punctuation collided.
	//
	// Letters, digits, dashes and underscores, and not one of reservedIDs.
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

// Field is one column: the log field to show, optionally what to call it in the
// table header, and optionally how to colour its values.
type Field struct {
	Name  string `yaml:"name"`
	Label string `yaml:"label"`

	// Style names one of Config.ValueStyles, which then decides how this
	// column's values are drawn — a 500 that is red wherever it appears is one
	// less thing to read.
	//
	// Per field rather than per field NAME across the deployment: what a value
	// means is a property of the slice of logs it came from, and the tool is
	// what says which slice that is.
	Style string `yaml:"style"`
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
		case "name", "label", "style":
		default:
			return fmt.Errorf("line %d: field has no %q setting; want name, label or style", node.Content[i].Line, key)
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

	// Before the tools: a field's `style:` names one of these, and the check
	// that it names a real one belongs where the styles are already known to be
	// sound.
	if err := c.validateValueStyles(); err != nil {
		return err
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
		if reservedIDs[t.ID] {
			return fmt.Errorf("tools[%d]: id %q is a path this server already answers on, so /%s would never reach the UI; pick another id", i, t.ID, t.ID)
		}
		if first, dup := seen[t.ID]; dup {
			return fmt.Errorf("tools[%d] and tools[%d]: both use the id %q, which has to be unique — it is how a request names one tool rather than the other",
				i, first, t.ID)
		}
		seen[t.ID] = i

		// The tooltip defaults to the id: a tool called "billing" needs no second
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
			f.Style = strings.TrimSpace(f.Style)
			if f.Name == "" {
				return fmt.Errorf("tools[%d] (%s): fields[%d] has no name", i, t.Tooltip, j)
			}
			if f.Style != "" {
				if _, ok := c.ValueStyles[f.Style]; !ok {
					return fmt.Errorf("tools[%d] (%s): fields[%d] (%s): style names %q, which is not in value_styles (%s)",
						i, t.Tooltip, j, f.Name, f.Style, orNone(setNames(c.ValueStyles)))
				}
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

// reservedIDs are the ids a tool may not take, because the URL the UI would
// give it is already answered by something else.
//
// The tool is a path segment under the mount point — /http is the http tool —
// and the router reaches for its own routes first: /api is the API, /healthz is
// the health check, and /assets is where the built JavaScript lives. A tool
// named for one of those would be a link that silently returns JSON, or a
// script, instead of the UI. Refused at startup, where the operator is looking,
// rather than discovered by whoever clicks the icon.
var reservedIDs = map[string]bool{
	"api":     true,
	"healthz": true,
	"assets":  true,
}

/* validateValueStyles checks every rule of every style, fills in the default
 * type, and parses the ranges once so the hot path never has to.
 *
 * All of it is refused at startup rather than at render time: a colour the
 * stylesheet does not have, or a range written backwards, would otherwise
 * appear as an uncoloured value in a table three screens down, on the one row a
 * week where it matters. */
func (c *Config) validateValueStyles() error {
	for name, style := range c.ValueStyles {
		if strings.TrimSpace(name) == "" {
			return errors.New("value_styles: a style has no name")
		}

		style.Type = strings.TrimSpace(style.Type)
		if style.Type == "" {
			style.Type = StyleTag
		}
		if !slices.Contains(StyleTypes, style.Type) {
			return fmt.Errorf("value_styles[%s]: type is %q; want one of %s",
				name, style.Type, strings.Join(StyleTypes, ", "))
		}
		if len(style.Rules) == 0 {
			return fmt.Errorf("value_styles[%s]: no rules; a style that matches nothing is a style that does nothing", name)
		}
		// The map holds values, not pointers, so the filled-in type has to be
		// written back — the loop variable is a copy.
		c.ValueStyles[name] = style

		rules := style.Rules
		for i := range rules {
			r := &rules[i]
			r.Range = strings.TrimSpace(r.Range)
			r.Prefix = strings.TrimSpace(r.Prefix)
			r.Color = strings.TrimSpace(r.Color)
			r.Description = strings.TrimSpace(r.Description)

			// Exactly one matcher. Two on one rule would be an intersection
			// nobody wrote deliberately, and silently honouring the first is
			// how a config comes to mean something other than what it says.
			matchers := 0
			for _, set := range []bool{len(r.Value) > 0, r.Range != "", r.Prefix != "", r.Default} {
				if set {
					matchers++
				}
			}
			switch {
			case matchers == 0:
				return fmt.Errorf("value_styles[%s].rules[%d]: no matcher; want one of value, range, prefix or default", name, i)
			case matchers > 1:
				return fmt.Errorf("value_styles[%s].rules[%d]: more than one of value, range, prefix and default; a rule matches one way", name, i)
			}

			// A default that is not last can never be reached past itself, so
			// every rule after it is dead. Better said here than discovered.
			if r.Default && i != len(rules)-1 {
				return fmt.Errorf("value_styles[%s].rules[%d]: default has to be the last rule; the %d after it could never match",
					name, i, len(rules)-1-i)
			}

			if r.Range != "" {
				bounds, err := parseRange(r.Range)
				if err != nil {
					return fmt.Errorf("value_styles[%s].rules[%d]: range %q: %w", name, i, r.Range, err)
				}
				r.bounds = bounds
			}

			if r.Color == "" {
				return fmt.Errorf("value_styles[%s].rules[%d]: no color; want one of %s", name, i, strings.Join(StyleColors, ", "))
			}
			if !slices.Contains(StyleColors, r.Color) {
				return fmt.Errorf("value_styles[%s].rules[%d]: color %q is not one this UI can draw; want one of %s",
					name, i, r.Color, strings.Join(StyleColors, ", "))
			}
		}
	}
	return nil
}

/* parseRange reads "200-399" into its bounds, inclusive, with either end
 * allowed to be missing: "1000-" is a threshold upwards, "-100" one downwards.
 *
 * Deliberately unclever: numbers and a dash. Negative bounds would make the
 * dash ambiguous, and a log value that is negative is not one anybody colours
 * by band.
 */
func parseRange(s string) (RuleRange, error) {
	var out RuleRange

	before, after, found := strings.Cut(s, "-")
	if !found {
		return out, errors.New(`want a dash, as in "200-399", "1000-" or "-100"`)
	}
	before, after = strings.TrimSpace(before), strings.TrimSpace(after)
	if before == "" && after == "" {
		return out, errors.New("has no bounds at all; want a number on at least one side of the dash")
	}

	if before != "" {
		lo, err := strconv.ParseFloat(before, 64)
		if err != nil {
			return out, fmt.Errorf("%q is not a number", before)
		}
		out.Lo, out.HasLo = lo, true
	}
	if after != "" {
		hi, err := strconv.ParseFloat(after, 64)
		if err != nil {
			return out, fmt.Errorf("%q is not a number", after)
		}
		out.Hi, out.HasHi = hi, true
	}
	if out.HasLo && out.HasHi && out.Lo > out.Hi {
		return out, fmt.Errorf("%v is above %v; the low bound comes first", out.Lo, out.Hi)
	}
	return out, nil
}

// setNames lists the defined styles, sorted, for the error a mistyped `style:`
// produces — the answer to "then what IS there" belongs in the message.
func setNames(sets map[string]ValueStyle) []string {
	names := make([]string, 0, len(sets))
	for name := range sets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "none are defined"
	}
	return strings.Join(names, ", ")
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
