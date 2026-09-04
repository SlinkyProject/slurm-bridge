// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmconstraint

import (
	"errors"
	"strconv"
	"testing"
)

const feature = "slurm_bridge_gres_compatible"

func TestCompose(t *testing.T) {
	tests := []struct {
		constraints string
		want        string
		wantErr     bool
	}{
		{constraints: "", want: feature},
		{constraints: "  ", want: feature},
		{constraints: feature, want: feature},
		{constraints: "gpu", want: feature + "&gpu"},
		{constraints: "intel&gpu", want: feature + "&intel&gpu"},
		{constraints: "rack1|rack2", want: feature + "&(rack1|rack2)"},
		{constraints: "a|b|c", want: feature + "&(a|b|c)"},
		{constraints: "a&b|c", want: feature + "&(a&b|c)"},
		{constraints: "gpu&(rack-a|rack-b)", want: feature + "&gpu&(rack-a|rack-b)"},
		{constraints: "(rack-a|rack-b)&gpu", want: feature + "&(rack-a|rack-b)&gpu"},
		{constraints: "[rack1|rack2]", want: feature + "&[rack1|rack2]"},
		{constraints: "[rack1*2&rack2*4]", want: feature + "&[rack1*2&rack2*4]"},
		{constraints: "[(knl&snc4&flat)*4&haswell*1]", want: feature + "&[(knl&snc4&flat)*4&haswell*1]"},
		{constraints: "[rack1|rack2]&gpu", want: feature + "&[rack1|rack2]&gpu"},
		// Not composable under Slurm's grammar.
		{constraints: "graphics*4", wantErr: true},
		{constraints: "(a&b)|(c&d)", wantErr: true},
		{constraints: "(a|b)|c", wantErr: true},
		{constraints: "c|[a|b]", wantErr: true},
		// Already invalid.
		{constraints: "a&(b&(c|d))", wantErr: true},
		{constraints: "(a|[b|c])", wantErr: true},
		{constraints: "[a|b]&[c|d]", wantErr: true},
		{constraints: "a|b)", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.constraints, func(t *testing.T) {
			got, err := Compose(feature, tt.constraints)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Compose(%q) error = %v, wantErr %v", tt.constraints, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got != tt.want {
				t.Fatalf("Compose(%q) = %q, want %q", tt.constraints, got, tt.want)
			}
			if err := slurmValidate(got); err != nil {
				t.Fatalf("Compose(%q) = %q is rejected by Slurm's grammar: %v", tt.constraints, got, err)
			}
		})
	}
}

// slurmValidate is a test-only port of the syntax checks in slurmctld's
// _feature_string2list (src/slurmctld/job_scheduler.c). It reports the same
// conditions Slurm rejects with ESLURM_INVALID_FEATURE, without evaluating the
// expression. Keep it in sync with the upstream parser.
func slurmValidate(features string) error {
	var (
		feature      bool // a feature name is being accumulated
		count        int
		bracket      int
		paren        int
		brackSets    int
		hasAsterisk  bool
		featureCount int
	)
	for i := 0; i < len(features); i++ {
		c := features[i]
		switch c {
		case '*':
			j := i + 1
			for j < len(features) && features[j] >= '0' && features[j] <= '9' {
				j++
			}
			n, _ := strconv.Atoi(features[i+1 : j])
			if bracket == 0 {
				hasAsterisk = true
			}
			if !feature || n <= 0 || paren != 0 {
				return errors.New("'*' must be requested with a positive integer, and after a feature or parentheses")
			}
			count = n
			i = j - 1
		case '&', '|':
			if !feature {
				return errors.New("operator without a feature")
			}
			featureCount++
			feature = false
			count = 0
		case '[':
			if feature || bracket != 0 || paren != 0 {
				return errors.New("imbalanced brackets")
			}
			bracket++
			brackSets++
			if brackSets > 1 {
				return errors.New("more than one set of brackets")
			}
		case ']':
			if !feature || bracket == 0 || paren != 0 {
				return errors.New("imbalanced brackets")
			}
			bracket--
		case '(':
			if feature || paren != 0 {
				return errors.New("imbalanced parentheses")
			}
			paren++
		case ')':
			if !feature || paren == 0 {
				return errors.New("imbalanced parentheses")
			}
			paren--
		default:
			if !feature {
				feature = true
			} else if i > 0 && (features[i-1] == ')' || features[i-1] == ']') {
				return errors.New("unexpected character after ')' or ']'")
			}
		}
	}
	if feature {
		featureCount++
	}
	_ = count
	if bracket != 0 {
		return errors.New("unbalanced brackets")
	}
	if paren != 0 {
		return errors.New("unbalanced parenthesis")
	}
	if hasAsterisk && featureCount > 1 {
		return errors.New("'*' outside of brackets with more than one feature")
	}
	return nil
}

// TestSlurmValidate pins the port against expressions whose acceptance is
// known from the Slurm documentation and parser.
func TestSlurmValidate(t *testing.T) {
	valid := []string{"intel", "intel&gpu", "a|b", "foo&(bar|baz)", "[rack1|rack2]", "[rack1*2&rack2*4]",
		"[(knl&snc4&flat)*4&haswell*1]", "graphics*4", "f&[a|b]", "[a|b]&f", "f&x&(a|b)"}
	// A count applied to a parenthesised group is only valid inside brackets.
	invalid := []string{"f&a*2", "(a|b)*2", "f&(a&(b|c))", "f&([a|b])", "[a|b]&[c|d]", "&a", "(a|b)c", "a(b)"}
	for _, expr := range valid {
		if err := slurmValidate(expr); err != nil {
			t.Errorf("slurmValidate(%q) = %v, want accepted", expr, err)
		}
	}
	for _, expr := range invalid {
		if err := slurmValidate(expr); err == nil {
			t.Errorf("slurmValidate(%q) accepted, want rejected", expr)
		}
	}
}
