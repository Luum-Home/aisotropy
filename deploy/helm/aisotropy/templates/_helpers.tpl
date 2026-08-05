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

{{/* Cross-value guards. Rendered unconditionally from deployment.yaml so an
     impossible combination fails at TEMPLATE time instead of at 03:00 as a
     CrashLoop. Same posture as the autoscaling/resources.requests guard in
     hpa.yaml: refuse to emit a manifest that cannot work. */}}
{{- define "aisotropy.validate" -}}
{{- if .Values.persistence.enabled -}}
{{- $max := .Values.replicaCount -}}
{{- if .Values.autoscaling.enabled -}}
{{- $max = .Values.autoscaling.maxReplicas -}}
{{- end -}}
{{- if gt (int $max) 1 -}}
{{- fail (printf "persistence.enabled with %d replicas cannot boot. aisotropyd's working memory is an embedded BadgerDB that takes an EXCLUSIVE directory lock (badger.Open, internal/memory/working_memory.go), and every pod mounts the same ReadWriteOnce PVC — the second pod fails to attach the volume or crash-loops on the lock. Either keep one replica (persistence + replicaCount=1, autoscaling off or maxReplicas=1), or run persistence.enabled=false and scale out with sessionAffinity.enabled=true, accepting the cold-start-on-pod-loss documented in values.yaml." (int $max)) -}}
{{- end -}}
{{- end -}}
{{- if .Values.sessionAffinity.enabled -}}
{{- if ne .Values.sessionAffinity.controller "nginx" -}}
{{- fail (printf "sessionAffinity.controller=%q is not implemented. This chart only translates the ingress-nginx dialect (nginx.ingress.kubernetes.io/upstream-hash-by). For another controller set sessionAffinity.enabled=false and express affinity yourself via ingress.annotations." .Values.sessionAffinity.controller) -}}
{{- end -}}
{{- if not .Values.sessionAffinity.hashBy -}}
{{- fail "sessionAffinity.enabled requires sessionAffinity.hashBy — the nginx variable to hash on (e.g. \"$request_uri\"). An empty upstream-hash-by disables hashing silently." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Session-affinity annotations for the fronting Ingress. Empty string unless
     sessionAffinity.enabled — callers must treat "" as "nothing to add".
     Validation lives in aisotropy.validate so it runs even when this template
     is not reached (ingress.enabled=false). */}}
{{- define "aisotropy.sessionAffinityAnnotations" -}}
{{- if .Values.sessionAffinity.enabled -}}
nginx.ingress.kubernetes.io/upstream-hash-by: {{ .Values.sessionAffinity.hashBy | quote }}
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
