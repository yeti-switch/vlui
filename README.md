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
- **Field sidebar** — the most frequent values per field, searchable by field
  name, one click to filter.
- **Live tailing** — follow new logs as they arrive.
- **Timestamps** in the timezone you pick.
- **Tools** — configurable icons in the left rail, each scoping the session to a
  slice of the logs. Applied server-side.
- **Shareable state** — the tool is the path (`/http`), and the query and time
  range live in the URL with it.
- **OIDC login** — any conformant provider.
- **Prometheus exporter** — built in, optional, with alerting rules included.

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
it. The tab's title and icon are worth setting when several of these are open at
once:

```yaml
ui:
  title: Yeti production logs
  favicon: /opt/vlui/etc/logo.svg    # a file on this host; svg, png, ico, …
```

Authentication needs an OIDC client:

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
  - id: main
    tooltip: Everything
    icon: gear

  - id: yeti
    tooltip: Yeti Logs
    icon: yeti
    query: 'named_tags.system: yeti'
    fields: [_time, level, host, _msg]

  - id: http
    letters: WEB
    query: 'system: nginx'
    # A label is what the column header shows; a long field name costs width
    # for nothing. The field panel and log entry keep the real name.
    fields:
      - _time
      - _msg
      - {name: payload.response.status_code, label: status}

  - id: apilogs
    letters: API              # up to three characters, instead of an icon
    query: 'system: api'
```

`id` is required and unique — it *is* the URL, since a tool is a path segment
(`/http` selects the http tool above), and it is what each request sends, so
renaming one breaks links to it. Letters, digits, dashes and underscores, and
not `api`, `healthz` or `assets`: the server answers those paths itself, and a
tool named for one is refused at startup rather than becoming a dead link.
`tooltip` defaults to the id.

Each entry is an icon in the rail, labelled on hover. Selecting one scopes
everything — rows, histogram, facets, autocomplete, live tail — to its query,
shown as a static prefix beside the input. Each tool keeps its own query and its
own columns; `fields` is the default set, and whatever the reader picks is
remembered per tool in their browser.

### Value styles

A field can carry `style:`, naming one of the ways to draw values defined once
at the top level — colour in the table and in the log entry, so a 500 among two
hundred 200s is found by eye rather than by reading the column:

```yaml
value_styles:
  http_status:                          # what yeti-web's API log has always done
    rules:
      - {range: 200-399, color: ok}
      - {range: 400-499, color: warn, description: rejected — the request was wrong}
      - {range: 500-599, color: error, description: the application failed}
  slow:
    type: text
    rules:
      - {range: 1000-, color: error}    # a threshold, with no upper bound
      - {range: 250-999, color: warn}

tools:
  - id: http
    fields:
      - {name: payload.status, label: status, style: http_status}
      - {name: duration, style: slow}
```

`type` says how the colour lands. `tag`, the default, is a tinted pill — for a
category like a status or a method, read as a badge. `text` colours the value
itself — for a measurement that is merely alarming past some point, where a
column of pills would read as categories and the digits would stop lining up.

`rules` are tried in order, first match wins, and each carries exactly one
matcher — `value` (one or a list), `range` (inclusive, numeric values only,
either end optional), `prefix`, or `default` (last rule only) — plus a colour
named from `ok`, `warn`, `error`, `info`, `neutral`, `muted`, and optionally a
`description`, which appears under the value in the cell's tooltip: the colour
says a value is worth attention, the description says why. Names rather than
hex: each resolves to the current theme's own colour, so styles work in light and
dark. A value no rule matches is drawn as plain text.

Matching happens in the browser; the server publishes the styles with the rest
of the config and refuses a bad one — unknown colour or type, backwards range,
`style:` naming one that does not exist — at startup.

Each tool carries either an `icon` or up to three `letters` — past a handful of
tools the abstract shapes stop being distinguishable, while `API` needs no
legend. Icons: `gear`, `yeti`, `bolt`, `bug`, `chart`, `cloud`, `database`,
`globe`, `lock`, `phone`, `server`, `tag`, `terminal`.

The filter is applied by the **server**, from the tool's id, as VictoriaLogs
`extra_filters` — never composed in the browser, and a request naming no tool
gets the first tool's filter. To make the rail a boundary rather than a set of
shortcuts, gate the wide tools with `allowed_groups` (needs `auth.enabled`):

```yaml
  - id: billing
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
