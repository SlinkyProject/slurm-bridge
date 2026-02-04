// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"slices"
	"sort"

	"github.com/kelindar/bitmap"
	"k8s.io/utils/cpuset"

	"github.com/SlinkyProject/slurm-bridge/internal/utils/bitmaputil"
)

// CPUMap holds the Slurm abstract to machine core map.
type CPUMap struct {
	// CPUInfoMap stores the raw CPUInfos as a map,
	// where the index is the CPU ID, and the value is the CPUInfo.
	CPUInfoMap map[int]*CPUInfo `json:"cpuInfoMap"`

	// AbstractToMachine is a map of abstract to machine cores,
	// where the index is the core, and the value is the set of CpuIDs.
	AbstractToMachine []cpuset.CPUSet `json:"abstractToMachine"`

	// MachineToAbstract is the map of machine to abstract cores,
	// where the index is CpuID, and the value is the core index from AbstractToMachine.
	MachineToAbstract map[int]int `json:"machineToAbstract"`
}

// GetOnesBitmap returns a bitmap with all available bits set to one.
func (cpuMap CPUMap) GetOnesBitmap() bitmap.Bitmap {
	absCpus := make([]int, len(cpuMap.AbstractToMachine))
	for i := range len(cpuMap.AbstractToMachine) {
		absCpus[i] = i
	}
	return bitmaputil.New(absCpus...)
}

// ToAbstractCPUs converts the machine CPU set into an abstract CPU Bitmap.
func (cpuMap CPUMap) ToAbstractCPUs(macCpuSet cpuset.CPUSet) bitmap.Bitmap {
	absCpus := []int{}
	for _, idx := range macCpuSet.List() {
		absCpus = append(absCpus, cpuMap.MachineToAbstract[idx])
	}
	return bitmaputil.New(absCpus...)
}

// ToMachineCPUs converts the abstract CPU Bitmap into a machine CPU set.
func (cpuMap CPUMap) ToMachineCPUs(absBitmap bitmap.Bitmap) cpuset.CPUSet {
	macCpus := []int{}
	absBitmap.Range(func(idx uint32) {
		macCpus = append(macCpus, cpuMap.AbstractToMachine[idx].List()...)
	})
	return cpuset.New(macCpus...)
}

func NewCPUMap(cpuInfos []*CPUInfo) CPUMap {
	sort.SliceStable(cpuInfos, func(i, j int) bool {
		// Align sockets
		if cpuInfos[i].SocketID != cpuInfos[j].SocketID {
			return cpuInfos[i].SocketID < cpuInfos[j].SocketID
		}
		// Align core siblings
		if cpuInfos[i].CoreID != cpuInfos[j].CoreID {
			return cpuInfos[i].CoreID < cpuInfos[j].CoreID
		}
		return cpuInfos[i].CpuID < cpuInfos[j].CpuID
	})

	cpuInfoMap := make(map[int]*CPUInfo, len(cpuInfos))
	abstractToMachine := make([]cpuset.CPUSet, 0)
	for _, cpuInfo := range cpuInfos {
		cpuInfoMap[int(cpuInfo.CpuID)] = cpuInfo
		if cpuInfo.CoreType == CoreTypeEfficiency {
			// Slurm does not enumerate Efficiency cores by default
			continue
		}
		coreSiblings := findCoreSiblings(cpuInfos, cpuInfo)
		cpuInfo.SiblingCpuID = coreSiblings
		if !slices.ContainsFunc(abstractToMachine, coreSiblings.Equals) {
			abstractToMachine = append(abstractToMachine, coreSiblings)
		}
	}

	machineToAbstract := make(map[int]int, len(abstractToMachine))
	for absIdx, coreSiblings := range abstractToMachine {
		for _, macIdx := range coreSiblings.List() {
			machineToAbstract[int(macIdx)] = int(absIdx) //nolint:gosec //FIXME
		}
	}

	return CPUMap{
		CPUInfoMap:        cpuInfoMap,
		AbstractToMachine: abstractToMachine,
		MachineToAbstract: machineToAbstract,
	}
}

func findCoreSiblings(cpuInfos []*CPUInfo, sibling *CPUInfo) cpuset.CPUSet {
	siblings := []int{}
	for _, CPUInfo := range cpuInfos {
		if sibling.SocketID == CPUInfo.SocketID && sibling.CoreID == CPUInfo.CoreID {
			siblings = append(siblings, CPUInfo.CpuID)
		}
	}
	return cpuset.New(siblings...)
}
