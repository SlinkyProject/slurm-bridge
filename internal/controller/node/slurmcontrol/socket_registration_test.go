// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmclient "github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
)

func TestSocketRegistrationFingerprintChanges(t *testing.T) {
	slice := testNodeCPUResourceSlice("worker")
	info := testNodeInfoFromResourceSlices(t, "worker", []resourcev1.ResourceSlice{*slice})
	extra, err := info.EncodeApplied("")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, cpus, extra       string
		wantRecreate, wantError bool
	}{
		{name: "publication generation only", cpus: "0-3", extra: extra},
		{name: "same geometry different CPUs", cpus: "4-7", extra: extra, wantRecreate: true},
		{name: "missing fingerprint after interrupted create", cpus: "0-3", wantRecreate: true},
		{name: "topology changed after interrupted create", cpus: "4-7", wantRecreate: true},
		{name: "GRES only registration", cpus: "0-3", extra: `slurm-bridge.dra-gres-map={"v":1,"profiles":{}}`, wantRecreate: true},
		{name: "invalid fingerprint", cpus: "0-3", extra: `slurm-bridge.dra-gres-map={"v":1,"profiles":{},"cpuFingerprint":"corrupt"}`, wantRecreate: true},
		{name: "foreign Extra", cpus: "0-3", extra: "owned by another integration", wantError: true},
		{name: "inventory disappeared", extra: extra, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slurmNode := &api.V0044Node{Name: ptr.To("worker"), Cpus: ptr.To[int32](4), Sockets: ptr.To[int32](1), Cores: ptr.To[int32](2), Threads: ptr.To[int32](2), RealMemory: ptr.To[int64](0), Extra: ptr.To(tc.extra), State: ptr.To([]api.V0044NodeState{api.V0044NodeStateEXTERNAL})}
			c := fake.NewClientBuilder().WithObjects(slurmNode).WithInterceptorFuncs(interceptor.Funcs{Update: func(ctx context.Context, obj object.Object, req any, opts ...slurmclient.UpdateOption) error {
				t.Fatal("must drain and recreate; cannot adopt current inventory into an existing registration")
				return nil
			}}).Build()
			r := &realSlurmControl{Client: c}
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
			desiredInfo := &nodeinfo.NodeInfo{}
			if tc.cpus != "" {
				desired := slice.DeepCopy()
				desired.Spec.Pool.Generation++
				desired.Spec.Devices[0].Attributes[nodeinfo.DraDriverCpu_CPUIDs] = resourcev1.DeviceAttribute{StringValue: ptr.To(tc.cpus)}
				desired.Spec.Devices[0].Attributes[nodeinfo.DraDriverCpu_SMTLayout] = resourcev1.DeviceAttribute{StringValue: ptr.To(tc.cpus + ">1")}
				desiredInfo = testNodeInfoFromResourceSlices(t, "worker", []resourcev1.ResourceSlice{*desired})
			}
			got, err := r.NodeNeedsRecreate(context.Background(), node, desiredInfo, nil)
			if (err != nil) != tc.wantError || got != tc.wantRecreate {
				t.Fatalf("got recreate=%v error=%v", got, err)
			}
		})
	}
}

func TestSocketRegistrationWithAppliedGPUInventory(t *testing.T) {
	slice := testNodeCPUResourceSlice("worker")
	info := testNodeInfoFromResourceSlices(t, "worker", []resourcev1.ResourceSlice{*slice})
	inventory := testExampleDRAInventory()
	gres, err := buildNodeGRESConfig(inventory)
	if err != nil {
		t.Fatal(err)
	}
	extra, err := info.EncodeApplied(gres.extra)
	if err != nil {
		t.Fatal(err)
	}
	slurmNode := &api.V0044Node{Name: ptr.To("worker"), Cpus: ptr.To[int32](4), Sockets: ptr.To[int32](1), Cores: ptr.To[int32](2), Threads: ptr.To[int32](2), RealMemory: ptr.To[int64](0), Gres: ptr.To(gres.gres), Extra: ptr.To(extra)}
	r := &realSlurmControl{Client: fake.NewClientBuilder().WithObjects(slurmNode).Build()}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
	slice.Spec.Pool.Generation++
	info = testNodeInfoFromResourceSlices(t, "worker", []resourcev1.ResourceSlice{*slice})
	changed, err := r.NodeNeedsRecreate(context.Background(), node, info, inventory)
	if err != nil || changed {
		t.Fatalf("generation-only update with GPU inventory: changed=%v error=%v", changed, err)
	}
}
