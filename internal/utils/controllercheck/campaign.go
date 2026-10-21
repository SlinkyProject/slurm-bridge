// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

// Package controllercheck explores generated controller lifecycles against a
// shared contract. Environments supply their own state, actors, and cache models.
package controllercheck

import (
	"context"
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

// Rule is a requirement over authoritative state, independent of the actions
// used to reach that state. Check must be read-only.
type Rule[S any] struct {
	Name      string
	Statement string
	Check     func(S) error
}

type Contract[S any] struct {
	Always     []Rule[S]
	Eventually []Rule[S]
}

func checkRules[S any](rules []Rule[S], state S) error {
	for _, rule := range rules {
		if err := rule.Check(state); err != nil {
			return fmt.Errorf("%s: %s: %w", rule.Name, rule.Statement, err)
		}
	}
	return nil
}

// Campaign explores a whole controller environment. There is no distinguished
// reconcile key or cleanup phase: the environment owns lifecycle transitions,
// queues, clocks, caches, and the real controller(s) under test.
type Campaign[S any] struct {
	Snapshot func(context.Context) (S, error)
	// Actions draw parameters from current state. Skip before mutation when an
	// action is unavailable. Every shrinking attempt gets a fresh campaign.
	Actions map[string]func(*rapid.T) error
	// ObserveEffects installs a hook called after every committed controller
	// side effect. The hook records violations without changing controller flow.
	ObserveEffects func(func())
	// Recover stops disturbances and establishes the contract's assumptions.
	// Progress fairly schedules the actors relied on by Eventually.
	Recover       func(context.Context) error
	Progress      func(context.Context) error
	Restart       func(context.Context) error
	Retryable     func(error) bool
	RecoverySteps int
	Trace         *Trace
}

// Explore runs each rule as an independent Rapid property over the same whole-
// controller model. A failing property does not prevent the remaining subtests
// from running. Every generated history and shrinking attempt gets a fresh world.
// Use Go's -run flag to select a rule, and Rapid's flags for shrinking and replay.
func Explore[S any](t *testing.T, contract Contract[S], factory func(*rapid.T) Campaign[S]) {
	t.Helper()
	if len(contract.Always)+len(contract.Eventually) == 0 {
		t.Fatal("controller contract has no rules")
	}
	for _, group := range []struct {
		name  string
		rules []Rule[S]
	}{{"always", contract.Always}, {"eventually", contract.Eventually}} {
		seen := map[string]bool{}
		for _, rule := range group.rules {
			if rule.Name == "" || rule.Check == nil || seen[rule.Name] {
				t.Fatalf("invalid or duplicate %s rule %q", group.name, rule.Name)
			}
			seen[rule.Name] = true
			t.Run(group.name+"/"+rule.Name, func(t *testing.T) {
				selected := Contract[S]{Always: []Rule[S]{rule}}
				if group.name == "eventually" {
					selected = Contract[S]{Eventually: []Rule[S]{rule}}
				}
				exploreProperty(t, selected, contract.Eventually, factory)
			})
		}
	}
}

func exploreProperty[S any](t *testing.T, contract Contract[S], recovery []Rule[S], factory func(*rapid.T) Campaign[S]) {
	t.Helper()
	rapid.Check(t, func(rt *rapid.T) {
		c := factory(rt)
		err := runCampaign(rt.Context(), c, contract, recovery, func(check func() error) error {
			actions := make(map[string]func(*rapid.T), len(c.Actions)+1)
			for name, run := range c.Actions {
				actions[name] = func(rt *rapid.T) {
					c.Trace.Logf("action: %s", name)
					err := run(rt)
					if safetyErr := check(); safetyErr != nil {
						rt.Fatalf("%v\nhistory:\n%s", safetyErr, c.Trace)
					}
					if err != nil && !onlyExpected(err, c.Retryable) {
						rt.Fatalf("action %s: %v\nhistory:\n%s", name, err, c.Trace)
					}
				}
			}
			actions[""] = func(rt *rapid.T) {
				if err := check(); err != nil {
					rt.Fatalf("%v\nhistory:\n%s", err, c.Trace)
				}
			}
			rt.Repeat(actions)
			return nil
		})
		if err != nil {
			rt.Fatalf("%v\nhistory:\n%s", err, c.Trace)
		}
	})
}

func runCampaign[S any](ctx context.Context, c Campaign[S], contract Contract[S], recovery []Rule[S], exercise func(func() error) error) error {
	if c.Trace == nil || c.Snapshot == nil || c.ObserveEffects == nil || c.Recover == nil ||
		c.Progress == nil || c.Restart == nil || c.RecoverySteps < 1 ||
		len(contract.Always)+len(contract.Eventually) == 0 {
		return fmt.Errorf("incomplete controller campaign")
	}
	var firstViolation error
	check := func() error {
		if firstViolation == nil {
			state, err := c.Snapshot(ctx)
			if err != nil {
				firstViolation = err
			} else {
				firstViolation = checkRules(contract.Always, state)
			}
		}
		return firstViolation
	}
	c.ObserveEffects(func() { _ = check() })
	defer c.ObserveEffects(nil)
	if err := check(); err != nil {
		return err
	}
	if err := exercise(check); err != nil {
		return err
	}
	c.Trace.Logf("recovery: stop disturbances; establish fairness assumptions")
	if err := c.Recover(ctx); err != nil {
		return err
	}
	settled := func() error {
		state, err := c.Snapshot(ctx)
		if err != nil {
			return err
		}
		return checkRules(contract.Eventually, state)
	}
	stable := func() error {
		// Stability includes reconciliation before and after a new instance.
		for _, action := range []func(context.Context) error{c.Progress, c.Restart, c.Progress} {
			if err := action(ctx); err != nil {
				return err
			}
			if err := check(); err != nil {
				return err
			}
			if err := settled(); err != nil {
				return fmt.Errorf("settled state was not stable: %w", err)
			}
		}
		return nil
	}
	var pending error
	for i := range c.RecoverySteps {
		if err := check(); err != nil {
			return err
		}
		c.Trace.Logf("recovery tick: %d", i)
		if err := c.Progress(ctx); err != nil && !onlyExpected(err, c.Retryable) {
			return err
		}
		if err := check(); err != nil {
			return err
		}
		if len(contract.Eventually) == 0 {
			// The full contract's eventual outcomes identify a settled world.
			// They only bound recovery here: failure to settle cannot falsify
			// an independent safety property. Without a settled world, cover
			// the entire recovery bound before checking restart stability.
			if len(recovery) > 0 {
				state, err := c.Snapshot(ctx)
				if err != nil {
					return err
				}
				if checkRules(recovery, state) == nil {
					return stable()
				}
			}
			continue
		}
		pending = settled()
		if pending != nil {
			continue
		}
		return stable()
	}
	if len(contract.Eventually) == 0 {
		return stable()
	}
	return fmt.Errorf("did not converge in %d recovery ticks: %w", c.RecoverySteps, pending)
}
