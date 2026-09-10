{{- /*
SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/*
Scheduler name
*/}}
{{- define "slurm-bridge.scheduler.name" -}}
{{ .Values.schedulerConfig.schedulerName | default "slurm-bridge-scheduler" }}
{{- end }}

{{/*
Scheduler Labels
*/}}
{{- define "slurm-bridge.scheduler.labels" -}}
{{ include "slurm-bridge.labels" . }}
{{ include "slurm-bridge.scheduler.selectorLabels" . }}
app.kubernetes.io/component: scheduler
{{- end }}

{{/*
Scheduler selector labels
*/}}
{{- define "slurm-bridge.scheduler.selectorLabels" -}}
app.kubernetes.io/name: {{ include "slurm-bridge.scheduler.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Determine scheduler image repository
*/}}
{{- define "slurm-bridge.scheduler.image.repository" -}}
{{ .Values.scheduler.image.repository | default "ghcr.io/slinkyproject/slurm-bridge" }}
{{- end }}

{{/*
Define scheduler image tag
*/}}
{{- define "slurm-bridge.scheduler.image.tag" -}}
{{ .Values.scheduler.image.tag | default .Chart.Version }}
{{- end }}

{{/*
Determine scheduler image reference (repo:tag)
*/}}
{{- define "slurm-bridge.scheduler.imageRef" -}}
{{ printf "%s:%s" (include "slurm-bridge.scheduler.image.repository" .) (include "slurm-bridge.scheduler.image.tag" .) | quote }}
{{- end }}
