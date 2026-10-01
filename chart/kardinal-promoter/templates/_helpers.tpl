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
Host names the UI server answers to (--ui-allowed-hosts): the controller
Service's DNS names plus ui.allowedHosts. The controller always allows
localhost, 127.0.0.1 and ::1 as well.
*/}}
{{- define "kardinal-promoter.uiAllowedHosts" -}}
{{- $svc := include "kardinal-promoter.fullname" . -}}
{{- $ns := .Release.Namespace -}}
{{- $own := list $svc (printf "%s.%s" $svc $ns) (printf "%s.%s.svc" $svc $ns) (printf "%s.%s.svc.cluster.local" $svc $ns) -}}
{{- concat $own (.Values.ui.allowedHosts | default list) | uniq | join "," -}}
{{- end }}

{{/*
ClusterRoles bound to the Graph ServiceAccount (templates/graph-rbac.yaml).
*/}}
{{- define "kardinal-promoter.graphApplierRole" -}}
{{- printf "%s-graph-applier" (include "kardinal-promoter.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}
{{- define "kardinal-promoter.graphReaderRole" -}}
{{- printf "%s-graph-reader" (include "kardinal-promoter.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
Install-mode checks. Rendered from deployment.yaml so a bad combination fails
`helm install` before anything is applied.
*/}}
{{- define "kardinal-promoter.validate" -}}
{{- $w := .Values.controller.watchNamespace -}}
{{- if and $w (ne $w .Release.Namespace) -}}
{{- fail (printf "controller.watchNamespace (%s) must equal the release namespace (%s): in namespace mode the controller's cache holds only the watch namespace, and its Lease, kardinal-version ConfigMap and SCM token Secret live in the release namespace. Install the chart into %s." $w .Release.Namespace $w) -}}
{{- end -}}
{{- if $w -}}
{{- range .Values.controller.policyNamespaces -}}
{{- if ne . $w -}}
{{- fail (printf "controller.policyNamespaces entry %q is outside controller.watchNamespace (%s); in namespace mode org-level PolicyGates must live in the watch namespace." . $w) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if and .Values.github.token .Values.github.secretRef.name -}}
{{- fail "set github.token or github.secretRef.name, not both" -}}
{{- end -}}
{{- with .Values.github.secretRef.namespace -}}
{{- if ne . $.Release.Namespace -}}
{{- fail (printf "github.secretRef.namespace (%s) must be empty or the release namespace (%s): GITHUB_TOKEN is read with a secretKeyRef, which only reads the Pod's namespace, so the startup token and the rotation watcher would read different Secrets." . $.Release.Namespace) -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Value for --policy-namespaces, or "" to keep the controller default
(platform-policies). Namespace mode always uses the watch namespace.
*/}}
{{- define "kardinal-promoter.policyNamespaces" -}}
{{- if .Values.controller.watchNamespace -}}
{{- .Values.controller.watchNamespace -}}
{{- else -}}
{{- join "," .Values.controller.policyNamespaces -}}
{{- end -}}
{{- end }}

{{/*
Name of the Secret holding the SCM token: the chart-owned Secret when
github.token is set, else github.secretRef.name.
*/}}
{{- define "kardinal-promoter.githubSecretName" -}}
{{- if .Values.github.token -}}
{{- printf "%s-github-token" (include "kardinal-promoter.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .Values.github.secretRef.name -}}
{{- end -}}
{{- end }}

{{/*
Key of the SCM token in that Secret.
*/}}
{{- define "kardinal-promoter.githubSecretKey" -}}
{{- if .Values.github.token -}}
token
{{- else -}}
{{- .Values.github.secretRef.key | default "token" -}}
{{- end -}}
{{- end }}

{{/*
controller-runtime's --zap-log-level accepts debug, info, error and panic
(or a positive integer), not warn. Map warn to error.
*/}}
{{- define "kardinal-promoter.zapLogLevel" -}}
{{- if eq .Values.logLevel "warn" -}}error{{- else -}}{{ .Values.logLevel }}{{- end -}}
{{- end }}

{{/*
The container port a bind address (":8080", "0.0.0.0:8080") listens on.
Called with (list <bind address> <fallback port>); the fallback is for an
address with no port or port 0 (controller-runtime's "disabled").
*/}}
{{- define "kardinal-promoter.bindPort" -}}
{{- $port := splitList ":" (index . 0) | last -}}
{{- if and $port (ne $port "0") -}}{{ $port }}{{- else -}}{{ index . 1 }}{{- end -}}
{{- end }}

{{/*
The controller's container ports. Metrics and health listen on their bind
addresses; the UI and webhook servers listen on their Service ports.
*/}}
{{- define "kardinal-promoter.containerPorts" -}}
{{- $ports := dict -}}
{{- $_ := set $ports "metrics" (include "kardinal-promoter.bindPort" (list .Values.metricsBindAddress .Values.service.metricsPort)) -}}
{{- $_ = set $ports "health" (include "kardinal-promoter.bindPort" (list .Values.healthProbeBindAddress .Values.service.healthPort)) -}}
{{- $_ = set $ports "ui" (toString .Values.service.uiPort) -}}
{{- $_ = set $ports "webhook" (toString .Values.service.webhookPort) -}}
{{- toJson $ports -}}
{{- end }}
