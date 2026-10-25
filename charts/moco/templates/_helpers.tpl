{{- define "moco.fullname" -}}
{{- default (printf "moco-%s" .Release.Name | replace "." "-") .Values.fullnameOverride | trunc 52 | trimSuffix "-" -}}
{{- end -}}

{{- define "moco.selectorLabels" -}}
app.kubernetes.io/name: moco
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}

{{- define "moco.labels" -}}
{{ include "moco.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}

{{- define "moco.image" -}}
{{ required "image.repository is required" .Values.image.repository }}@{{ required "image.digest is required" .Values.image.digest }}
{{- end -}}

{{- define "moco.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}

{{- define "moco.validate" -}}
{{- if ne (int .Values.replicaCount) 1 }}{{ fail "SQLite requires replicaCount=1" }}{{ end -}}
{{- if ge (int .Values.backups.timeoutSeconds) (int .Values.backups.intervalSeconds) }}{{ fail "backups.timeoutSeconds must be less than intervalSeconds" }}{{ end -}}
{{- if le (int .Values.backups.maxAgeSeconds) (add (int .Values.backups.intervalSeconds) (int .Values.backups.timeoutSeconds)) }}{{ fail "backups.maxAgeSeconds must exceed intervalSeconds + timeoutSeconds" }}{{ end -}}
{{- $dataClaim := default (printf "data-%s-0" (include "moco.fullname" .)) .Values.persistence.data.existingClaim -}}
{{- $backupClaim := default (printf "backups-%s-0" (include "moco.fullname" .)) .Values.persistence.backups.existingClaim -}}
{{- $claims := list $dataClaim $backupClaim .Values.backups.mirrorClaim -}}
{{- range $i, $claim := $claims -}}
{{- if $claim -}}{{- range $j, $other := $claims -}}{{- if and (ne $i $j) (eq $claim $other) }}{{ fail "data, local backup and mirror claims must be distinct" }}{{ end -}}{{- end -}}{{- end -}}
{{- end -}}
{{- if .Values.monitoring.enabled -}}
{{- if not .Values.networkPolicy.enabled }}{{ fail "monitoring requires NetworkPolicy protection for the administrative listener" }}{{ end -}}
{{- range list .Values.monitoring.namespaceSelector .Values.monitoring.podSelector -}}
{{- if and (empty .matchLabels) (empty .matchExpressions) }}{{ fail "monitoring requires explicit nonempty namespace and pod selectors" }}{{ end -}}
{{- end -}}
{{- if eq .Values.monitoring.existingSecret .Values.credentials.existingSecret }}{{ fail "monitoring must use a separate token/CA-only Secret" }}{{ end -}}
{{- if not (.Capabilities.APIVersions.Has "monitoring.coreos.com/v1/ServiceMonitor") }}{{ fail "monitoring requires the Prometheus Operator ServiceMonitor CRD" }}{{ end -}}
{{- end -}}
{{- end -}}
