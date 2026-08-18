// Package metrics is the built-in Prometheus exporter.
//
// It is served on its own listener, never as a route on the application: the
// app's routes sit behind OIDC and may sit under base_path, and a scraper
// should have to care about neither.
//
// The questions it exists to answer without anyone reading the journal:
//
//   - Is VictoriaLogs reachable, right now, even though nobody has run a query
//     since yesterday? (vl_up, kept fresh by the background probe.)
//   - Is it rejecting our requests, and with which status? (vl_requests_total.)
//   - Is a query pinned open by a browser tab somebody forgot?
//     (queries_active, tail_sessions_active.)
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "vlui"

// Metrics is the whole exporter. A nil *Metrics is safe to call on every
// method, so the metrics-disabled path and the tests need no branches at the
// call sites.
type Metrics struct {
	reg *prometheus.Registry

	httpRequests *prometheus.CounterVec   // route, status
	httpDuration *prometheus.HistogramVec // route

	vlRequests *prometheus.CounterVec   // endpoint, status
	vlDuration *prometheus.HistogramVec // endpoint
	vlUp       prometheus.Gauge

	queryRows    prometheus.Counter
	queryBytes   prometheus.Counter
	queriesLive  prometheus.Gauge
	tailSessions prometheus.Gauge
}

func New(version, commit string) *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		reg: reg,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "http_requests_total",
			Help: "Requests served by the application, by route and response status.",
		}, []string{"route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "http_request_duration_seconds",
			Help: "Time to serve an application request.",
			// Wide: a facet lookup is milliseconds and a wide query is minutes,
			// and both are normal.
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 300},
		}, []string{"route"}),

		vlRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "vl_requests_total",
			Help: "Requests sent to VictoriaLogs, by endpoint and outcome (ok, error, or an HTTP status).",
		}, []string{"endpoint", "status"}),
		vlDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "vl_request_duration_seconds",
			Help: "Time from sending a VictoriaLogs request to its response headers. " +
				"For the streaming endpoints this is time-to-first-byte, not the life of the stream.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"endpoint"}),
		vlUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "vl_up",
			Help: "1 if the last VictoriaLogs health probe succeeded. Absent when probing is disabled.",
		}),

		queryRows: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "query_rows_total",
			Help: "Log lines forwarded to browsers.",
		}),
		queryBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "query_bytes_total",
			Help: "Bytes of log data forwarded to browsers.",
		}),
		queriesLive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "queries_active",
			Help: "Queries in flight right now.",
		}),
		tailSessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "tail_sessions_active",
			Help: "Live-tail streams open right now.",
		}),
	}

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "build_info",
		Help: "Always 1; the version and commit are the labels.",
	}, []string{"version", "commit"})
	buildInfo.WithLabelValues(version, commit).Set(1)

	reg.MustRegister(
		m.httpRequests, m.httpDuration,
		m.vlRequests, m.vlDuration,
		m.queryRows, m.queryBytes, m.queriesLive, m.tailSessions,
		buildInfo,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return m
}

// ObserveHTTP records one served request. route is the chi pattern, not the
// path: /api/field_values with a thousand distinct fields must not become a
// thousand time series.
func (m *Metrics) ObserveHTTP(route, status string, d time.Duration) {
	if m == nil {
		return
	}
	m.httpRequests.WithLabelValues(route, status).Inc()
	m.httpDuration.WithLabelValues(route).Observe(d.Seconds())
}

// ObserveUpstream is the vl.Observer.
func (m *Metrics) ObserveUpstream(endpoint, status string, d time.Duration) {
	if m == nil {
		return
	}
	m.vlRequests.WithLabelValues(endpoint, status).Inc()
	m.vlDuration.WithLabelValues(endpoint).Observe(d.Seconds())
}

// The nil checks are in the methods themselves, not in a shared helper: a
// helper would still have to evaluate m.queryRows at the call site, which is
// the dereference that a nil *Metrics cannot survive.
func (m *Metrics) AddRows(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.queryRows.Add(float64(n))
}

func (m *Metrics) AddBytes(n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.queryBytes.Add(float64(n))
}

// QueryStarted and TailStarted return the function that ends the accounting, so
// a caller cannot increment the gauge and forget to decrement it — the only way
// to use them is `defer m.QueryStarted()()`.
func (m *Metrics) QueryStarted() func() {
	if m == nil {
		return func() {}
	}
	return gauge(m.queriesLive)
}

func (m *Metrics) TailStarted() func() {
	if m == nil {
		return func() {}
	}
	return gauge(m.tailSessions)
}

func gauge(g prometheus.Gauge) func() {
	g.Inc()
	return g.Dec
}

// Probe keeps vl_up meaningful on an idle instance. It runs until the context
// ends; interval zero means the deployment would rather alert on the error rate
// and the gauge is left out of the registry's output entirely.
func (m *Metrics) Probe(ctx context.Context, interval time.Duration, ping func(context.Context) error, log *slog.Logger) {
	if m == nil || interval <= 0 {
		return
	}

	// Registered here rather than in New, so that a deployment which disables
	// probing does not export a gauge frozen at whatever it was initialised to
	// — an absent series is honest, a permanent 0 is a lie that pages people.
	if err := m.reg.Register(m.vlUp); err != nil {
		var already prometheus.AlreadyRegisteredError
		if !errors.As(err, &already) {
			log.Error("cannot register vl_up", "err", err)
			return
		}
	}

	check := func() {
		err := ping(ctx)
		if err != nil {
			m.vlUp.Set(0)
			// Debug, not Warn: a VictoriaLogs restart would otherwise fill the
			// journal, and the gauge is what monitoring is for.
			log.Debug("victorialogs health probe failed", "err", err)
			return
		}
		m.vlUp.Set(1)
	}

	// Immediately, so the gauge is right from the first scrape rather than one
	// interval later.
	check()

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}

// Serve runs the exporter listener until the context ends. An empty address
// disables it.
func (m *Metrics) Serve(ctx context.Context, addr, path string, log *slog.Logger) error {
	if m == nil || addr == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle(path, promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
		// A broken collector must not take the scrape down with it.
		ErrorHandling: promhttp.ContinueOnError,
	}))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Info("metrics listening", "addr", addr, "path", path)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
