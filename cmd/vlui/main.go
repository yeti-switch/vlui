// Command vlui is an alternative web UI for VictoriaLogs: a Go server with the
// Vue SPA embedded in the binary, configured by one YAML file and backed by no
// database of its own.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/yeti-switch/vlui/internal/api"
	"github.com/yeti-switch/vlui/internal/auth"
	"github.com/yeti-switch/vlui/internal/config"
	"github.com/yeti-switch/vlui/internal/metrics"
	"github.com/yeti-switch/vlui/internal/vl"
	"github.com/yeti-switch/vlui/internal/webui"
	"github.com/yeti-switch/vlui/web"
)

// Set by the linker at release time; "dev" in a local build.
var (
	version = "dev"
	commit  = ""
)

func main() {
	if err := run(); err != nil {
		// Not slog: a startup failure has to be readable in `systemctl status`,
		// which shows the last few stderr lines and no structure.
		fmt.Fprintln(os.Stderr, "vlui:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", "", "path to the YAML configuration file")
		showVersion = flag.Bool("version", false, "print the version and exit")
		checkConfig = flag.Bool("check-config", false, "load and validate the configuration, then exit")
		debug       = flag.Bool("debug", false, "log at debug level")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(versionString())
		return nil
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	// Parse and validate only. Useful before a restart — and the only way the
	// Helm chart can prove that the config it renders is one this binary
	// accepts, since config.Load rejects unknown keys and a chart that invented
	// one would be a CrashLoopBackOff rather than a warning.
	//
	// It deliberately does NOT reach the network: no OIDC discovery, no
	// VictoriaLogs. It answers "is this file right", not "is the world up".
	if *checkConfig {
		// config.Load does not look inside the auth block — the rules live with
		// the OIDC code that uses them. Without this, a cookie_secret too short
		// to start would still be reported OK, which is worse than no check.
		if cfg.Auth.Enabled {
			if err := cfg.Auth.Validate(); err != nil {
				return err
			}
		}
		// Likewise the favicon: it is a path to a file this process must be able
		// to read and recognise, and "config OK" for one that does not exist
		// would be a check that misses the only thing about it that can fail.
		if _, err := webui.LoadFavicon(cfg.UI.Favicon, cfg.BasePath); err != nil {
			return err
		}
		fmt.Printf("config OK: %s\n", describe(cfg))
		return nil
	}

	// Signals first, so a Ctrl-C during OIDC discovery is honoured rather than
	// waiting out the IdP's timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Nil when the exporter is off, which every method on *Metrics is written to
	// tolerate — so "no metrics" costs no branches at the call sites and no
	// registry in memory, rather than a registry nothing can read.
	var m *metrics.Metrics
	if cfg.Metrics.Listen != "" {
		m = metrics.New(version, commit)
	}

	client := vl.New(vl.Options{
		URL:       cfg.VictoriaLogs.URL,
		Timeout:   cfg.VictoriaLogs.Timeout,
		Username:  cfg.VictoriaLogs.BasicAuth.Username,
		Password:  cfg.VictoriaLogs.BasicAuth.Password,
		AccountID: cfg.VictoriaLogs.Tenant.AccountID,
		ProjectID: cfg.VictoriaLogs.Tenant.ProjectID,
	}, m.ObserveUpstream)

	// Nil when disabled, which every consumer reads as "everyone is anonymous".
	var a *auth.Auth
	if cfg.Auth.Enabled {
		// Talks to the IdP, so it can fail when the provider is unreachable.
		// That is deliberate: starting with authentication silently broken
		// would serve every log line to anyone who asked.
		a, err = auth.New(ctx, cfg.Auth, cfg.BasePath, log)
		if err != nil {
			return err
		}
	} else {
		log.Warn("authentication is disabled: every request is anonymous")
	}

	// Read at startup, so a missing or unusable file is a refusal to start
	// rather than a tab that quietly shows no icon.
	favicon, err := webui.LoadFavicon(cfg.UI.Favicon, cfg.BasePath)
	if err != nil {
		return err
	}

	apiSrv := api.New(cfg, client, a, m, log, version, commit)

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: router(cfg, apiSrv, favicon, log),
		// Header timeout only. A read timeout would cap how long a query may
		// take, and a write timeout would cut live tailing off mid-stream —
		// both are bounded by their own contexts instead.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	if m != nil {
		go func() {
			if err := m.Serve(ctx, cfg.Metrics.Listen, cfg.Metrics.Path, log); err != nil {
				// The exporter failing must not take the application down:
				// losing metrics is an observability outage, not a service one.
				log.Error("metrics listener stopped", "err", err)
			}
		}()
		// Only alongside the exporter: the probe exists to keep vlui_vl_up
		// fresh, and with nothing serving the gauge it would be a request to
		// VictoriaLogs every interval that nobody could ever read.
		go m.Probe(ctx, cfg.Metrics.ProbeInterval, client.Ping, log)
	} else {
		log.Info("metrics exporter disabled (metrics.listen is not set)")
	}

	go func() {
		<-ctx.Done()
		log.Info("shutting down")
		// Long enough for an in-flight query to finish; short enough that a
		// restart is not held up by a forgotten live-tail tab.
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Info("vlui listening",
		"addr", cfg.Listen,
		"base_path", cfg.BasePath,
		"victorialogs", cfg.VictoriaLogs.URL,
		"auth", cfg.Auth.Enabled,
		"version", versionString(),
	)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// router mounts the whole application under base_path: the API, the health
// endpoint and the SPA. The metrics listener is deliberately not here.
func router(cfg config.Config, apiSrv *api.Server, favicon *webui.Favicon, log *slog.Logger) http.Handler {
	app := chi.NewRouter()
	app.Use(middleware.Recoverer)
	app.Use(securityHeaders)

	// Unauthenticated by design: a health check that requires a login tells you
	// about the IdP, not about this process.
	app.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	app.Mount("/api", apiSrv.Routes())

	// The SPA shell is served to anyone: it is only JavaScript, and it asks
	// /api/auth/me who it is talking to before it shows anything.
	app.Handle("/*", webui.Handler(web.Dist(), webui.Options{
		Base:    cfg.BasePath,
		Title:   cfg.UI.Title,
		Favicon: favicon,
	}))

	if cfg.BasePath == "" {
		return app
	}

	root := chi.NewRouter()
	root.Mount(cfg.BasePath, http.StripPrefix(cfg.BasePath, app))
	return root
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Everything the page needs is served from this origin — the SPA is
		// embedded and there is no CDN, no font host and no analytics. Vue
		// attaches component styles at runtime, hence 'unsafe-inline' for
		// styles only; script-src stays strict.
		h.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
				"connect-src 'self'; frame-ancestors 'none'; base-uri 'self'")
		next.ServeHTTP(w, r)
	})
}

// describe is the one-line summary -check-config prints: enough to see which
// file was actually read, without echoing any of its secrets.
func describe(cfg config.Config) string {
	auth := "auth off"
	if cfg.Auth.Enabled {
		auth = "auth on"
	}
	metrics := cfg.Metrics.Listen
	if metrics == "" {
		metrics = "off"
	}
	return fmt.Sprintf("listen %s, base_path %q, victorialogs %s, %s, metrics %s, %d tool(s)",
		cfg.Listen, cfg.BasePath, cfg.VictoriaLogs.URL, auth, metrics, len(cfg.Tools))
}

func versionString() string {
	if commit == "" {
		return version
	}
	return version + " (" + commit + ")"
}
