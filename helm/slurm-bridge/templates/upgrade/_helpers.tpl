{{- /*
SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
SPDX-License-Identifier: Apache-2.0
*/}}

{{- define "slurm-bridge.upgrade.container" -}}
image: {{ printf "%s:%s" .Values.upgradeHook.image.repository .Values.upgradeHook.image.tag | quote }}
imagePullPolicy: {{ .Values.upgradeHook.image.pullPolicy }}
command: [kubectl]
securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop: [ALL]
volumeMounts:
  - name: tmp
    mountPath: /tmp
{{- with .Values.upgradeHook.resources }}
resources: {{- toYaml . | nindent 2 }}
{{- end }}
{{- end }}
