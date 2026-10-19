// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/SlinkyProject/slurm-bridge/internal/controller/node/slurmcontrol"
	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

type drainingSocketControl struct {
	slurmcontrol.SlurmControlInterface
	drains, additions, undrains, disabled, removals int
	external                                        bool
	registrationErr                                 error
	drainErr                                        error
	disableErr                                      error
	drained                                         bool
	drainedErr                                      error
	removeErr                                       error
}

func (c *drainingSocketControl) NodeExists(context.Context, *corev1.Node) (bool, error) {
	return true, nil
}
func (c *drainingSocketControl) GetNodeNames(context.Context) ([]string, error) {
	return []string{"worker"}, nil
}
func (c *drainingSocketControl) NodeNeedsRecreate(context.Context, *corev1.Node, *nodeinfo.NodeInfo, []dra.GRESInventory) (bool, error) {
	return true, c.registrationErr
}
func (c *drainingSocketControl) IsNodeExternal(context.Context, *corev1.Node) (bool, error) {
	return c.external, nil
}
func (c *drainingSocketControl) DisableNodeGRESCompatibility(context.Context, *corev1.Node) error {
	c.disabled++
	return c.disableErr
}
func (c *drainingSocketControl) MakeNodeDrain(context.Context, *corev1.Node, string) error {
	c.drains++
	return c.drainErr
}
func (c *drainingSocketControl) IsNodeDrained(context.Context, *corev1.Node) (bool, error) {
	return c.drained, c.drainedErr
}
func (c *drainingSocketControl) RemoveNode(context.Context, *corev1.Node) error {
	c.removals++
	return c.removeErr
}
func (c *drainingSocketControl) MakeNodeUndrain(context.Context, *corev1.Node, string) error {
	c.undrains++
	return nil
}
func (c *drainingSocketControl) AddNode(context.Context, *corev1.Node, *nodeinfo.NodeInfo, []dra.GRESInventory) error {
	c.additions++
	return nil
}

func (c *drainingSocketControl) UpdateHybridNode(context.Context, *corev1.Node, *nodeinfo.NodeInfo, []dra.GRESInventory) error {
	return c.registrationErr
}

func TestSyncStateAfterRegistration(t *testing.T) {
	registrationErr := errors.New("CPU topology changed; drain before re-registering")
	drainErr := errors.New("Slurm drain failed")
	for _, tc := range []struct {
		name                      string
		labeled, cordoned         bool
		registrationErr, drainErr error
		wantDrains, wantUndrains  int
	}{
		{name: "cordoned hybrid with invalid inventory", cordoned: true, registrationErr: registrationErr, wantDrains: 1},
		{name: "cordoned labeled hybrid with invalid inventory", labeled: true, cordoned: true, registrationErr: registrationErr, wantDrains: 1},
		{name: "uncordoned hybrid with invalid inventory", registrationErr: registrationErr},
		{name: "uncordoned labeled hybrid with invalid inventory", labeled: true, registrationErr: registrationErr},
		{name: "cordoned hybrid with valid inventory", cordoned: true, wantDrains: 1},
		{name: "uncordoned hybrid with valid inventory", wantUndrains: 1},
		{name: "registration and drain both fail", cordoned: true, registrationErr: registrationErr, drainErr: drainErr, wantDrains: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "worker"},
				Spec:       corev1.NodeSpec{Unschedulable: tc.cordoned},
			}
			if tc.labeled {
				node.Labels = map[string]string{wellknown.LabelExternalNode: "true"}
			}
			control := &drainingSocketControl{registrationErr: tc.registrationErr, drainErr: tc.drainErr}
			registry, err := dra.NewRegistry(nil)
			if err != nil {
				t.Fatal(err)
			}
			r := &NodeReconciler{Client: fake.NewFakeClient(node), slurmControl: control, draRegistry: registry, SchedulerName: "slurm-bridge-scheduler"}
			err = r.Sync(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(node)})
			if tc.registrationErr == nil && tc.drainErr == nil && err != nil {
				t.Fatal(err)
			}
			for _, wantErr := range []error{tc.registrationErr, tc.drainErr} {
				if wantErr != nil && !errors.Is(err, wantErr) {
					t.Errorf("Sync() error = %v, want %v", err, wantErr)
				}
			}
			if control.drains != tc.wantDrains || control.undrains != tc.wantUndrains {
				t.Fatalf("drain=%d undrain=%d, want drain=%d undrain=%d", control.drains, control.undrains, tc.wantDrains, tc.wantUndrains)
			}
		})
	}
}

