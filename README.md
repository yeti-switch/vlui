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

- **Query line** — LogsQL, with autocomplete on field names and their values,
  drawn from the same VictoriaLogs that answers the query. `Enter` runs,
  `Shift+Enter` is a newline.
- **Results table** — virtualised, so the row cap is about what is readable
  rather than about what the browser survives. Click a row to open every field
  beside it; from there, filter to a value, exclude it, or promote it to a
  column.
- **Hits histogram** — how many logs matched over the window, in buckets chosen
  to be round numbers. Drag across it to zoom into a range.
- **Field sidebar** — the most frequent values per field across the current
  selection, from `/select/logsql/facets`. One click adds the filter. It starts
  collapsed to a rail, and stays collapsed until you open it: an open panel is
  an extra facets query per run, so it is opt-in and the choice is remembered.
- **Live tailing** — follow new logs as they arrive, over Server-Sent Events.
- **Shareable state** — the query, the window, the row cap and the columns live
  in the URL fragment, so a link to what you are looking at is the address bar.
- **Tools** — configurable icons in the left rail, each scoping the whole
  session to a slice of the logs. The tool's filter shows beside the query
  input, and the **server** applies it on every request from the tool's id (see
  below) — the browser never composes it.
- **Left rail** — the tools, then timezone (display only: every request carries
  instants, so changing it re-renders rather than re-queries), light/dark/system
  theme, the build version, and the signed-in user with Sign out. Both
  preferences are remembered per browser.
- **OIDC login** — any conformant provider; optionally restricted to a group.
- **Prometheus exporter**, built in, on its own port.

## Installing

### Debian

```sh
apt install vlui
cp /opt/vlui/etc/config.example.yml /opt/vlui/etc/config.yml
$EDITOR /opt/vlui/etc/config.yml       # at least: victorialogs.url
systemctl enable --now vlui
```

The unit runs with `DynamicUser=yes` and reads the config through
`LoadCredential=`, so the file stays `0600 root:root` and no account is created.
Nothing is written to disk at runtime — there is nothing to back up.

Put nginx in front of it for TLS. Live tailing is a stream, so turn buffering
off for the location:

```nginx
location / {
  proxy_pass http://127.0.0.1:8080;
  proxy_set_header Host $host;
  proxy_set_header X-Forwarded-Proto https;
  proxy_buffering off;
  proxy_read_timeout 1h;   # a live tail is a long-lived response
}
```

### Container

```sh
docker run --rm -p 8080:8080 -p 9108:9108 \
  -v ./config.yml:/opt/vlui/etc/config.yml:ro \
  ghcr.io/yeti-switch/vlui:latest
```

The image is `gcr.io/distroless/static-debian13:nonroot`: no shell, no package
manager, no libc — about 2 MB of base under a static binary.

## Configuring

Everything lives in one file; `config.example.yml` documents every key. The
minimum is where VictoriaLogs is:

```yaml
listen: 127.0.0.1:8080

victorialogs:
  url: http://127.0.0.1:9428

metrics:
  listen: 127.0.0.1:9108
```

Turning on authentication needs an OIDC client and a cookie key:

```yaml
auth:
  enabled: true
  issuer: https://idp.example.com/realms/ops
  client_id: vlui
  client_secret: "…"
  redirect_url: https://logs.example.com/api/auth/callback
  cookie_secret: "…"        # openssl rand -hex 32
  allowed_groups: [noc]     # authorisation, not just authentication
```

The session is a signed cookie carrying the user, which is what lets this
application have no database. Rotating `cookie_secret` revokes every session.

**One instance reads one tenant.** `victorialogs.tenant` is fixed for the
process, because which tenant is on show is a property of the deployment rather
than of whoever is looking. To publish two, run two instances.

## Tools

```yaml
tools:
  - tooltip: main
    icon: gear

  - tooltip: Yeti Logs
    icon: yeti
    query: 'named_tags.system: yeti'

  - tooltip: API Logs
    icon: bolt
    query: 'system: api'
```

Each entry becomes an icon in the rail, labelled on hover. Selecting one scopes
everything — rows, histogram, facets, autocomplete, live tail — to its query,
which is shown as a static prefix beside the input so what is in force is always
on screen. A tool with no query, first in the list, is the usual "everything"
entry.

