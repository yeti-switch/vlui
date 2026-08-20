# vlui

An alternative web UI for [VictoriaLogs](https://docs.victoriametrics.com/victorialogs/):
a LogsQL query line on top, the matching logs in a table below.

One Go binary with the Vue SPA embedded in it, one YAML file, and no database.

```
┌────┬─────────────────────────────────────────────────────────────────────┐
│ ▤  │ query line       [🕒 last 1h] [500] [Run] [▶ Live]      [Presets ▾] │
│    │ 400 rows · 0.19s                                        [Download] │
│    ├─────────────────────────────────────────────────────────────────────┤
│    │ ▁▂▅█▆▃▁▁▂▄▆█▅▂▁  hits histogram — drag to zoom into a window        │
│    ├───────┬─────────────────────────────────────┬───────────────────────┤
│    │Fields │ _time           _msg                │ selected entry        │
│    │ level │ 10:02:11.417    upstream timeout …  │  _time  …             │
│ TZ │  error│ 10:02:11.402    call failed …       │  _msg   …             │
│ ☾  │  info │ 10:02:10.998    …                   │  host   …             │
│ DS │       │                                     │                       │
│ver │       │                                     │                       │
└────┴───────┴─────────────────────────────────────┴───────────────────────┘
```

## What it does

- **Query line** — LogsQL, with autocomplete on field names and values.
- **Results table** — virtualised. Click a row to see every field; filter,
  exclude, or promote one to a column.
- **Hits histogram** — matches over the window. Drag to zoom.
- **Field sidebar** — the most frequent values per field, one click to filter.
- **Live tailing** — follow new logs as they arrive.
- **Timestamps** in the timezone you pick.
- **Tools** — configurable icons in the left rail, each scoping the session to a
  slice of the logs. Applied server-side.
- **Shareable state** — query, window, row cap and columns live in the URL.
- **OIDC login** — any conformant provider.
- **Prometheus exporter** — built in, optional.

## Installing

```sh
apt install vlui                                    # Debian
docker run ghcr.io/yeti-switch/vlui:latest          # container
helm install vlui oci://ghcr.io/yeti-switch/charts/vlui   # Kubernetes
```

Each needs a config file and a little setup — nginx or a Gateway in front, and
`victorialogs.url` pointing at something. See **[INSTALL.md](INSTALL.md)**.

## Configuring

Everything lives in one file; `config.example.yml` documents every key. The
minimum is where VictoriaLogs is:

```yaml
victorialogs:
  url: http://127.0.0.1:9428
```

`listen` defaults to `127.0.0.1:8080`; everything else is off until you ask for
it. Authentication needs an OIDC client:

```yaml
auth:
  enabled: true
  issuer: https://idp.example.com/realms/ops
  client_id: vlui
  client_secret: "…"
  redirect_url: https://logs.example.com/api/auth/callback
  cookie_secret: "…"        # openssl rand -hex 32; empty generates one per process
  allowed_groups: [noc]
  # groups_claim: groups    # dotted path; see config.example.yml for Zitadel and Keycloak
```

The session is a signed cookie, which is what lets this application have no
database. Rotating `cookie_secret` revokes every session; leaving it empty
generates one at startup, so restarts and extra replicas sign people out.

**One instance reads one tenant.** `victorialogs.tenant` is fixed for the
process. To publish two, run two instances.

## Tools

```yaml
tools:
  - tooltip: main
    icon: gear

  - tooltip: Yeti Logs
    icon: yeti
    query: 'named_tags.system: yeti'
```

Each entry is an icon in the rail, labelled on hover. Selecting one scopes
everything — rows, histogram, facets, autocomplete, live tail — to its query,
shown as a static prefix beside the input. Each tool keeps its own query, and a
tool with a filter accepts an empty box.

Icons: `gear`, `yeti`, `bolt`, `bug`, `chart`, `cloud`, `database`, `globe`,
`lock`, `phone`, `server`, `tag`, `terminal`.

The filter is applied by the **server**, from the tool's id, as VictoriaLogs
`extra_filters` — never composed in the browser, and a request naming no tool
gets the first tool's filter. To make the rail a boundary rather than a set of
shortcuts, gate the wide tools with `allowed_groups` (needs `auth.enabled`):

```yaml
  - tooltip: Billing
    icon: lock
    query: 'system: billing'
    allowed_groups: [billing, admin]
```

Such a tool is refused with 403 and left out of `/api/config`. None of it
restricts anyone who can reach VictoriaLogs directly.

## Metrics

A Prometheus exporter is built in, on its own listener — optional, and off
until `metrics.listen` names an address. See **[METRICS.md](METRICS.md)** for
what is exported, why `vl_up` is probed in the background, and alerting rules.

## Developing

```sh
make dev        # the Go API on :8080, against ./config.yml
make dev-web    # Vite on :5173 with HMR, proxying /api to it
make check      # gofmt, go vet, go test, and the SPA type-check + build
```

See **[DEVELOPING.md](DEVELOPING.md)** for the layout, a VictoriaLogs to develop
against, the chart checks and how a release is cut.
