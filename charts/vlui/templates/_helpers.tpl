{{/*
Common helpers — names, labels, selectors, and the two indirections that decide
where the config comes from.
*/}}

{{- define "vlui.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "vlui.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "vlui.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "vlui.labels" -}}
helm.sh/chart: {{ include "vlui.chart" . }}
{{ include "vlui.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "vlui.selectorLabels" -}}
app.kubernetes.io/name: {{ include "vlui.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
configMapName / configMapKey: where config.yml comes from. Either the ConfigMap
this chart renders from .Values.config, or one that already exists — which is
how a config templated by something else (Ansible, Kustomize, an operator) gets
in without going through values.
*/}}
{{- define "vlui.configMapName" -}}
{{- if .Values.existingConfigMap -}}
{{- .Values.existingConfigMap -}}
{{- else -}}
{{- printf "%s-config" (include "vlui.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "vlui.configMapKey" -}}
{{- if .Values.existingConfigMap -}}
{{- default "config.yml" .Values.existingConfigMapKey -}}
{{- else -}}
config.yml
{{- end -}}
{{- end -}}

{{/*
renderedConfig: the application config, with the listen addresses forced to
0.0.0.0.

Both are deliberately not left to the operator. The defaults that ship in
config.example.yml are loopback, because that is right for a systemd host behind
nginx and wrong in every container: a process listening on 127.0.0.1 inside a
network namespace is reachable from nothing — not the Service, not the
HTTPRoute, and not the PodMonitor, which scrapes the pod IP directly. A chart
that let you paste the example config in and then answered nothing would be a
chart nobody could debug.

The PORTS stay yours: only the host part is replaced.
*/}}
{{- define "vlui.renderedConfig" -}}
{{- $cfg := deepCopy .Values.config -}}
{{- $listen := default "0.0.0.0:8080" $cfg.listen -}}
{{- $_ := set $cfg "listen" (printf "0.0.0.0:%s" (last (splitList ":" $listen))) -}}
{{- if and $cfg.metrics $cfg.metrics.listen -}}
{{- $_ := set $cfg.metrics "listen" (printf "0.0.0.0:%s" (last (splitList ":" $cfg.metrics.listen))) -}}
{{- end -}}
{{- toYaml $cfg -}}
{{- end -}}

{{/*
The ports the container actually listens on, taken from the config rather than
configured twice. A containerPort that disagreed with the config would be a
Service pointing at a closed socket.
*/}}
{{- define "vlui.httpPort" -}}
{{- last (splitList ":" (default "0.0.0.0:8080" .Values.config.listen)) -}}
{{- end -}}

{{/*
metricsPort is empty when the exporter is off, which is what every
`{{- if include "vlui.metricsPort" . }}` in this chart keys on.

The exporter is opt-in in vlui itself: no metrics.listen means no listener, no
registry and no health probe. The chart follows that exactly rather than
supplying a port of its own — a container port and a PodMonitor pointing at a
socket the process never opened would be worse than no metrics at all.

values.yaml does set an address, so the chart's own default is "on"; deleting
that key turns it off, in the chart and in the binary alike.
*/}}
{{- define "vlui.metricsPort" -}}
{{- $m := default dict .Values.config.metrics -}}
{{- if $m.listen -}}
{{- last (splitList ":" $m.listen) -}}
{{- end -}}
{{- end -}}

{{- define "vlui.metricsPath" -}}
{{- $m := default dict .Values.config.metrics -}}
{{- default "/metrics" $m.path -}}
{{- end -}}

{{/*
basePath: the sub-path the whole app is mounted under, normalised to "" or
"/logs". Everything that has to agree with it — the probes, the HTTPRoute's
default path match, the NOTES — reads it from here.
*/}}
{{- define "vlui.basePath" -}}
{{- $p := default "" .Values.config.base_path | trimAll "/" -}}
{{- if $p -}}/{{ $p }}{{- end -}}
{{- end -}}

{{/*
The health endpoint, which moves with base_path: the whole application is
mounted under it, /healthz included. A probe left at /healthz against an
instance running at /logs gets a 404 and CrashLoopBackOff, which is a miserable
thing to debug.
*/}}
{{- define "vlui.healthPath" -}}
{{- printf "%s/healthz" (include "vlui.basePath" .) -}}
{{- end -}}
