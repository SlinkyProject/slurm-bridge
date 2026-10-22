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
Scheduler name for cluster-scoped objects, which two releases of the chart
would otherwise collide on. Deliberately independent of the scheduler name
above, which names a scheduler rather than a release.
*/}}
{{- define "slurm-bridge.scheduler.fullname" -}}
{{ printf "%s-scheduler" (include "slurm-bridge.fullname" .) }}
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

{{/*
Determine whether the Kubernetes version supports built-in Workload APIs
*/}}
{{- define "slurm-bridge.scheduler.supportsWorkloadAPI" -}}
{{- semverCompare ">=1.36-0" .Capabilities.KubeVersion.Version -}}
{{- end }}

{{/*
Use the same default-on native Workload gate for the scheduler and controllers.
*/}}
{{- define "slurm-bridge.genericWorkload.enabled" -}}
{{- $gates := .Values.scheduler.featureGates | default dict -}}
{{- if hasKey $gates "GenericWorkload" -}}
{{- $gates.GenericWorkload -}}
{{- else -}}
true
{{- end -}}
{{- end }}

{{/*
List compatible scheduler feature gates. The 1.36 API adapter supports ordinary
PodGroups; hierarchy gates require the 1.37 APIs and native scheduling enabled.
*/}}
{{- define "slurm-bridge.scheduler.featureGates" -}}
{{- $featureGates := list }}
{{- $gates := deepCopy (.Values.scheduler.featureGates | default dict) }}
{{- $enabled := eq (include "slurm-bridge.genericWorkload.enabled" .) "true" }}
{{- $_ := set $gates "GenericWorkload" $enabled }}
{{- $hierarchyGates := list "CompositePodGroup" "TopologyAwareWorkloadScheduling" }}
{{- $supportsHierarchy := and $enabled (semverCompare ">=1.37-0" .Capabilities.KubeVersion.Version) }}
{{- range $name, $value := $gates }}
    {{- if or $supportsHierarchy (not (has $name $hierarchyGates)) }}
    {{- $featureGates = append $featureGates (printf "%s=%t" $name $value) }}
    {{- end }}
{{- end }}
{{- join "," $featureGates }}
{{- end }}
