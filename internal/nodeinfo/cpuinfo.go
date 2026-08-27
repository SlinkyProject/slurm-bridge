// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: Copyright 2025 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"fmt"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/utils/cpuset"
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

func NewCPUInfos(rSlice *resourcev1.ResourceSlice) ([]*CPUInfo, error) {
	if rSlice == nil {
		return nil, fmt.Errorf("expected a CPU ResourceSlice")
	}
	if rSlice.Spec.Driver != DraDriverCpu {
		return nil, fmt.Errorf("unsupported resource device driver %q", rSlice.Spec.Driver)
	}
	if len(rSlice.Spec.Devices) == 0 {
		return nil, fmt.Errorf("DRA CPU ResourceSlice %q contains no devices", rSlice.Name)
	}

	cpuInfos := make([]*CPUInfo, 0, len(rSlice.Spec.Devices))
	for _, device := range rSlice.Spec.Devices {
		cpuID, err := requiredCPUIntegerAttribute(rSlice.Name, device, DraDriverCpu_CpuID)
		if err != nil {
			return nil, err
		}
		coreID, err := requiredCPUIntegerAttribute(rSlice.Name, device, DraDriverCpu_CoreID)
		if err != nil {
			return nil, err
		}
		socketID, err := requiredCPUIntegerAttribute(rSlice.Name, device, DraDriverCpu_SocketID)
		if err != nil {
			return nil, err
		}
		coreType, err := requiredCPUCoreType(rSlice.Name, device)
		if err != nil {
			return nil, err
		}
		cpuInfos = append(cpuInfos, &CPUInfo{
			Name:     device.Name,
			CpuID:    cpuID,
			CoreID:   coreID,
			SocketID: socketID,
			CoreType: coreType,
		})
	}
	return cpuInfos, nil
}

func requiredCPUIntegerAttribute(sliceName string, device resourcev1.Device, name resourcev1.QualifiedName) (int, error) {
	attribute, found := device.Attributes[name]
	if !found || attribute.IntValue == nil {
		return 0, fmt.Errorf("DRA CPU ResourceSlice %q device %q does not use the supported individual-device schema: attribute %q must be an integer", sliceName, device.Name, name)
	}
	if *attribute.IntValue < 0 {
		return 0, fmt.Errorf("DRA CPU ResourceSlice %q device %q attribute %q must not be negative", sliceName, device.Name, name)
	}
	return int(*attribute.IntValue), nil
}

func requiredCPUCoreType(sliceName string, device resourcev1.Device) (CoreType, error) {
	attribute, found := device.Attributes[DraDriverCpu_CoreType]
	if !found || attribute.StringValue == nil {
		return CoreTypeUndefined, fmt.Errorf("DRA CPU ResourceSlice %q device %q does not use the supported individual-device schema: attribute %q must be a string", sliceName, device.Name, DraDriverCpu_CoreType)
	}
	switch *attribute.StringValue {
	case "":
		return CoreTypeUndefined, nil
	case CoreTypeStandard.String():
		return CoreTypeStandard, nil
	case CoreTypePerformance.String():
		return CoreTypePerformance, nil
	case CoreTypeEfficiency.String():
		return CoreTypeEfficiency, nil
	default:
		return CoreTypeUndefined, fmt.Errorf("DRA CPU ResourceSlice %q device %q has unsupported core type %q", sliceName, device.Name, *attribute.StringValue)
	}
}
