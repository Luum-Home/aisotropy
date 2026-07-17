{{/* Expand the name of the chart. */}}
{{- define "aisotropy.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name. */}}
{{- define "aisotropy.fullname" -}}
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

{{- define "aisotropy.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Common labels. */}}
{{- define "aisotropy.labels" -}}
helm.sh/chart: {{ include "aisotropy.chart" . }}
{{ include "aisotropy.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: aisotropy
{{- end -}}

{{/* Selector labels — aisotropyd runtime. */}}
{{- define "aisotropy.selectorLabels" -}}
app.kubernetes.io/name: {{ include "aisotropy.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: runtime
{{- end -}}

{{/* ServiceAccount name. */}}
{{- define "aisotropy.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "aisotropy.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Name of the Secret holding auth material (chart-created or existing). */}}
{{- define "aisotropy.secretName" -}}
{{- if .Values.auth.existingSecret -}}
{{- .Values.auth.existingSecret -}}
{{- else -}}
{{- printf "%s-secrets" (include "aisotropy.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/* True when the chart should render its own Secret (any secret value set and
     no external Secret referenced). */}}
{{- define "aisotropy.createSecret" -}}
{{- if and (not .Values.auth.existingSecret) (or .Values.auth.jwtSecret .Values.anthropicApiKey .Values.opsToken) -}}
true
{{- end -}}
{{- end -}}

{{/* Name of the Secret holding networked-tier credentials (chart-created or
     existing). */}}
{{- define "aisotropy.tierSecretName" -}}
{{- if .Values.tiers.networked.auth.existingSecret -}}
{{- .Values.tiers.networked.auth.existingSecret -}}
{{- else -}}
{{- printf "%s-tier-secrets" (include "aisotropy.fullname" .) -}}
{{- end -}}
{{- end -}}
