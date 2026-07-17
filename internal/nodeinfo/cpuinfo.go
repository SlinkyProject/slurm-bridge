// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: Copyright 2025 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"fmt"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
)

// CPUInfo holds information about a single CPU.
type CPUInfo struct {
	// Name of enumerated CPU
	Name string `json:"name"`

	// CpuID is the enumerated CPU ID
	CpuID int `json:"cpuID"`

	// CoreID is the logical core ID, unique within each SocketID
	CoreID int `json:"coreID"`

	// SocketID is the physical socket ID
	SocketID int `json:"socketID"`

	// CPU Sibling of the CpuID
	SiblingCpuID cpuset.CPUSet `json:"siblingCpuID"`

	// Core Type (e-core or p-core)
	CoreType CoreType `json:"coreType,omitempty"`
}

// CoreType is an enum for the type of CPU core.
type CoreType int

const (
	// CoreTypeUndefined is the default zero value.
	CoreTypeUndefined CoreType = iota
	// CoreTypeStandard is a standard CPU core.
	CoreTypeStandard
	// CoreTypePerformance is a performance core (p-core).
	CoreTypePerformance
	// CoreTypeEfficiency is an efficiency core (e-core).
	CoreTypeEfficiency
)

// String returns the string representation of a CoreType.
func (c CoreType) String() string {
	switch c {
	case CoreTypeStandard:
		return "standard"
	case CoreTypePerformance:
		return "p-core"
	case CoreTypeEfficiency:
		return "e-core"
	default:
		return ""
	}
}

const DraDriverCpu = "dra.cpu"

// Resource attributes
const (
	DraDriverCpu_CpuID    resourcev1.QualifiedName = "dra.cpu/cpuID"
	DraDriverCpu_CoreID   resourcev1.QualifiedName = "dra.cpu/coreID"
	DraDriverCpu_SocketID resourcev1.QualifiedName = "dra.cpu/socketID"
	DraDriverCpu_CoreType resourcev1.QualifiedName = "dra.cpu/coreType"
)

func NewCPUInfos(rSlice *resourcev1.ResourceSlice) []*CPUInfo {
	cpuInfos := []*CPUInfo{}
	switch rSlice.Spec.Driver {
	case DraDriverCpu:
		for _, device := range rSlice.Spec.Devices {
			cpuInfo := &CPUInfo{
				Name:     device.Name,
				CpuID:    int(ptr.Deref(device.Attributes[DraDriverCpu_CpuID].IntValue, -1)),
				CoreID:   int(ptr.Deref(device.Attributes[DraDriverCpu_CoreID].IntValue, -1)),
				SocketID: int(ptr.Deref(device.Attributes[DraDriverCpu_SocketID].IntValue, -1)),
				CoreType: CoreType(ptr.Deref(device.Attributes[DraDriverCpu_CoreType].IntValue, 0)),
			}
			cpuInfos = append(cpuInfos, cpuInfo)
		}
	default:
		panic(fmt.Errorf("unsupported resource device driver: %v", rSlice.Spec.Driver))
	}
	return cpuInfos
}
