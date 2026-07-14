{{/* Chart name */}}
{{- define "sebastian.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Fully qualified app name */}}
{{- define "sebastian.fullname" -}}
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

{{/* Chart label */}}
{{- define "sebastian.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Common labels */}}
{{- define "sebastian.labels" -}}
helm.sh/chart: {{ include "sebastian.chart" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: sebastian
{{- end }}

{{/* Per-component selector labels: {{ include "sebastian.selectorLabels" (dict "ctx" . "component" "server") }} */}}
{{- define "sebastian.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sebastian.name" .ctx }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/* Hardened pod security context (non-root), shared by the Python services */}}
{{- define "sebastian.appSecurityContext" -}}
runAsNonRoot: true
runAsUser: 1001
runAsGroup: 1001
fsGroup: 1001
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{/* Hardened container security context, shared by the Python services */}}
{{- define "sebastian.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end }}

{{/* Shared LiveKit connection env (URL + key + secret) for the Python services */}}
{{- define "sebastian.livekitEnv" -}}
{{- $secret := required "existingSecret must be set (keys: LIVEKIT_URL, LIVEKIT_API_KEY, LIVEKIT_API_SECRET)" .existingSecret }}
- name: LIVEKIT_URL
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: LIVEKIT_URL
- name: LIVEKIT_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: LIVEKIT_API_KEY
- name: LIVEKIT_API_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ $secret }}
      key: LIVEKIT_API_SECRET
{{- end }}
