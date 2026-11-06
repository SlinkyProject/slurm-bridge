// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package structutils

import (
	"reflect"
	"testing"
)

func TestMergeList(t *testing.T) {
	tests := []struct {
		name string
		in   [][]string
		want []string
	}{
		{name: "no lists", in: nil, want: []string{}},
		{name: "empty lists", in: [][]string{{}, {}}, want: []string{}},
		{name: "single list", in: [][]string{{"a", "b"}}, want: []string{"a", "b"}},
		{name: "concatenates in order", in: [][]string{{"b"}, nil, {"a", "c"}}, want: []string{"b", "a", "c"}},
		{name: "keeps duplicates", in: [][]string{{"a"}, {"a"}}, want: []string{"a", "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MergeList(tt.in...); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MergeList() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSortedDedup(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "empty", in: []string{}, want: []string{}},
		{name: "nil", in: nil, want: []string{}},
		{name: "sorts", in: []string{"c", "a", "b"}, want: []string{"a", "b", "c"}},
		{name: "dedups", in: []string{"a", "a", "b"}, want: []string{"a", "b"}},
		{name: "trims whitespace", in: []string{" a ", "b\t"}, want: []string{"a", "b"}},
		{name: "drops empty and whitespace-only entries", in: []string{"a", "", "  "}, want: []string{"a"}},
		{name: "combined sort dedup trim", in: []string{" b ", "a", "b", ""}, want: []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SortedDedup(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SortedDedup() = %v, want %v", got, tt.want)
			}
		})
	}
}
