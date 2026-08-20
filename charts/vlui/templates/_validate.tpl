{{/*
Checks that fail the render rather than the pod.

Each of these is something that produces a confusing failure much later: a
CrashLoopBackOff with one line of stderr, or worse, a running instance that is
quietly wrong.
*/}}

{{- define "vlui.validate" -}}

{{- if .Values.existingConfigMap -}}
{{/* Nothing to check: the config is theirs and this chart cannot see it. */}}
{{- else if .Values.config.auth.enabled -}}

  {{- $auth := .Values.config.auth -}}

  {{/*
  YAML reads an unquoted run of digits as a NUMBER. A cookie_secret of
  1234567890123456789012345678901234567890 arrives here as a float and renders
  back as 1.2345678901234568e+39 — a different, shorter, and silently wrong
  secret. The same applies to client_secret, where the damage is invisible:
  the IdP simply rejects every login.

  openssl rand -hex 32 usually contains a letter, which is why this trap is
  rare enough to be baffling when it does happen.
  */}}
  {{- range $key := list "cookie_secret" "client_secret" -}}
    {{- $v := index $auth $key -}}
    {{- if and $v (ne (kindOf $v) "string") -}}
      {{- fail (printf "config.auth.%s is a %s, not a string — YAML read it as a number. Quote it: %s: \"...\"" $key (kindOf $v) $key) -}}
    {{- end -}}
  {{- end -}}

  {{/*
  An empty cookie_secret is allowed — vlui generates one at startup — but only
  for a single pod. Each replica generates its OWN, so a session created against
  one is refused by the others: behind a Service, people are signed out at
  random, on some requests and not others. That reads as a broken application
  rather than a missing setting, which is exactly why it fails here instead.

  A rolling update has the same effect even at one replica, and that one is
  survivable (everybody signs in again), so it is a warning in the pod's log
  rather than a refusal here.
  */}}
  {{- if and (not $auth.cookie_secret) (gt (int .Values.replicaCount) 1) -}}
    {{- fail (printf "%s\n\n%s\n\n%s"
      (printf "config.auth.cookie_secret is empty and replicaCount is %d." (int .Values.replicaCount))
      "vlui generates a random secret when none is set, and each replica generates a different one — a session signed by one pod is refused by every other, so users are signed out on roughly (n-1)/n of their requests."
      "Set config.auth.cookie_secret (openssl rand -hex 32), or run a single replica.") -}}
  {{- end -}}
  {{- if and $auth.cookie_secret (lt (len (toString $auth.cookie_secret)) 32) -}}
    {{- fail (printf "config.auth.cookie_secret is %d bytes; vlui requires at least 32, or leave it empty to have one generated" (len (toString $auth.cookie_secret))) -}}
  {{- end -}}
  {{- if not $auth.issuer -}}
    {{- fail "config.auth.enabled is true but config.auth.issuer is empty" -}}
  {{- end -}}
  {{- if not $auth.redirect_url -}}
    {{- fail "config.auth.enabled is true but config.auth.redirect_url is empty — it must match what the provider has registered, base_path included" -}}
  {{- end -}}

  {{/*
  The redirect URL is registered with the IdP and must match byte for byte.
  Getting base_path wrong here means every login lands on a 404 after the
  round trip to the provider, which looks like an IdP problem and is not.
  */}}
  {{- $base := include "vlui.basePath" . -}}
  {{- $want := printf "%s/api/auth/callback" $base -}}
  {{- if not (hasSuffix $want (toString $auth.redirect_url)) -}}
    {{- fail (printf "config.auth.redirect_url must end in %q for this base_path, got %q" $want $auth.redirect_url) -}}
  {{- end -}}

{{- end -}}

{{- if not (default "" .Values.config.victorialogs.url) -}}
  {{- fail "config.victorialogs.url must be set — there is nothing to read logs from" -}}
{{- end -}}

{{/*
A tail that outlives the Gateway's request timeout is cut mid-stream, and the
browser reconnects in a loop that looks like a flapping backend.
*/}}
{{- if and .Values.httpRoute.enabled .Values.httpRoute.timeouts.request -}}
  {{- $t := toString .Values.httpRoute.timeouts.request -}}
  {{- if and (ne $t "0s") (ne $t "0") -}}
    {{- $tail := toString (default "1h" .Values.config.victorialogs.tail_max_duration) -}}
    {{- if ne $t $tail -}}
      {{- /* Not fatal: durations are not comparable here without parsing them,
             and a deliberately shorter timeout is a legitimate choice. */ -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{- end -}}
