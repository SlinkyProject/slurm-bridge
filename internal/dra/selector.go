// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"fmt"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
)

// validateDeviceProfileSelector catches explicit contradictions between the
// structural driver prefilter and required device.driver equality or singleton
// membership constraints. Only direct constraints and conjunctions are checked.
// It deliberately does not try to prove what an arbitrary CEL expression
// implies about the driver.
func validateDeviceProfileSelector(profile DeviceProfile) error {
	compiled := deviceProfileCELCache.Check(profile.Selector)
	if compiled.Error != nil {
		return fmt.Errorf("compile selector for device profile %q: %w", profile.Name, compiled.Error)
	}

	parsed, issues := compiled.Environment.Parse(profile.Selector)
	if issues != nil {
		return fmt.Errorf("parse selector for device profile %q: %s", profile.Name, issues.String())
	}

	return validateDeviceProfileDriverConstraint(profile, parsed.NativeRep().Expr())
}

func validateDeviceProfileDriverConstraint(profile DeviceProfile, expr celast.Expr) error {
	if expr.Kind() == celast.CallKind && expr.AsCall().FunctionName() == operators.LogicalAnd {
		for _, arg := range expr.AsCall().Args() {
			if err := validateDeviceProfileDriverConstraint(profile, arg); err != nil {
				return err
			}
		}
		return nil
	}
	if driver, ok := deviceDriverConstraint(expr); ok && driver != profile.Driver {
		return fmt.Errorf(
			"device profile %q selector constrains device.driver to %q, but configured driver is %q",
			profile.Name,
			driver,
			profile.Driver,
		)
	}
	return nil
}

func deviceDriverConstraint(expr celast.Expr) (string, bool) {
	if expr.Kind() != celast.CallKind {
		return "", false
	}
	call := expr.AsCall()
	args := call.Args()
	if len(args) != 2 {
		return "", false
	}
	switch call.FunctionName() {
	case operators.Equals:
		if isDeviceDriverReference(args[0]) {
			return stringLiteral(args[1])
		}
		if isDeviceDriverReference(args[1]) {
			return stringLiteral(args[0])
		}
	case operators.In:
		if isDeviceDriverReference(args[0]) && args[1].Kind() == celast.ListKind {
			elements := args[1].AsList().Elements()
			if len(elements) == 1 {
				return stringLiteral(elements[0])
			}
		}
	}
	return "", false
}

func stringLiteral(expr celast.Expr) (string, bool) {
	if expr.Kind() != celast.LiteralKind {
		return "", false
	}
	value, ok := expr.AsLiteral().Value().(string)
	return value, ok
}

func isDeviceDriverReference(expr celast.Expr) bool {
	if expr.Kind() != celast.SelectKind {
		return false
	}
	selection := expr.AsSelect()
	operand := selection.Operand()
	return selection.FieldName() == "driver" && operand.Kind() == celast.IdentKind && operand.AsIdent() == "device"
}
