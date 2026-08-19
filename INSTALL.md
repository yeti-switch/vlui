# Installing vlui

vlui is a single Go binary with the web UI embedded in it, configured by one
YAML file. It keeps no state of its own — no database, no cache, no session
store — so there is nothing to back up and nothing to migrate.

Three ways to run it: a Debian package, a Helm chart, or the container image.
All three carry the same binary and the same config file.

After installing, see **[Configuring](README.md#configuring)** for the config
file itself, **[Tools](README.md#tools)** for the icons in the rail, and
**[METRICS.md](METRICS.md)** for the Prometheus exporter.

## Debian

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

## Kubernetes (Helm)

The chart is published as an OCI artifact alongside the image:

```sh
helm install vlui oci://ghcr.io/yeti-switch/charts/vlui \
  --version 1.0.0 \
  --set config.victorialogs.url=http://victorialogs:9428
```

Chart version == appVersion == the release tag, so chart 1.0.0 runs vlui 1.0.0
with no `image.tag` needed.

It ships an **HTTPRoute** (Gateway API), not an Ingress, and a **PodMonitor**,
not a ServiceMonitor. Both are off by default — each needs CRDs the chart does
not install:

```yaml
httpRoute:
  enabled: true
  parentRefs:
    - name: external
      namespace: gateway-system
      sectionName: https
  hostnames: [logs.example.com]
  # Live tailing is a long-lived response; most Gateways default to a request
  # timeout that would cut it.
  timeouts:
    request: 1h

podMonitor:
  enabled: true
  # Most kube-prometheus-stack installs need a release label here, or their
  # podMonitorSelector never matches this.
  labels:
    release: kube-prometheus-stack
```

The PodMonitor scrapes the pod IP directly, which is why the chart rewrites
`listen` and `metrics.listen` to `0.0.0.0` (keeping your ports): the loopback
defaults that are right on a systemd host are reachable from nothing inside a
pod.

`config:` in values is the same file documented in `config.example.yml`,
rendered into a ConfigMap. To supply one templated elsewhere, name it instead —
the chart then stops managing the config, so editing that ConfigMap does not
roll the pods (`kubectl rollout restart` does):

```sh
kubectl create configmap vlui-config --from-file=config.yml
helm install vlui oci://ghcr.io/yeti-switch/charts/vlui --set existingConfigMap=vlui-config
```

With `auth.enabled`, note that `client_secret` and `cookie_secret` are then in a
ConfigMap — readable by anything with namespace read access, and not treated as
sensitive by RBAC or audit.

Set `config.base_path` and the chart follows it — the probes and the HTTPRoute's
path match both move with it. Several configurations that cannot work are
refused at render time rather than at runtime: an HTTPRoute with no
`parentRefs`, a PodMonitor with the exporter disabled, `auth.enabled` without a
usable `cookie_secret`, and a `redirect_url` that disagrees with `base_path`.

## Container

```sh
docker run --rm -p 8080:8080 -p 9108:9108 \
  -v ./config.yml:/opt/vlui/etc/config.yml:ro \
  ghcr.io/yeti-switch/vlui:latest
```

The image is `gcr.io/distroless/static-debian13:nonroot`: no shell, no package
manager, no libc — about 2 MB of base under a static binary.

The config baked into it listens on `0.0.0.0` (loopback is reachable from
nothing inside a container) and enables the exporter on 9108. Mount your own
over `/opt/vlui/etc/config.yml`, or name a different one — `docker run … vlui
-config /custom.yml` replaces the image's CMD. The exporter is opt-in, so a
config of your own without `metrics.listen` leaves 9108 closed.
