// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package controllercheck

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"pgregory.net/rapid"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Establish that a rule is checked at the commit boundary, even when the action
// repairs the state afterwards. Merely checking the final snapshot misses this.
func TestControllerCheckCampaignCommitSafety(t *testing.T) {
	state := 0
	var observer func()
	c := counterCampaign(&state, &observer)
	err := runCampaign(t.Context(), c, counterContract(), nil, func(check func() error) error {
		state = -1
		observer()
		state = 0
		return check()
	})
	if err == nil || !strings.Contains(err.Error(), "nonnegative") {
		t.Fatalf("temporary unsafe commit was missed: %v", err)
	}
}

func TestControllerCheckCampaignRecoveryAndRestart(t *testing.T) {
	for _, stuck := range []bool{false, true} {
		t.Run(fmt.Sprintf("stuck=%t", stuck), func(t *testing.T) {
			state, restarts := 3, 0
			var observer func()
			c := counterCampaign(&state, &observer)
			c.Progress = func(context.Context) error {
				if !stuck && state > 0 {
					state--
				}
				observer()
				return nil
			}
			c.Restart = func(context.Context) error { restarts++; return nil }
			err := runCampaign(t.Context(), c, counterContract(), nil, func(check func() error) error { return check() })
			if stuck {
				if err == nil || !strings.Contains(err.Error(), "drained") {
					t.Fatalf("lost progress was missed: %v", err)
				}
			} else if err != nil || restarts != 1 {
				t.Fatalf("recovery/stability failed: restarts=%d err=%v", restarts, err)
			}
		})
	}
}

func counterContract() Contract[int] {
	return Contract[int]{
		Always: []Rule[int]{{Name: "nonnegative", Statement: "counter cannot go negative", Check: func(n int) error {
			if n < 0 {
				return fmt.Errorf("counter=%d", n)
			}
			return nil
		}}},
		Eventually: []Rule[int]{{Name: "drained", Statement: "work finishes", Check: func(n int) error {
			if n != 0 {
				return fmt.Errorf("counter=%d", n)
			}
			return nil
		}}},
	}
}

func counterCampaign(state *int, observer *func()) Campaign[int] {
	return Campaign[int]{
		Snapshot:       func(context.Context) (int, error) { return *state, nil },
		ObserveEffects: func(f func()) { *observer = f },
		Recover:        func(context.Context) error { return nil },
		Progress:       func(context.Context) error { return nil },
		Restart:        func(context.Context) error { return nil },
		RecoverySteps:  4, Trace: &Trace{},
	}
}

func TestControllerCheckSafetyCoversRecoveryBound(t *testing.T) {
	state, ticks := 1, 0
	var observer func()
	c := counterCampaign(&state, &observer)
	c.Progress = func(context.Context) error {
		ticks++
		if ticks == c.RecoverySteps {
			state = -1
			observer()
		}
		return nil
	}
	contract := counterContract()
	recovery := contract.Eventually
	contract.Eventually = nil
	err := runCampaign(t.Context(), c, contract, recovery, func(check func() error) error { return check() })
	if err == nil || !strings.Contains(err.Error(), "nonnegative") || ticks != c.RecoverySteps {
		t.Fatalf("late recovery violation was missed: ticks=%d err=%v", ticks, err)
	}
}

func TestControllerCheckSafetyAfterConvergence(t *testing.T) {
	state, ticks := 1, 0
	var observer func()
	c := counterCampaign(&state, &observer)
	c.Progress = func(context.Context) error {
		ticks++
		state = 0
		observer()
		return nil
	}
	c.Restart = func(context.Context) error {
		state = -1
		observer()
		return nil
	}
	contract := counterContract()
	recovery := contract.Eventually
	contract.Eventually = nil
	err := runCampaign(t.Context(), c, contract, recovery, func(check func() error) error { return check() })
	if err == nil || !strings.Contains(err.Error(), "nonnegative") || ticks != 2 {
		t.Fatalf("settled world did not check restart safety: ticks=%d err=%v", ticks, err)
	}
}

// Exercise actual testing.T/Rapid failure behavior in a child process so expected
// failures cannot mark this test suite failed. This is a harness test, not a Pod
// scenario or the implementation of Explore.
func TestControllerCheckExploreContinues(t *testing.T) {
	const child = "CONTROLLERCHECK_TEST_CONTINUATION"
	if os.Getenv(child) == "1" {
		contract := counterContract()
		contract.Always = append(contract.Always, Rule[int]{
			Name: "unchanged", Statement: "counter stays at minus one",
			Check: func(n int) error {
				if n != -1 {
					return fmt.Errorf("counter=%d", n)
				}
				return nil
			},
		})
		Explore(t, contract, func(*rapid.T) Campaign[int] {
			state := -1
			var observer func()
			return counterCampaign(&state, &observer)
		})
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControllerCheckExploreContinues$", //nolint:gosec // Re-exec this test binary with fixed arguments.
		"-test.v", "-test.count=1", "-rapid.checks=1", "-rapid.seed=1", "-rapid.shrinktime=0", "-rapid.nofailfile")
	cmd.Env = append(os.Environ(), child+"=1")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("expected assertion failure, got %v\n%s", err, output)
	}
	for _, want := range []string{
		"--- FAIL: TestControllerCheckExploreContinues/always/nonnegative",
		"--- PASS: TestControllerCheckExploreContinues/always/unchanged",
		"--- FAIL: TestControllerCheckExploreContinues/eventually/drained",
		"did not converge in 4 recovery ticks",
	} {
		if !strings.Contains(string(output), want) {
			t.Errorf("missing %q in child output:\n%s", want, output)
		}
	}
}

func TestControllerCheckQueueDeadlines(t *testing.T) {
	q := NewQueue(func(a, b reconcile.Request) int { return cmp.Compare(a.String(), b.String()) })
	a := reconcile.Request{NamespacedName: types.NamespacedName{Name: "a"}}
	b := reconcile.Request{NamespacedName: types.NamespacedName{Name: "b"}}
	q.AddAfter(a, 30*time.Second)
	q.AddAfter(a, 60*time.Second) // A duplicate must not postpone existing work.
	q.Now = 29 * time.Second
	if q.Len() != 0 {
		t.Fatal("delayed work ran early")
	}
	q.Now = 30 * time.Second
	q.Add(b)
	if ready := q.Ready(); len(ready) != 2 || ready[0] != a {
		t.Fatalf("ready=%v", ready)
	}
	got, _ := q.Get()
	if got != a {
		t.Fatalf("got=%v", got)
	}
	q.AddRateLimited(a)
	if q.Len() != 1 || q.NumRequeues(a) != 1 {
		t.Fatal("failed work did not wait for retry")
	}
	q.Now += time.Second
	if q.Len() != 2 {
		t.Fatal("retry was lost")
	}
	q.Forget(a)
	if q.NumRequeues(a) != 0 {
		t.Fatal("retry state survived success")
	}
}
