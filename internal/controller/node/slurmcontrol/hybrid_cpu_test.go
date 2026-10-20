// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestHybridCPURegistration(t *testing.T) {
	slice := testNodeCPUResourceSlice("worker")
	info := testNodeInfoFromResourceSlices(t, "worker", []resourcev1.ResourceSlice{*slice})
	gres, err := dra.EncodeAppliedInventory(testExampleDRAInventory())
	if err != nil {
		t.Fatal(err)
	}
	applied, err := info.EncodeApplied(gres)
	if err != nil {
		t.Fatal(err)
	}
	moved := slice.DeepCopy()
	moved.Spec.Devices[0].Attributes[nodeinfo.DraDriverCpu_CPUIDs] = resourcev1.DeviceAttribute{StringValue: ptr.To("4-7")}
	moved.Spec.Devices[0].Attributes[nodeinfo.DraDriverCpu_SMTLayout] = resourcev1.DeviceAttribute{StringValue: ptr.To("4-7>1")}
	movedInfo := testNodeInfoFromResourceSlices(t, "worker", []resourcev1.ResourceSlice{*moved})
	multi := slice.DeepCopy()
	second := moved.Spec.Devices[0].DeepCopy()
	second.Name = "cpu-socket1"
	second.Attributes[nodeinfo.DraDriverCpu_SocketID] = resourcev1.DeviceAttribute{IntValue: ptr.To[int64](1)}
	multi.Spec.Devices = append(multi.Spec.Devices, *second)
	multiInfo := testNodeInfoFromResourceSlices(t, "worker", []resourcev1.ResourceSlice{*multi})

	for _, tc := range []struct {
		name       string
		extra      string
		info       *nodeinfo.NodeInfo
		state      []api.V0044NodeState
		changeNode func(*api.V0044Node)
		wantErr    bool
	}{
		{name: "first registration", info: info},
		{name: "multiple sockets", info: multiInfo, changeNode: func(n *api.V0044Node) { n.Sockets = ptr.To[int32](2); n.Cpus = ptr.To[int32](8) }},
		{name: "adds CPU fingerprint to GRES registration", info: info, extra: gres},
		{name: "matching map while jobs run", info: info, extra: applied, state: []api.V0044NodeState{api.V0044NodeStateMIXED}},
		{name: "first registration waits for idle", info: info, state: []api.V0044NodeState{api.V0044NodeStateALLOCATED}, wantErr: true},
		{name: "idle flag with allocated CPUs", info: info, changeNode: func(n *api.V0044Node) { n.AllocCpus = ptr.To[int32](1) }, wantErr: true},
		{name: "same geometry changed CPU IDs needs drain", info: movedInfo, extra: applied, wantErr: true},
		{name: "changed map on drained idle node", info: movedInfo, extra: applied, state: []api.V0044NodeState{api.V0044NodeStateIDLE, api.V0044NodeStateDRAIN}},
		{name: "changed map still draining", info: movedInfo, extra: applied, state: []api.V0044NodeState{api.V0044NodeStateMIXED, api.V0044NodeStateDRAIN}, wantErr: true},
		{name: "changed map completing", info: movedInfo, extra: applied, state: []api.V0044NodeState{api.V0044NodeStateIDLE, api.V0044NodeStateDRAIN, api.V0044NodeStateCOMPLETING}, wantErr: true},
		{name: "changed map undraining", info: movedInfo, extra: applied, state: []api.V0044NodeState{api.V0044NodeStateIDLE, api.V0044NodeStateDRAIN, api.V0044NodeStateUNDRAIN}, wantErr: true},
		{name: "socket count mismatch", info: info, changeNode: func(n *api.V0044Node) { n.Sockets = ptr.To[int32](2) }, wantErr: true},
		{name: "same CPU count different geometry", info: info, changeNode: func(n *api.V0044Node) { n.Cores = ptr.To[int32](4); n.Threads = ptr.To[int32](1) }, wantErr: true},
		{name: "CPU count mismatch", info: info, changeNode: func(n *api.V0044Node) { n.Cpus = ptr.To[int32](8) }, wantErr: true},
		{name: "inventory disappeared", extra: applied, wantErr: true},
		{name: "foreign Extra", info: info, extra: "administrator value", wantErr: true},
		{name: "malformed previous registration", info: info, extra: dra.AppliedInventoryExtraPrefix + "invalid", wantErr: true},
	} {
		for _, labeled := range []bool{false, true} {
			t.Run(tc.name+"/labeled="+strconv.FormatBool(labeled), func(t *testing.T) {
				state := tc.state
				if state == nil {
					state = []api.V0044NodeState{api.V0044NodeStateIDLE}
				}
				n := &api.V0044Node{
					Name: ptr.To("worker"), Cpus: ptr.To[int32](4), Sockets: ptr.To[int32](1), Cores: ptr.To[int32](2), Threads: ptr.To[int32](2),
					State: ptr.To(state), Extra: ptr.To(tc.extra), Gres: ptr.To("gpu:gpu.example.com:2,nic:unmanaged:1"),
					Features: ptr.To(api.V0044CsvString{"admin", wellknown.SlurmFeatureGRESCompatible}), ActiveFeatures: ptr.To(api.V0044CsvString{"admin", wellknown.SlurmFeatureGRESCompatible}),
				}
				if tc.changeNode != nil {
					tc.changeNode(n)
				}
				var updates []api.V0044UpdateNodeMsg
				c := fake.NewClientBuilder().WithObjects(n).WithUpdateFn(func(_ context.Context, _ object.Object, req any, _ ...slurmclient.UpdateOption) error {
					updates = append(updates, req.(api.V0044UpdateNodeMsg))
					return nil
				}).Build()
				r := &realSlurmControl{Client: c}
				knode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
				var err error
				if labeled {
					// The externally labeled hybrid route must never recreate slurmd's node.
					recreate, checkErr := r.NodeNeedsRecreate(context.Background(), knode, tc.info, testExampleDRAInventory())
					if recreate || (checkErr != nil) != tc.wantErr {
						t.Fatalf("recreate=%v error=%v", recreate, checkErr)
					}
					err = r.AddNode(context.Background(), knode, tc.info, testExampleDRAInventory())
				} else {
					err = r.UpdateHybridNode(context.Background(), knode, tc.info, testExampleDRAInventory())
				}
				if (err != nil) != tc.wantErr {
					t.Fatalf("registration error=%v, wantErr=%v", err, tc.wantErr)
				}
				gotExtra := tc.extra
				for _, update := range updates {
					if update.State != nil || update.Gres != nil {
						t.Fatalf("changed slurmd-managed configuration: %+v", update)
					}
					if update.Extra != nil {
						if tc.wantErr {
							t.Fatal("overwrote registration after validation failed")
						}
						gotExtra = *update.Extra
					}
				}
				if tc.wantErr {
					if len(updates) != 2 || updates[0].FeaturesAct == nil || updates[1].Features == nil ||
						!slices.Equal(*updates[0].FeaturesAct, api.V0044CsvString{"admin"}) || !slices.Equal(*updates[1].Features, api.V0044CsvString{"admin"}) {
						t.Fatalf("failed to revoke bridge eligibility: %+v", updates)
					}
				} else if err := tc.info.ValidateCPUFingerprint(gotExtra); err != nil {
					t.Fatal(err)
				} else if inventory, err := dra.DecodeAppliedInventory(gotExtra); err != nil || len(inventory["gpu.example.com"].Devices) != 2 {
					t.Fatalf("lost GRES inventory: %v, %v", inventory, err)
				}
			})
		}
	}
}

