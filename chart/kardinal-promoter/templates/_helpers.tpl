{{/*
Copyright 2026 The kardinal-promoter Authors.
Licensed under the Apache License, Version 2.0
*/}}

{{/*
Expand the name of the chart.
*/}}
{{- define "kardinal-promoter.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
Truncate at 63 chars because some Kubernetes name fields are limited.
*/}}
{{- define "kardinal-promoter.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "kardinal-promoter.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kardinal-promoter.labels" -}}
helm.sh/chart: {{ include "kardinal-promoter.chart" . }}
{{ include "kardinal-promoter.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kardinal-promoter.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kardinal-promoter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kardinal-promoter.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kardinal-promoter.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
krocodile image reference.
Uses krocodile.image.tag if set, otherwise falls back to krocodile.pinnedCommit.
This ensures the bundled krocodile version is always deterministic.
*/}}
{{- define "krocodile.image" -}}
{{- $tag := .Values.krocodile.image.tag | default .Values.krocodile.pinnedCommit -}}
{{- printf "%s:%s" .Values.krocodile.image.repository $tag -}}
{{- end }}
