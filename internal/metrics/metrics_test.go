package metrics_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yeti-switch/vlui/internal/metrics"
)

func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()

	// Serve on a port the kernel picks, so tests can run concurrently.
	ln := httptest.NewServer(nil)
	addr := strings.TrimPrefix(ln.URL, "http://")
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = m.Serve(ctx, addr, "/metrics", slog.New(slog.DiscardHandler)) }()

	var body string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get("http://" + addr + "/metrics")
		if err == nil {
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			body = string(b)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if body == "" {
		t.Fatal("exporter never answered")
	}
	return body
}

// A gauge frozen at whatever it was initialised to is worse than no gauge: it
// reads as a healthy VictoriaLogs to anything alerting on it.
func TestVLUpIsAbsentUntilProbed(t *testing.T) {
	m := metrics.New("test", "abc")

	if got := scrape(t, m); strings.Contains(got, "vlui_vl_up") {
		t.Error("vl_up must not be exported before the probe has run")
	}
}

func TestProbeExportsVLUp(t *testing.T) {
	m := metrics.New("test", "abc")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The probe checks once immediately, so the gauge is right from the first
	// scrape rather than one interval later.
	go m.Probe(ctx, time.Hour, func(context.Context) error { return nil }, slog.New(slog.DiscardHandler))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(scrape(t, m), "vlui_vl_up 1") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("vl_up 1 never appeared after a successful probe")
}

func TestProbeReportsFailure(t *testing.T) {
	m := metrics.New("test", "abc")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go m.Probe(ctx, time.Hour, func(context.Context) error { return errors.New("down") }, slog.New(slog.DiscardHandler))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(scrape(t, m), "vlui_vl_up 0") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("vl_up 0 never appeared after a failed probe")
}

// Every one of these is called on the request path, where a metrics-disabled
// process passes nil. A panic here would be a 500 on a working query.
func TestNilMetricsIsSafe(t *testing.T) {
	var m *metrics.Metrics

	m.ObserveHTTP("/api/query", "200", time.Second)
	m.ObserveUpstream("/select/logsql/query", "ok", time.Second)
	m.AddRows(10)
	m.AddBytes(100)
	m.QueryStarted()()
	m.TailStarted()()
	m.Probe(context.Background(), time.Second, nil, slog.New(slog.DiscardHandler))

	if err := m.Serve(context.Background(), "127.0.0.1:0", "/metrics", slog.New(slog.DiscardHandler)); err != nil {
		t.Errorf("Serve on a nil *Metrics = %v, want nil", err)
	}
}

// Zero means "we would rather alert on the error rate"; it must not leave a
// goroutine spinning or a gauge behind.
func TestProbeIntervalZeroDisablesTheGauge(t *testing.T) {
	m := metrics.New("test", "abc")

	done := make(chan struct{})
	go func() {
		m.Probe(context.Background(), 0, func(context.Context) error { return nil }, slog.New(slog.DiscardHandler))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Probe with interval 0 must return immediately")
	}

	if strings.Contains(scrape(t, m), "vlui_vl_up") {
		t.Error("vl_up must not be exported when probing is disabled")
	}
}
