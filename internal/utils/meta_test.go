// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"testing"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
)

func TestNamespacedNameFromString(t *testing.T) {
	tests := []struct {
		name          string
		qualifiedName string
		want          types.NamespacedName
	}{
		{
			name:          "empty",
			qualifiedName: "",
			want:          types.NamespacedName{},
		},
		{
			name:          "qualified",
			qualifiedName: "foo/bar",
			want: types.NamespacedName{
				Namespace: "foo",
				Name:      "bar",
			},
		},
		{
			name:          "name",
			qualifiedName: "bar",
			want: types.NamespacedName{
				Name: "bar",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NamespacedNameFromString(tt.qualifiedName)
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("NamespacedNameFromString() = %v, want %v", got, tt.want)
			}
		})
	}
}
