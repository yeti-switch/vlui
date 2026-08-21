# Developing

## Prerequisites

Go (the version in `go.mod`) and Node 22. Helm is needed only to touch the
chart.

## Running it

Two processes, in two terminals:

```sh
make dev        # the Go API on :8080, against ./config.yml
make dev-web    # Vite on :5173 with HMR, proxying /api to the Go process
```

Open **:5173** — that is the one with hot reload. :8080 serves whatever was last
built into `web/dist`, which is what production looks like.

`config.yml` is gitignored; copy `config.example.yml` and point
`victorialogs.url` at something real. A VictoriaLogs to develop against is one
container:

```sh
docker run --rm -p 9428:9428 docker.io/victoriametrics/victoria-logs:latest
```

It starts empty, and an empty result set hides most bugs. Feed it something:

```sh
echo '{"_time":"0","_msg":"call 1000 completed","level":"info","host":"sems-1"}
{"_time":"0","_msg":"upstream timeout after 5s","level":"error","host":"sems-2"}' \
  | curl -X POST -H 'Content-Type: application/stream+json' --data-binary @- \
    'http://localhost:9428/insert/jsonline?_stream_fields=host'
```

`_time: "0"` means "stamp it now", so the lines land inside the UI's default
one-hour window instead of at a fixed date you would then have to go looking
for. `_stream_fields=host` gives them a real `_stream`, which is what the field
sidebar and `_stream:{…}` filters work on.

## Building

```sh
make build      # SPA, then the binary with the SPA embedded — as it ships
make build-go   # only the Go side, reusing whatever is in web/dist
make static     # CGO_ENABLED=0, the binary that goes in the .deb and the image
```

The SPA is embedded with `go:embed`, so `web/dist` must exist before the Go
build. `web/dist/.gitkeep` is committed for exactly that reason: its contents
are gitignored, and `go:embed all:dist` fails outright on a missing directory —
without the placeholder a fresh clone would not compile. Vite empties the
directory on every build, so `make web` puts the placeholder back afterwards.

A binary built without the SPA still runs; it serves
`SPA not built: run 'make web'` instead of the UI.

## Checks

```sh
make check         # gofmt, go vet, go test, and the SPA type-check + build
make chart-check   # helm lint, and the chart's rendered config through the binary
go test -race ./...
```

`make chart-check` is the one worth understanding. `config.Load` runs with
`KnownFields`, so a key the chart writes that the binary does not know is a
`CrashLoopBackOff` rather than a warning. The target renders the chart's
ConfigMap and feeds it to `vlui -check-config`, which parses and validates
without touching the network. CI does the same across several value
combinations.

It checks the auth block too — a `cookie_secret` too short to start is caught
here rather than at the next restart — but it contacts nothing: no OIDC
discovery, no VictoriaLogs. It answers "is this file right", not "is the world
up".

`-check-config` is useful on a server too, before restarting into a config you
just edited:

```sh
/opt/vlui/bin/vlui -config /opt/vlui/etc/config.yml -check-config
```

## Alert rules

`deploy/alerts/vlui.yml` is canonical, in the same vmalert format as the other
yeti services' rules. `charts/vlui/alerts/vlui.yml` is a copy the Helm chart
renders into a PrometheusRule — Helm cannot read files outside a chart, so after
editing the canonical one:

```sh
cp deploy/alerts/vlui.yml charts/vlui/alerts/vlui.yml
promtool check rules deploy/alerts/vlui.yml
cd deploy/alerts && promtool test rules vlui_test.yml
```

`go test ./internal/` fails if the copies differ, if a rule names a metric the
exporter does not have, or if an upstream-error alert matches `status!="ok"` —
that one includes the 400 VictoriaLogs returns for a mistyped query, so it would
page somebody for a typo.

## Layout

```
cmd/vlui/          flags, wiring, the root router, graceful shutdown
deploy/alerts/     vmalert rules, and their promtool tests
internal/config/   the single YAML file; defaults, normalisation, validation
internal/vl/       VictoriaLogs client — streaming query/tail, hits, facets, fields
internal/api/      /api/* — parameter clamping, tool filters, error mapping
internal/auth/     OIDC login and the signed session cookie
internal/metrics/  the Prometheus exporter and its background probe
internal/webui/    serving the embedded SPA: history fallback, base path, caching
web/               Vue 3 + TypeScript + Vite; web/embed.go embeds web/dist
charts/vlui/       the Helm chart
packaging/         systemd unit, maintainer scripts, the container's config
```

Two conventions worth keeping:

- **The frontend and the API that feeds it are one artifact.** The SPA is
  embedded rather than served from an nginx root, so they cannot drift out of
  version with each other.
- **Icons are declared twice on purpose** — the SVG in `web/src/icons.ts`, the
  name in `internal/config/icons.go`, which validates what the YAML may say.
  `TestIconsMatchTheFrontend` parses the TypeScript and fails if the two lists
  disagree, so a half-finished addition cannot ship.

## Testing notes

Go tests run against a fake VictoriaLogs (`httptest`), never a real one, and
assert on the *form* of the upstream request as much as on the response —
whether the row cap was clamped, whether a tool's filter arrived as
`extra_filters`. The frontend has no unit tests; `vue-tsc --noEmit` runs in CI
as part of `npm run build`.

## Releasing

Publish a GitHub release (a tag alone does nothing). `.github/workflows/release.yml`
then builds three artifacts, all versioned by the tag:

| artifact | where it lands |
| --- | --- |
| `.deb` | attached to the release, and signed into the yeti APT repo |
| container image | `ghcr.io/yeti-switch/vlui:<tag>` (amd64 + arm64) |
| Helm chart | `oci://ghcr.io/yeti-switch/charts/vlui:<tag>` |

The chart's `version` and `appVersion` are both set to the tag at package time,
so chart 1.4.0 runs vlui 1.4.0 with no values needed. `Chart.yaml` carries
`0.0.0` placeholders so local `helm lint` has valid SemVer to work with — leave
them alone.

The chart job waits on the image job: a chart whose default pull does not exist
is worse than a late chart.
