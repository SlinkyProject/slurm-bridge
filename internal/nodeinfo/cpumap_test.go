// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/utils/cpuset"

	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
)

func TestNewCPUMap(t *testing.T) {
	tests := []struct {
		name     string
		cpuInfos []*nodeinfo.CPUInfo
		want     nodeinfo.CPUMap
	}{
		{
			name: "all socket, no SMT",
			cpuInfos: []*nodeinfo.CPUInfo{
				{Name: "cpu0", CpuID: 0, SocketID: 0, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu1", CpuID: 1, SocketID: 1, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu2", CpuID: 2, SocketID: 2, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu3", CpuID: 3, SocketID: 3, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
			},
			want: nodeinfo.CPUMap{
				CPUInfoMap: map[int]*nodeinfo.CPUInfo{
					0: {Name: "cpu0", CpuID: 0, SocketID: 0, CoreID: 0, SiblingCpuID: cpuset.New(0), CoreType: nodeinfo.CoreTypeStandard},
					1: {Name: "cpu1", CpuID: 1, SocketID: 1, CoreID: 0, SiblingCpuID: cpuset.New(1), CoreType: nodeinfo.CoreTypeStandard},
					2: {Name: "cpu2", CpuID: 2, SocketID: 2, CoreID: 0, SiblingCpuID: cpuset.New(2), CoreType: nodeinfo.CoreTypeStandard},
					3: {Name: "cpu3", CpuID: 3, SocketID: 3, CoreID: 0, SiblingCpuID: cpuset.New(3), CoreType: nodeinfo.CoreTypeStandard},
				},
				AbstractToMachine: []cpuset.CPUSet{
					cpuset.New(0),
					cpuset.New(1),
					cpuset.New(2),
					cpuset.New(3),
				},
				MachineToAbstract: map[int]int{
					0: 0,
					1: 1,
					2: 2,
					3: 3,
				},
			},
		},
		{
			name: "socket, no SMT",
			cpuInfos: []*nodeinfo.CPUInfo{
				{Name: "cpu0", CpuID: 0, SocketID: 0, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu1", CpuID: 1, SocketID: 0, CoreID: 1, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu2", CpuID: 2, SocketID: 1, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu3", CpuID: 3, SocketID: 1, CoreID: 1, CoreType: nodeinfo.CoreTypeStandard},
			},
			want: nodeinfo.CPUMap{
				CPUInfoMap: map[int]*nodeinfo.CPUInfo{
					0: {Name: "cpu0", CpuID: 0, SocketID: 0, CoreID: 0, SiblingCpuID: cpuset.New(0), CoreType: nodeinfo.CoreTypeStandard},
					1: {Name: "cpu1", CpuID: 1, SocketID: 0, CoreID: 1, SiblingCpuID: cpuset.New(1), CoreType: nodeinfo.CoreTypeStandard},
					2: {Name: "cpu2", CpuID: 2, SocketID: 1, CoreID: 0, SiblingCpuID: cpuset.New(2), CoreType: nodeinfo.CoreTypeStandard},
					3: {Name: "cpu3", CpuID: 3, SocketID: 1, CoreID: 1, SiblingCpuID: cpuset.New(3), CoreType: nodeinfo.CoreTypeStandard},
				},
				AbstractToMachine: []cpuset.CPUSet{
					cpuset.New(0),
					cpuset.New(1),
					cpuset.New(2),
					cpuset.New(3),
				},
				MachineToAbstract: map[int]int{
					0: 0,
					1: 1,
					2: 2,
					3: 3,
				},
			},
		},
		{
			name: "socket, SMT",
			cpuInfos: []*nodeinfo.CPUInfo{
				{Name: "cpu0", CpuID: 0, SocketID: 0, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu1", CpuID: 1, SocketID: 0, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu2", CpuID: 2, SocketID: 1, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu3", CpuID: 3, SocketID: 1, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
			},
			want: nodeinfo.CPUMap{
				CPUInfoMap: map[int]*nodeinfo.CPUInfo{
					0: {Name: "cpu0", CpuID: 0, SocketID: 0, CoreID: 0, SiblingCpuID: cpuset.New(0, 1), CoreType: nodeinfo.CoreTypeStandard},
					1: {Name: "cpu1", CpuID: 1, SocketID: 0, CoreID: 0, SiblingCpuID: cpuset.New(0, 1), CoreType: nodeinfo.CoreTypeStandard},
					2: {Name: "cpu2", CpuID: 2, SocketID: 1, CoreID: 0, SiblingCpuID: cpuset.New(2, 3), CoreType: nodeinfo.CoreTypeStandard},
					3: {Name: "cpu3", CpuID: 3, SocketID: 1, CoreID: 0, SiblingCpuID: cpuset.New(2, 3), CoreType: nodeinfo.CoreTypeStandard},
				},
				AbstractToMachine: []cpuset.CPUSet{
					cpuset.New(0, 1),
					cpuset.New(2, 3),
				},
				MachineToAbstract: map[int]int{
					0: 0,
					1: 0,
					2: 1,
					3: 1,
				},
			},
		},
		{
			name: "sequential, SMT",
			cpuInfos: []*nodeinfo.CPUInfo{
				{Name: "cpu0", CpuID: 0, SocketID: 0, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu1", CpuID: 1, SocketID: 0, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu2", CpuID: 2, SocketID: 0, CoreID: 1, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu3", CpuID: 3, SocketID: 0, CoreID: 1, CoreType: nodeinfo.CoreTypeStandard},
			},
			want: nodeinfo.CPUMap{
				CPUInfoMap: map[int]*nodeinfo.CPUInfo{
					0: {Name: "cpu0", CpuID: 0, SocketID: 0, CoreID: 0, SiblingCpuID: cpuset.New(0, 1), CoreType: nodeinfo.CoreTypeStandard},
					1: {Name: "cpu1", CpuID: 1, SocketID: 0, CoreID: 0, SiblingCpuID: cpuset.New(0, 1), CoreType: nodeinfo.CoreTypeStandard},
					2: {Name: "cpu2", CpuID: 2, SocketID: 0, CoreID: 1, SiblingCpuID: cpuset.New(2, 3), CoreType: nodeinfo.CoreTypeStandard},
					3: {Name: "cpu3", CpuID: 3, SocketID: 0, CoreID: 1, SiblingCpuID: cpuset.New(2, 3), CoreType: nodeinfo.CoreTypeStandard},
				},
				AbstractToMachine: []cpuset.CPUSet{
					cpuset.New(0, 1),
					cpuset.New(2, 3),
				},
				MachineToAbstract: map[int]int{
					0: 0,
					1: 0,
					2: 1,
					3: 1,
				},
			},
		},
		{
			name: "sequential, SMT, e-cores",
			cpuInfos: []*nodeinfo.CPUInfo{
				{Name: "cpu0", CpuID: 0, SocketID: 0, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu1", CpuID: 1, SocketID: 0, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu2", CpuID: 2, SocketID: 0, CoreID: 2, CoreType: nodeinfo.CoreTypeEfficiency},
				{Name: "cpu3", CpuID: 3, SocketID: 0, CoreID: 4, CoreType: nodeinfo.CoreTypeEfficiency},
			},
			want: nodeinfo.CPUMap{
				CPUInfoMap: map[int]*nodeinfo.CPUInfo{
					0: {Name: "cpu0", CpuID: 0, SocketID: 0, CoreID: 0, SiblingCpuID: cpuset.New(0, 1), CoreType: nodeinfo.CoreTypeStandard},
					1: {Name: "cpu1", CpuID: 1, SocketID: 0, CoreID: 0, SiblingCpuID: cpuset.New(0, 1), CoreType: nodeinfo.CoreTypeStandard},
					2: {Name: "cpu2", CpuID: 2, SocketID: 0, CoreID: 2, SiblingCpuID: cpuset.New(), CoreType: nodeinfo.CoreTypeEfficiency},
					3: {Name: "cpu3", CpuID: 3, SocketID: 0, CoreID: 4, SiblingCpuID: cpuset.New(), CoreType: nodeinfo.CoreTypeEfficiency},
				},
				AbstractToMachine: []cpuset.CPUSet{
					cpuset.New(0, 1),
					// cpuset.New(2),
					// cpuset.New(3),
				},
				MachineToAbstract: map[int]int{
					0: 0,
					1: 0,
					// 2: 2,
					// 3: 3,
				},
			},
		},
		{
			name: "non-sequential, SMT",
			cpuInfos: []*nodeinfo.CPUInfo{
				{Name: "cpu0", CpuID: 0, SocketID: 0, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu1", CpuID: 1, SocketID: 0, CoreID: 1, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu2", CpuID: 2, SocketID: 0, CoreID: 0, CoreType: nodeinfo.CoreTypeStandard},
				{Name: "cpu3", CpuID: 3, SocketID: 0, CoreID: 1, CoreType: nodeinfo.CoreTypeStandard},
			},
			want: nodeinfo.CPUMap{
				CPUInfoMap: map[int]*nodeinfo.CPUInfo{
					0: {Name: "cpu0", CpuID: 0, SocketID: 0, CoreID: 0, SiblingCpuID: cpuset.New(0, 2), CoreType: nodeinfo.CoreTypeStandard},
					1: {Name: "cpu1", CpuID: 1, SocketID: 0, CoreID: 1, SiblingCpuID: cpuset.New(1, 3), CoreType: nodeinfo.CoreTypeStandard},
					2: {Name: "cpu2", CpuID: 2, SocketID: 0, CoreID: 0, SiblingCpuID: cpuset.New(0, 2), CoreType: nodeinfo.CoreTypeStandard},
					3: {Name: "cpu3", CpuID: 3, SocketID: 0, CoreID: 1, SiblingCpuID: cpuset.New(1, 3), CoreType: nodeinfo.CoreTypeStandard},
				},
				AbstractToMachine: []cpuset.CPUSet{
					cpuset.New(0, 2),
					cpuset.New(1, 3),
				},
				MachineToAbstract: map[int]int{
					0: 0,
					1: 1,
					2: 0,
					3: 1,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodeinfo.NewCPUMap(tt.cpuInfos)
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("NewCPUMap() = %v, want %v", got, tt.want)
			}
		})
	}
}
