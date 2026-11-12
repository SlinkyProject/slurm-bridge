// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type fieldPatchOptions struct{ uid *types.UID }

// FieldPatchOption configures preconditions for a field patch.
type FieldPatchOption func(*fieldPatchOptions)

// WithUIDPrecondition requires the object to retain this UID. The check and
// field update execute atomically in one PATCH, without resource-version locking.
func WithUIDPrecondition(uid types.UID) FieldPatchOption {
	return func(opts *fieldPatchOptions) { opts.uid = &uid }
}

// FieldPatch adds or replaces the field at path. Path is a JSON Pointer whose
// parent must exist. Without options, the patch has no identity precondition.
func FieldPatch(path string, value any, options ...FieldPatchOption) (client.Patch, error) {
	opts := fieldPatchOptions{}
	for _, option := range options {
		option(&opts)
	}
	type operation struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}
	var operations []operation
	if opts.uid != nil {
		if *opts.uid == "" {
			return nil, fmt.Errorf("UID is required for a conditional field patch")
		}
		operations = append(operations, operation{Op: "test", Path: "/metadata/uid", Value: *opts.uid})
	}
	operations = append(operations, operation{Op: "add", Path: path, Value: value})
	data, err := json.Marshal(operations)
	if err != nil {
		return nil, fmt.Errorf("encode field patch: %w", err)
	}
	return client.RawPatch(types.JSONPatchType, data), nil
}
