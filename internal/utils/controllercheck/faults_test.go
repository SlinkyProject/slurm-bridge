// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package controllercheck

import (
	"errors"
	"fmt"
	"testing"
)

func TestFaultsCommitBoundary(t *testing.T) {
	for _, mode := range []FailureMode{BeforeCommit, AfterCommit} {
		t.Run(string(mode), func(t *testing.T) {
			f := &Faults{Trace: &Trace{}}
			commits := 0
			commit := func() error { commits++; return nil }
			f.Arm("write", mode)
			if err := f.Call("write", commit); !errors.Is(err, ErrInjected) {
				t.Fatalf("want injected failure, got %v", err)
			}
			want := 0
			if mode == AfterCommit {
				want = 1
			}
			if commits != want {
				t.Fatalf("commits = %d, want %d", commits, want)
			}
			if err := f.Call("write", commit); err != nil || commits != want+1 {
				t.Fatalf("one-shot failure was not consumed: commits=%d err=%v", commits, err)
			}
		})
	}
}

func TestControllerCheckDoesNotHideJoinedErrors(t *testing.T) {
	bug := errors.New("unexpected controller bug")
	err := fmt.Errorf("wrapped: %w", errors.Join(ErrInjected, bug))
	if onlyExpected(err, nil) {
		t.Fatal("an injected error hid an unrelated error in errors.Join")
	}
	if !onlyExpected(fmt.Errorf("wrapped: %w", errors.Join(ErrInjected, ErrInjected)), nil) {
		t.Fatal("joined injected failures should be retryable")
	}
}