With a tool selected you can leave the query box **empty** — the tool's filter
is the query, and "show me everything this tool covers" is usually the first
thing you want. It becomes `*` upstream, still bounded by the tool's filter and
the time range. An empty box with no tool filter in force stays an error: there
would be nothing narrowing the read at all. Icons: `gear`, `yeti`, `bolt`, `bug`, `chart`, `cloud`, `database`,
`globe`, `lock`, `phone`, `server`, `tag`, `terminal`.

**Where it is applied, and why that matters.** The filter is applied by the
server, from the tool's id, on every request. It is never composed in the
browser — `/api/*` is reachable with curl by anyone holding a session cookie, so
a prefix the SPA glues onto the query box could simply be left off the next
request. For the same reason, a request naming no tool gets the **first** tool's
filter rather than an unfiltered read.

It reaches VictoriaLogs as `extra_filters`, which propagates into every subquery
(`| join`, `| union`, `:in(...)`). A filter concatenated onto the front of the
query would be escapable through a subquery; VictoriaLogs' own documentation
names `extra_filters` as the mechanism for restricting queries to a subset of
logs.

**Is it access control?** Only if you configure it that way. By default any
signed-in account may select any tool, which is right when the rail is a set of
shortcuts. To make it a boundary: leave no unrestricted tool for someone to
switch to, make the first tool a safe default, and gate the wide ones with
`allowed_groups` (requires `auth.enabled`; matches `auth.groups_claim`):

```yaml
  - tooltip: Billing
    icon: lock
    query: 'system: billing'
    allowed_groups: [billing, admin]
```

A tool the account may not use is refused with 403 *and* omitted from
`/api/config`, so it never appears in their rail. None of this restricts anyone
who can reach VictoriaLogs directly — keep that on loopback or behind vmauth.

## Metrics

Served on `metrics.listen`, never on the application port — that one sits behind
OIDC and may sit under a base path, and a scraper should have to care about
neither.

| metric | what it says |
| --- | --- |
| `vlui_vl_up` | the last health probe of VictoriaLogs. Only exported when `probe_interval` is set |
| `vlui_vl_requests_total{endpoint,status}` | upstream calls, by endpoint and outcome |
| `vlui_vl_request_duration_seconds{endpoint}` | time to response headers (time-to-first-byte for the streams) |
| `vlui_http_requests_total{route,status}` | requests served, by chi route pattern |
| `vlui_query_rows_total`, `vlui_query_bytes_total` | log volume forwarded to browsers |
| `vlui_queries_active`, `vlui_tail_sessions_active` | what is open right now |
| `vlui_build_info{version,commit}` | always 1 |

`vl_up` is kept fresh by a background probe rather than by the scrape: a hung
VictoriaLogs would otherwise hang the scrape and take every other metric down
with it, exactly when they are needed. Set `probe_interval: 0` to drop both the
probe and the gauge and alert on the error rate instead.

## Developing

```sh
make dev        # the Go process on :8080, against ./config.yml
make dev-web    # Vite on :5173 with HMR, proxying /api to it
make check      # gofmt, go vet, go test, and the SPA type-check + build
make build      # SPA + binary, as it ships
```

The SPA is embedded with `go:embed`, so `web/dist` must exist before the Go
build: `make build` does both in order, and `make build-go` reuses whatever is
already built.

## How it fits together

```
browser ──► vlui ──► VictoriaLogs
             │
             └────► /metrics  (own listener)
```

The Go side is a thin, opinionated wrapper: it validates and clamps the
parameters, adds the tenant headers, and forwards the answer as it arrives.
Nothing is stored, cached or aggregated.

- `POST /api/query` streams NDJSON as VictoriaLogs finds it, so the table fills
  while the query is still running. Closing the connection abandons the query at
  the far end. A failure after the first row can no longer change the status
  code, so it arrives as a final `{"_vlui_error": "…"}` line.
- `GET /api/tail` is the same stream re-framed as SSE, bounded by
  `tail_max_duration`; the browser reconnects by itself.
- Upstream error text is passed through verbatim. A LogsQL syntax error names
  the offending token, and any wording of ours would be strictly less useful.
