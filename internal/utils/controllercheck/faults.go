// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package controllercheck

import (
	"errors"
	"fmt"
)

var ErrInjected = errors.New("injected transport failure")

type FailureMode string

const (
	BeforeCommit FailureMode = "before-commit"
	AfterCommit  FailureMode = "after-commit"
)

// Faults intercepts named side effects. Each armed fault fires once, on the next
// matching operation. It is deliberately sequential, like the campaign runner.
// The same mechanism can wrap Kubernetes, Slurm, or another external API.
type Faults struct {
	Trace   *Trace
	pending map[string]FailureMode
}

func (f *Faults) Arm(operation string, mode FailureMode) {
	if f.pending == nil {
		f.pending = make(map[string]FailureMode)
	}
	f.pending[operation] = mode
}

func (f *Faults) Heal() { clear(f.pending) }

func (f *Faults) Call(operation string, commit func() error) error {
	mode := f.pending[operation]
	delete(f.pending, operation)
	if mode == BeforeCommit {
		f.Trace.Logf("effect: %s failed before commit", operation)
		return fmt.Errorf("%s before commit: %w", operation, ErrInjected)
	}
	if err := commit(); err != nil {
		f.Trace.Logf("effect: %s failed: %v", operation, err)
		return err
	}
	f.Trace.Logf("effect: %s committed", operation)
	if mode == AfterCommit {
		f.Trace.Logf("effect: %s response lost", operation)
		return fmt.Errorf("%s response lost: %w", operation, ErrInjected)
	}
	return nil
}

// An errors.Join containing an injected error AND a real error must still fail.
func onlyExpected(err error, retryable func(error) bool) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if !onlyExpected(child, retryable) {
				return false
			}
		}
		return true
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return onlyExpected(wrapped, retryable)
	}
	return errors.Is(err, ErrInjected) || (retryable != nil && retryable(err))
}
