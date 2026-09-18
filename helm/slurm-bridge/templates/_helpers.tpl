{{- /*
SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
SPDX-License-Identifier: Apache-2.0
*/}}

{{/*
Expand the name of the chart.
*/}}
{{- define "slurm-bridge.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "slurm-bridge.fullname" -}}
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
{{- define "slurm-bridge.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Allow the release namespace to be overridden
*/}}
{{- define "slurm-bridge.namespace" -}}
{{ default .Release.Namespace .Values.namespaceOverride }}
{{- end }}

{{/*
Define the Slurm JWT secret ref name
*/}}
{{- define "slurm-bridge.slurmJwtSecret.name" -}}
{{- $secret := .Values.sharedConfig.slurmJwtSecret | default dict -}}
{{- if kindIs "string" $secret }}
{{- $secret }}
{{- else if $secret.name }}
{{- $secret.name }}
{{- else }}
{{- printf "slurm-bridge-token" -}}
{{- end }}
{{- end }}

{{/*
Define the Slurm JWT secret ref key
*/}}
{{- define "slurm-bridge.slurmJwtSecret.key" -}}
{{- $secret := .Values.sharedConfig.slurmJwtSecret | default dict -}}
{{- if kindIs "string" $secret }}
{{- printf "SLURM_JWT" -}}
{{- else if $secret.key }}
{{- $secret.key }}
{{- else }}
{{- printf "SLURM_JWT" -}}
{{- end }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "slurm-bridge.labels" -}}
helm.sh/chart: {{ include "slurm-bridge.chart" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
