{{- /*
SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/*
Determine bridge controllers name
*/}}
{{- define "slurm-bridge.controllers.name" -}}
{{ printf "%s-controllers" (include "slurm-bridge.name" .) }}
{{- end }}

{{/*
Determine bridge controllers name for cluster-scoped objects, which two
releases of the chart would otherwise collide on.
*/}}
{{- define "slurm-bridge.controllers.fullname" -}}
{{ printf "%s-controllers" (include "slurm-bridge.fullname" .) }}
{{- end }}

{{/*
Determine bridge controllers image repository
*/}}
{{- define "slurm-bridge.controllers.image.repository" -}}
{{ .Values.controllers.image.repository | default (printf "ghcr.io/slinkyproject/%s" (include "slurm-bridge.controllers.name" .)) }}
{{- end }}

{{/*
Define bridge controllers image tag
*/}}
{{- define "slurm-bridge.controllers.image.tag" -}}
{{ .Values.controllers.image.tag | default .Chart.Version }}
{{- end }}

{{/*
Determine bridge controllers image reference (repo:tag)
*/}}
{{- define "slurm-bridge.controllers.imageRef" -}}
{{ printf "%s:%s" (include "slurm-bridge.controllers.image.repository" .) (include "slurm-bridge.controllers.image.tag" .) | quote }}
{{- end }}

{{/*
The controllers labels
*/}}
{{- define "slurm-bridge.controllers.labels" -}}
{{ include "slurm-bridge.labels" . }}
{{ include "slurm-bridge.controllers.selectorLabels" . }}
app.kubernetes.io/component: controllers
{{- end }}

{{/*
The controllers selector labels
*/}}
{{- define "slurm-bridge.controllers.selectorLabels" -}}
app.kubernetes.io/name: {{ include "slurm-bridge.controllers.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
