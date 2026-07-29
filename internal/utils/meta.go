// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"strings"

	"k8s.io/apimachinery/pkg/types"
)

func NamespacedNameFromString(qualifiedName string) types.NamespacedName {
	parts := strings.Split(qualifiedName, "/")
	if len(parts) != 2 {
		return types.NamespacedName{
			Name: parts[0],
		}
	}
	return types.NamespacedName{
		Namespace: parts[0],
		Name:      parts[1],
	}
}
