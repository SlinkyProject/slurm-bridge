// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import (
	"fmt"

	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
)

// validateDeviceProfileSelector catches explicit contradictions between the
// structural driver prefilter and direct device.driver equality expressions.
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

	var validationErr error
	celast.PreOrderVisit(parsed.NativeRep().Expr(), celast.NewExprVisitor(func(expr celast.Expr) {
		if validationErr != nil {
			return
		}
		driver, ok := deviceDriverEquality(expr)
		if ok && driver != profile.Driver {
			validationErr = fmt.Errorf(
				"device profile %q selector constrains device.driver to %q, but configured driver is %q",
				profile.Name,
				driver,
				profile.Driver,
			)
		}
	}))
	return validationErr
}

func deviceDriverEquality(expr celast.Expr) (string, bool) {
	if expr.Kind() != celast.CallKind {
		return "", false
	}
	call := expr.AsCall()
	args := call.Args()
	if call.FunctionName() != operators.Equals || len(args) != 2 {
		return "", false
	}
	if isDeviceDriverReference(args[0]) {
		return stringLiteral(args[1])
	}
	if isDeviceDriverReference(args[1]) {
		return stringLiteral(args[0])
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
