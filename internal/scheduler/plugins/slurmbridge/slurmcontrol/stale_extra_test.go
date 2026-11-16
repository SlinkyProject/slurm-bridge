// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	"github.com/SlinkyProject/slurm-client/pkg/client"
	"github.com/SlinkyProject/slurm-client/pkg/client/fake"
	"github.com/SlinkyProject/slurm-client/pkg/client/interceptor"
	"github.com/SlinkyProject/slurm-client/pkg/object"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/wellknown"
)

func TestLiveAllocationUsesCurrentGRESMap(t *testing.T) {
	oldExtra := `slurm-bridge.dra-gres-map={"v":1,"profiles":{"gpu.example.com":{"firstIndex":0,"devices":["/dra/gpu.example.com/node/gpu-old"]}}}`
	liveExtra := `slurm-bridge.dra-gres-map={"v":1,"profiles":{"gpu.example.com":{"firstIndex":0,"devices":["/dra/gpu.example.com/node/gpu-new"]}}}`
	r := &realSlurmControl{Client: fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(_ context.Context, _ object.ObjectKey, obj object.Object, opts ...client.GetOption) error {
			options := &client.GetOptions{}
			options.ApplyOptions(opts)
			switch out := obj.(type) {
			case *api.V0044NodeResourceLayoutListObject:
				return json.Unmarshal([]byte(`[{"node":"node","gres":[{"name":"gpu","type":"gpu.example.com","count":1,"index":"0"}]}]`), out)
			case *api.V0044Node:
				out.Extra = ptr.To(oldExtra)
				if options.SkipCache || options.RefreshCache {
					out.Extra = ptr.To(liveExtra)
				}
			}
			return nil
		},
	}).Build()}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{wellknown.LabelExternalJobId: "10"}}}
	resources, err := r.GetResourcesForNodes(t.Context(), pod, []string{"node"})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := dra.DecodeAppliedInventory(resources["node"].NodeExtra)
	if err != nil {
		t.Fatal(err)
	}
	devices, err := inventory.Devices("gpu.example.com", []int{0})
	if err != nil {
		t.Fatal(err)
	}
	if got := devices[0].Device.String(); got != "gpu-new" {
		t.Fatalf("live allocation resolves to %q; want gpu-new from current registration", got)
	}
}
