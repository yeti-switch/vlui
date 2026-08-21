# Metrics

The exporter is **optional and off by default**. It runs only when
`metrics.listen` names an address:

```yaml
metrics:
  listen: 127.0.0.1:9108
  path: /metrics
  probe_interval: 15s
```

Leave the block out — or leave `listen` out of it, or set it to `""` — and vlui
opens no second socket, keeps no registry, and never probes VictoriaLogs. A
deployment with no Prometheus has nothing to scrape it, and a process should not
open a port nobody asked for.

When it is on, it is on its own socket, never a route on the application port:
that one sits behind OIDC and may sit under a base path, and a scraper should
have to care about neither.

In Kubernetes the chart ships a PodMonitor (`podMonitor.enabled=true`), which
scrapes the pod IP directly — see [the chart's values](charts/vlui/values.yaml).
The chart's own values do set an address, so the exporter is on there by
default; deleting `config.metrics` from your values turns it off, and the chart
then declares no metrics port on the container or the Service.

## What is exported

| metric | type | what it says |
| --- | --- | --- |
| `vlui_vl_up` | gauge | 1 if the last VictoriaLogs health probe succeeded. **Absent** when `probe_interval` is 0 |
| `vlui_vl_requests_total{endpoint,status}` | counter | upstream calls, by endpoint and outcome (`ok`, `error`, or an HTTP status) |
| `vlui_vl_request_duration_seconds{endpoint}` | histogram | time to response headers — time-to-first-byte for the streaming endpoints, not the life of the stream |
| `vlui_http_requests_total{route,status}` | counter | requests served, by chi route pattern |
| `vlui_http_request_duration_seconds{route}` | histogram | time to serve one request |
| `vlui_query_rows_total` | counter | log lines forwarded to browsers |
| `vlui_query_bytes_total` | counter | bytes of log data forwarded to browsers |
| `vlui_queries_active` | gauge | queries in flight right now |
| `vlui_tail_sessions_active` | gauge | live-tail streams open right now |
| `vlui_build_info{version,commit}` | gauge | always 1; the build is in the labels |

Plus the usual `go_*` and `process_*` series.

`route` is the chi pattern, not the path — `/api/field_values`, never
`/api/field_values?field=host` — so a thousand distinct fields cannot become a
thousand time series.

## Why `vl_up` is probed in the background

The gauge has to mean something on an idle instance. Updated only when somebody
runs a query, it would report the state of whenever anyone last looked: a
VictoriaLogs that died at 02:00 would still read healthy from the previous
evening's traffic.

Probing during the scrape instead is worse. A hung VictoriaLogs would hang the
scrape, the exporter would time out, and you would lose **every** metric at
exactly the moment they matter.

So it is a background tick — one `GET /health` every `probe_interval`. Set
`probe_interval: 0` to drop both the probe and the gauge; the series then
disappears rather than freezing, and you alert on the error rate instead.

## Alerting

Rules ship with the repo: **[deploy/alerts/vlui.yml](deploy/alerts/vlui.yml)**,
in the same format as the other yeti services' vmalert rules. Drop it beside
them in the ansible victoria-metrics role, or let the Helm chart install it as a
PrometheusRule (`prometheusRule.enabled=true`).

| alert | fires when | severity |
| --- | --- | --- |
| `VluiDown` | nothing is being scraped at all | major |
| `VluiVictoriaLogsDown` | the health probe fails for 5m | major |
| `VluiVictoriaLogsErrors` | transport failures or 5xx from VictoriaLogs for 10m | major |
| `VluiInternalErrors` | vlui returns 500 for 10m | minor |
| `VluiQueriesStuck` | queries in flight for 15m | minor |
| `VluiSlowQueries` | 90th percentile time-to-first-row over 30s | minor |

Nothing is `critical`: vlui is how you read logs, not how calls get routed. If
it is down at 3am the logs are still being collected.

One thing worth knowing if you write your own: **do not alert on
`vlui_vl_requests_total{status!="ok"}`.** The status label carries the upstream
HTTP code, and a 400 is almost always a LogsQL syntax error — somebody mistyped
a query. That rule pages a human for a typo. Match `status=~"error|5.."`
instead, which is what the shipped rules do.

`deploy/alerts/vlui_test.yml` is a promtool test for these, including that case:

```sh
cd deploy/alerts && promtool test rules vlui_test.yml
```

## Looking at it by hand

```sh
curl -s http://127.0.0.1:9108/metrics | grep '^vlui_'
```

In a cluster the exporter is on the pod only — it is deliberately not a Service
port, since a PodMonitor addresses pods directly and a Service would just
load-balance a scrape onto a different replica each time. So port-forward the
deployment, not the service:

```sh
kubectl port-forward deploy/vlui 9108:9108
```
