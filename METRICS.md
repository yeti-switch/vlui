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

```yaml
groups:
  - name: vlui
    rules:
      # VictoriaLogs is unreachable. Only exists when probe_interval is set.
      - alert: VictoriaLogsDown
        expr: vlui_vl_up == 0
        for: 2m

      # VictoriaLogs is refusing our requests. The endpoint and status labels
      # say which call and why, so the alert is actionable.
      - alert: VictoriaLogsErrors
        expr: rate(vlui_vl_requests_total{status!="ok"}[5m]) > 0
        for: 5m

      # Somebody's browser tab has been holding a query open. Normal for a live
      # tail; suspicious for `queries_active`, which should be seconds at a time.
      - alert: VluiQueriesStuck
        expr: vlui_queries_active > 5
        for: 15m
```

With `probe_interval: 0` there is no `vl_up`, so `VictoriaLogsDown` never fires
and `VictoriaLogsErrors` is the only signal — which is the trade, and why the
probe is on by default.

## Looking at it by hand

```sh
curl -s http://127.0.0.1:9108/metrics | grep '^vlui_'
```

In a cluster, the exporter is on the pod rather than behind the Service's main
port:

```sh
kubectl port-forward deploy/vlui 9108:9108
```
