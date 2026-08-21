package internal_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The alerting rules exist twice: deploy/alerts/vlui.yml is the canonical file,
// the one a systemd deployment hands to vmalert, and charts/vlui/alerts/vlui.yml
// is a copy the Helm chart renders into a PrometheusRule.
//
// The duplication is forced: Helm's .Files.Get cannot read anything outside the
// chart directory, and a chart has to be self-contained to be packaged at all.
// This is what stops the two drifting — a rule fixed in one and forgotten in the
// other would mean Kubernetes and the hosts alerting on different things, which
// nobody would notice until the wrong one stayed quiet.
func TestAlertRulesMatchTheChart(t *testing.T) {
	canonical := read(t, filepath.Join("..", "deploy", "alerts", "vlui.yml"))
	chart := read(t, filepath.Join("..", "charts", "vlui", "alerts", "vlui.yml"))

	if canonical != chart {
		t.Error("deploy/alerts/vlui.yml and charts/vlui/alerts/vlui.yml differ — " +
			"copy the canonical one over: cp deploy/alerts/vlui.yml charts/vlui/alerts/vlui.yml")
	}
}

// Every metric an alert fires on has to be one vlui actually exports. A rule
// against a metric that was renamed is a rule that can never fire, and silence
// is indistinguishable from health.
func TestAlertRulesOnlyUseMetricsWeExport(t *testing.T) {
	rules := read(t, filepath.Join("..", "deploy", "alerts", "vlui.yml"))
	exporter := read(t, filepath.Join("internal", "metrics", "metrics.go"))
	if exporter == "" {
		exporter = read(t, filepath.Join("..", "internal", "metrics", "metrics.go"))
	}

	// vlui_foo_total, and the _bucket/_sum/_count suffixes a histogram adds.
	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`vlui_[a-z_]+`).FindAllString(rules, -1) {
		base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(m, "_bucket"), "_sum"), "_count")
		used[base] = true
	}
	if len(used) == 0 {
		t.Fatal("parsed no metric names out of the rules")
	}

	for name := range used {
		// The exporter declares them without the namespace prefix.
		short := strings.TrimPrefix(name, "vlui_")
		if !strings.Contains(exporter, `Name: "`+short+`"`) {
			t.Errorf("alert rules use %s, which internal/metrics does not export", name)
		}
	}
}

// A 4xx from VictoriaLogs is almost always a LogsQL syntax error — somebody
// mistyped a query. Alerting on it pages a human for a typo, which is the
// fastest way to teach a team to ignore an alert.
func TestUpstreamErrorAlertIgnoresClientErrors(t *testing.T) {
	rules := read(t, filepath.Join("..", "deploy", "alerts", "vlui.yml"))

	for _, bad := range []string{`status!="ok"`, `status!~"ok"`} {
		if strings.Contains(rules, bad) {
			t.Errorf("an alert matches %s, which includes the 400 VictoriaLogs returns for a bad query", bad)
		}
	}
	if !strings.Contains(rules, `status=~"error|5.."`) {
		t.Error("the upstream error alert should match only transport failures and 5xx")
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("%s: %v", path, err)
	}
	return string(b)
}
