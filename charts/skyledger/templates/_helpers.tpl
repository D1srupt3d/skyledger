{{/* Release "skyledger" names everything skyledger-*; any other release gets <release>-skyledger-*. */}}
{{- define "skyledger.fullname" -}}
{{- if contains "skyledger" .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-skyledger" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "skyledger.labels" -}}
app.kubernetes.io/name: skyledger
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{/* Call with (dict "root" $ "component" "ingest"). */}}
{{- define "skyledger.selector" -}}
app.kubernetes.io/name: skyledger
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "skyledger.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{- define "skyledger.secretName" -}}
{{ .Values.existingSecret | default (include "skyledger.fullname" .) }}
{{- end -}}

{{- define "skyledger.tar1090Url" -}}
{{- if .Values.tar1090Url -}}
{{ .Values.tar1090Url }}
{{- else if .Values.demo.enabled -}}
http://{{ include "skyledger.fullname" . }}-demo-feed:8080/
{{- else -}}
{{ fail "set tar1090Url to the base URL of your tar1090 (or demo.enabled=true to try skyledger without a receiver)" }}
{{- end -}}
{{- end -}}

{{- define "skyledger.dbHost" -}}
{{- if .Values.timescaledb.enabled -}}
{{ include "skyledger.fullname" . }}-timescaledb
{{- else -}}
{{ required "timescaledb.enabled is false: set database.host to your TimescaleDB" .Values.database.host }}
{{- end -}}
{{- end -}}

{{- define "skyledger.dbPort" -}}
{{ ternary 5432 .Values.database.port .Values.timescaledb.enabled }}
{{- end -}}

{{- define "skyledger.dbName" -}}
{{ ternary "skyledger" .Values.database.name .Values.timescaledb.enabled }}
{{- end -}}

{{- define "skyledger.dbUser" -}}
{{ ternary "skyledger" .Values.database.user .Values.timescaledb.enabled }}
{{- end -}}

{{/* Call with (dict "uid" 10001). */}}
{{- define "skyledger.podSecurity" -}}
runAsNonRoot: true
runAsUser: {{ .uid }}
runAsGroup: {{ .uid }}
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "skyledger.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}

{{- define "skyledger.secretEnv" -}}
- name: {{ .key }}
  valueFrom:
    secretKeyRef:
      name: {{ include "skyledger.secretName" .root }}
      key: {{ .key }}
{{- end -}}