func TestDrainWaitRequeuesWithoutError(t *testing.T) {
	for _, tc := range []struct {
		name          string
		labeled       bool
		wantAdditions int
	}{
		{name: "inventory replacement", labeled: true, wantAdditions: 1},
		{name: "external node removal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
			if tc.labeled {
				node.Labels = map[string]string{wellknown.LabelExternalNode: "true"}
			}
			control := &drainingSocketControl{external: true}
			registry, err := dra.NewRegistry(nil)
			if err != nil {
				t.Fatal(err)
			}
			r := &NodeReconciler{Client: fake.NewFakeClient(node), slurmControl: control, draRegistry: registry, SchedulerName: "slurm-bridge-scheduler"}
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(node)}
			result, err := r.Reconcile(ctx, req)
			if err != nil || result.RequeueAfter != 30*time.Second {
				t.Fatalf("expected a 30-second retry without error, got %+v, %v", result, err)
			}
			if control.drains != 1 || control.undrains != 0 || control.additions != 0 || control.removals != 0 {
				t.Fatalf("while draining: drain=%d undrain=%d add=%d remove=%d", control.drains, control.undrains, control.additions, control.removals)
			}

			control.drained = true
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if control.removals != 1 || control.additions != tc.wantAdditions || control.undrains != 0 {
				t.Fatalf("after draining: remove=%d add=%d undrain=%d", control.removals, control.additions, control.undrains)
			}
		})
	}
}

func TestDrainRemovalErrorsPropagate(t *testing.T) {
	failure := errors.New("Slurm unavailable")
	for _, tc := range []struct {
		name                            string
		drainErr, drainedErr, removeErr error
	}{
		{name: "drain", drainErr: failure},
		{name: "drain status", drainedErr: failure},
		{name: "removal", removeErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Labels: map[string]string{wellknown.LabelExternalNode: "true"}}}
			control := &drainingSocketControl{drained: true, drainErr: tc.drainErr, drainedErr: tc.drainedErr, removeErr: tc.removeErr}
			registry, err := dra.NewRegistry(nil)
			if err != nil {
				t.Fatal(err)
			}
			r := &NodeReconciler{Client: fake.NewFakeClient(node), slurmControl: control, draRegistry: registry}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(node)})
			if !errors.Is(err, failure) {
				t.Fatalf("expected Slurm error, got %v", err)
			}
			if control.additions != 0 || control.undrains != 0 {
				t.Fatalf("add=%d undrain=%d after Slurm failure", control.additions, control.undrains)
			}
		})
	}
}

func TestRegistrationFailureRevokesEligibilityWithoutDraining(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, missingRegistry := range []bool{false, true} {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Labels: map[string]string{wellknown.LabelExternalNode: "true"}}}
			registrationErr := errors.New("applied CPU inventory disappeared")
			control := &drainingSocketControl{external: external, registrationErr: registrationErr}
			registry, err := dra.NewRegistry(nil)
			if err != nil {
				t.Fatal(err)
			}
			if missingRegistry {
				registry = nil
			}
			r := &NodeReconciler{Client: fake.NewFakeClient(node), slurmControl: control, draRegistry: registry, SchedulerName: "slurm-bridge-scheduler"}
			err = r.Sync(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(node)})
			if err == nil || !missingRegistry && !errors.Is(err, registrationErr) {
				t.Fatalf("expected registration failure, got %v", err)
			}
			if control.drains != 0 || control.disabled != 1 || control.undrains != 0 || control.additions != 0 {
				t.Fatalf("external=%v missingRegistry=%v: drain=%d disabled=%d undrain=%d add=%d", external, missingRegistry, control.drains, control.disabled, control.undrains, control.additions)
			}
		}
	}
}

func TestRegistrationFailurePreservesRevocationError(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", Labels: map[string]string{wellknown.LabelExternalNode: "true"}}}
	registrationErr := errors.New("applied CPU inventory disappeared")
	disableErr := errors.New("feature update failed")
	control := &drainingSocketControl{external: true, registrationErr: registrationErr, disableErr: disableErr}
	registry, err := dra.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &NodeReconciler{Client: fake.NewFakeClient(node), slurmControl: control, draRegistry: registry}
	err = r.Sync(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(node)})
	if !errors.Is(err, registrationErr) || !errors.Is(err, disableErr) {
		t.Fatalf("expected registration and feature update errors, got %v", err)
	}
	if control.drains != 0 || control.disabled != 1 || control.undrains != 0 || control.additions != 0 {
		t.Fatalf("drain=%d disabled=%d undrain=%d add=%d", control.drains, control.disabled, control.undrains, control.additions)
	}
}
