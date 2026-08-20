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
- **Timestamps in your timezone** — `_time`, and any other field carrying an
  ISO-8601 instant, follow the zone selected in the rail, with the untouched
  value one hover away. A value with no zone (`2026-08-19 10:00:00`) is left
  exactly as stored: nothing here knows which clock the producer was on, and
  converting it would invent an offset.
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
  below) — the browser never composes it. Each tool keeps its own query, so
  switching between them returns you to what you were looking at.
- **Left rail** — the tools, then timezone (display only: every request carries
  instants, so changing it re-renders rather than re-queries), light/dark/system
  theme, the build version, and the signed-in user with Sign out. Both
  preferences are remembered per browser.
- **OIDC login** — any conformant provider; optionally restricted to a group,
  read from a configurable claim (`groups`, a Zitadel role URN, a nested
  Keycloak path) in whatever shape that provider sends.
- **Prometheus exporter**, built in, on its own port — optional, and off until
  you name a listen address.

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

That is the whole file. `listen` defaults to `127.0.0.1:8080`, and everything
else is off until you ask for it — no authentication, no tools, and no
Prometheus exporter:

```yaml
listen: 0.0.0.0:8080        # a container must not listen on loopback

metrics:
  listen: 127.0.0.1:9108    # naming an address is what turns the exporter on
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

`allowed_groups` reads from `groups_claim`, which defaults to `groups` and takes
a dotted path for providers that nest — `urn:zitadel:iam:org:project:roles` for
Zitadel, `resource_access.<client>.roles` for Keycloak client roles. The shape
is handled for you: an array, an object whose keys are the names, or a single
string. If a login is refused, run with `-debug`: the claims in the token and
what was read from the configured one are logged.

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

**Each tool remembers its own query.** A query written for one slice of the logs
is usually meaningless against another, so switching tools swaps the box rather
than carrying the text across, and switching back restores it. A tool you have
not visited starts empty when it has a filter of its own, and at `*` when it
does not. The memory lasts for the session; the URL carries the active tool's
query, so links and reloads still work.

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
