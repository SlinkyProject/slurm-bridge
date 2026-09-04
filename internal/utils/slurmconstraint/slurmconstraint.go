// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

// Package slurmconstraint composes Slurm job constraint (feature) expressions.
//
// Slurm's parser (slurmctld job_scheduler.c, _feature_string2list) imposes
// rules that make naive string wrapping unsafe:
//
//   - parentheses cannot nest;
//   - at most one set of square brackets is allowed, and never inside
//     parentheses;
//   - a "*count" outside brackets is only valid when it is the sole feature;
//   - without parentheses, operators are evaluated strictly left to right.
package slurmconstraint

import (
	"fmt"
	"strings"
)

// Compose returns a constraint expression that requires the feature required
// and preserves the meaning of the user-supplied expression constraints.
//
// It returns an error for the few expressions that cannot be combined with an
// additional feature under Slurm's grammar. Compose does not fully validate
// constraints; Slurm remains the authority on whether the result is accepted.
func Compose(required, constraints string) (string, error) {
	constraints = strings.TrimSpace(constraints)
	if constraints == "" || constraints == required {
		return required, nil
	}

	shape, err := analyze(constraints)
	if err != nil {
		return "", err
	}

	switch {
	case shape.countOutsideBrackets:
		return "", fmt.Errorf("constraint %q: a feature count outside square brackets cannot be combined with the required feature %q; move the count into brackets, for example %q",
			constraints, required, "["+required+"*N&"+constraints+"]")
	case !shape.topLevelOr:
		// f&X is exact when X has no top-level OR: every top-level operator
		// is AND, and OR only appears inside parentheses or brackets.
		return required + "&" + constraints, nil
	case shape.hasParens || shape.hasBrackets:
		return "", fmt.Errorf("constraint %q: an OR outside parentheses cannot be combined with the required feature %q when the expression also uses parentheses or brackets, because Slurm does not allow nested grouping; rewrite it with a single level of grouping, for example %q",
			constraints, required, "(a&b)|c -> [a&b|c] or a&(b|c)")
	default:
		// A flat expression with a top-level OR must be grouped so that the
		// required feature applies to the whole expression.
		return required + "&(" + constraints + ")", nil
	}
}

type expressionShape struct {
	topLevelOr           bool
	hasParens            bool
	hasBrackets          bool
	countOutsideBrackets bool
}

func analyze(constraints string) (expressionShape, error) {
	var shape expressionShape
	paren, bracket := 0, 0
	for i := 0; i < len(constraints); i++ {
		switch constraints[i] {
		case '(':
			shape.hasParens = true
			paren++
			if paren > 1 {
				return shape, fmt.Errorf("constraint %q: Slurm does not allow nested parentheses", constraints)
			}
		case ')':
			paren--
			if paren < 0 {
				return shape, fmt.Errorf("constraint %q: unbalanced parentheses", constraints)
			}
		case '[':
			if paren > 0 {
				return shape, fmt.Errorf("constraint %q: Slurm does not allow brackets inside parentheses", constraints)
			}
			if shape.hasBrackets {
				return shape, fmt.Errorf("constraint %q: Slurm allows only one set of brackets", constraints)
			}
			shape.hasBrackets = true
			bracket++
		case ']':
			bracket--
			if bracket < 0 {
				return shape, fmt.Errorf("constraint %q: unbalanced brackets", constraints)
			}
		case '|':
			if paren == 0 && bracket == 0 {
				shape.topLevelOr = true
			}
		case '*':
			if bracket == 0 {
				shape.countOutsideBrackets = true
			}
		}
	}
	if paren != 0 {
		return shape, fmt.Errorf("constraint %q: unbalanced parentheses", constraints)
	}
	if bracket != 0 {
		return shape, fmt.Errorf("constraint %q: unbalanced brackets", constraints)
	}
	return shape, nil
}
