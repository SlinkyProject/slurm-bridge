#!/bin/bash
# SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

BACKUP_DIR="/tmp/admission-config-backup"
VALIDATING_BACKUP="${BACKUP_DIR}/validating.yaml"
MUTATING_BACKUP="${BACKUP_DIR}/mutating.yaml"

# Validate ValidatingWebhook backup exists
if [ -f ${VALIDATING_BACKUP} ]; then
	# Revert ValidatingWebhookConfiguration: restore clientConfig and timeoutSeconds
	# Uses yq to extract original clientConfig from backup and build the patch
	CLIENT_CONFIG=$(cat "${VALIDATING_BACKUP}" | yq -o=json ".webhooks[0].clientConfig")
	kubectl patch validatingwebhookconfiguration slurm-bridge-admission --type='json' -p="[
	{\"op\": \"replace\", \"path\": \"/webhooks/0/clientConfig\", \"value\": ${CLIENT_CONFIG}},
	{\"op\": \"replace\", \"path\": \"/webhooks/0/timeoutSeconds\", \"value\": 10}
	]"
	rm -vf "${VALIDATING_BACKUP}"
fi

# Validate MutatingWebhook backup exists
if [ -f ${MUTATING_BACKUP} ]; then
	# Revert MutatingWebhookConfiguration: restore clientConfig and timeoutSeconds
	CLIENT_CONFIG=$(cat "${MUTATING_BACKUP}" | yq -o=json ".webhooks[0].clientConfig")
	kubectl patch mutatingwebhookconfiguration slurm-bridge-admission --type='json' -p="[
	{\"op\": \"replace\", \"path\": \"/webhooks/0/clientConfig\", \"value\": ${CLIENT_CONFIG}},
	{\"op\": \"replace\", \"path\": \"/webhooks/0/timeoutSeconds\", \"value\": 10}
	]"
	rm -vf "${MUTATING_BACKUP}"
fi
