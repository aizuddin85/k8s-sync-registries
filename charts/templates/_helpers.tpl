{{/*
Expand the name of the chart.
*/}}
{{- define "sync-registry-cronjob.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a fullname for the CronJob, combining release name and chart name.
*/}}
{{- define "sync-registry-cronjob.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "sync-registry-cronjob.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Chart version.
*/}}
{{- define "sync-registry-cronjob.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version -}}
{{- end -}}
