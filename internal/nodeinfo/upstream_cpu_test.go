// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo_test

import (
	"encoding/json"
	"os"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
)

func TestMergedUpstreamCPUTopology(t *testing.T) {
	for _, tc := range []struct {
		file    string
		threads int
		cores   []string
	}{
		{"upstream-cpu-no-smt.json", 1, []string{"1", "2", "3", "5", "6", "7"}},
		{"upstream-cpu-smt.json", 2, []string{"1,9", "2,10", "3,11", "5,13", "6,14", "7,15"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			var slice resourcev1.ResourceSlice
			if err := json.Unmarshal(data, &slice); err != nil {
				t.Fatal(err)
			}
			n, err := nodeinfo.NewNodeInfoFromResourceSlices("node", []resourcev1.ResourceSlice{slice})
			if err != nil {
				t.Fatal(err)
			}
			sockets, cores, threads, err := n.Geometry()
			if err != nil || sockets != 2 || cores != 3 || threads != tc.threads {
				t.Fatalf("geometry = (%d, %d, %d, %v), want (2, 3, %d, nil)", sockets, cores, threads, err, tc.threads)
			}
			if len(n.CpuMap.AbstractToMachine) != len(tc.cores) {
				t.Fatalf("got %d cores, want %d", len(n.CpuMap.AbstractToMachine), len(tc.cores))
			}
			for i, want := range tc.cores {
				if got := n.CpuMap.AbstractToMachine[i].String(); got != want {
					t.Errorf("core %d = %s, want %s", i, got, want)
				}
			}
		})
	}
}