func TestHybridCPURegistrationRetriesBeforeEnablingJobs(t *testing.T) {
	info := testNodeInfoFromResourceSlices(t, "worker", []resourcev1.ResourceSlice{*testNodeCPUResourceSlice("worker")})
	n := &api.V0044Node{
		Name: ptr.To("worker"), Cpus: ptr.To[int32](4), Sockets: ptr.To[int32](1), Cores: ptr.To[int32](2), Threads: ptr.To[int32](2),
		State: ptr.To([]api.V0044NodeState{api.V0044NodeStateIDLE}),
	}
	failExtra := true
	extraApplied := false
	enabled := false
	c := fake.NewClientBuilder().WithObjects(n).WithUpdateFn(func(_ context.Context, obj object.Object, req any, _ ...slurmclient.UpdateOption) error {
		update := req.(api.V0044UpdateNodeMsg)
		if update.Extra != nil {
			if failExtra {
				return errors.New("Extra write failed")
			}
			if err := info.ValidateCPUFingerprint(*update.Extra); err != nil {
				t.Fatal(err)
			}
			obj.(*api.V0044Node).Extra = update.Extra
			extraApplied = true
		}
		if update.Features != nil || update.FeaturesAct != nil {
			if !extraApplied {
				t.Fatal("enabled bridge jobs before applying the fingerprint")
			}
			enabled = true
		}
		return nil
	}).Build()
	r := &realSlurmControl{Client: c}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
	if err := r.UpdateHybridNode(context.Background(), node, info, nil); err == nil || enabled {
		t.Fatalf("failed write: error=%v enabled=%v", err, enabled)
	}
	failExtra = false
	if err := r.UpdateHybridNode(context.Background(), node, info, nil); err != nil || !enabled {
		t.Fatalf("retry: error=%v enabled=%v", err, enabled)
	}
}
