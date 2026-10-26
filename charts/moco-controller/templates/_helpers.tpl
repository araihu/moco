{{- define "moco-controller.name" -}}
{{- printf "%s-controller" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "moco-controller.labels" -}}
app.kubernetes.io/name: moco-controller
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}
