{{- /*
SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/*
Name of the schedulerAdmission
*/}}
{{- define "slurm-bridge.admission.name" -}}
{{ printf "%s-admission" (include "slurm-bridge.name" .) }}
{{- end }}

{{/*
Determine bridge schedulerAdmission image repository
*/}}
{{- define "slurm-bridge.admission.image.repository" -}}
{{ .Values.admission.image.repository | default (printf "ghcr.io/slinkyproject/%s" (include "slurm-bridge.admission.name" .)) }}
{{- end }}

{{/*
Define bridge schedulerAdmission image tag
*/}}
{{- define "slurm-bridge.admission.image.tag" -}}
{{ .Values.admission.image.tag | default .Chart.Version }}
{{- end }}

{{/*
Determine bridge schedulerAdmission image reference (repo:tag)
*/}}
{{- define "slurm-bridge.admission.imageRef" -}}
{{ printf "%s:%s" (include "slurm-bridge.admission.image.repository" .) (include "slurm-bridge.admission.image.tag" .) | quote }}
{{- end }}

{{/*
The schedulerAdmission labels
*/}}
{{- define "slurm-bridge.admission.labels" -}}
{{ include "slurm-bridge.labels" . }}
{{ include "slurm-bridge.admission.selectorLabels" . }}
app.kubernetes.io/component: admission
{{- end }}

{{/*
The schedulerAdmission selector labels
*/}}
{{- define "slurm-bridge.admission.selectorLabels" -}}
app.kubernetes.io/name: {{ include "slurm-bridge.admission.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
