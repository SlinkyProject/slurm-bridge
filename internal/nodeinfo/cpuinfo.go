// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-FileCopyrightText: Copyright 2025 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"fmt"
	"math"
	"strconv"
	"strings"

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

const (
	DraDriverCpu_CPUIDs     resourcev1.QualifiedName = "dra.cpu/cpuIDs"
	DraDriverCpu_SMTLayout  resourcev1.QualifiedName = "dra.cpu/smtLayout"
	DraDriverCpu_SMTEnabled resourcev1.QualifiedName = "dra.cpu/smtEnabled"
	DraDriverCpu_Capacity   resourcev1.QualifiedName = "dra.cpu/cpu"
	maxTopologyCPUs                                  = 65535
)

// NewCPUInfos expands socket devices into allocatable CPUs.
// Each core is identified by its lowest allocatable Linux CPU ID.
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
	var infos []*CPUInfo
	for _, device := range rSlice.Spec.Devices {
		socket, err := requiredCPUIntegerAttribute(rSlice.Name, device, DraDriverCpu_SocketID)
		if err != nil {
			return nil, err
		}
		smt, ok := device.Attributes[DraDriverCpu_SMTEnabled]
		if !ok || smt.BoolValue == nil {
			return nil, fmt.Errorf("device %q attribute %q must be a boolean", device.Name, DraDriverCpu_SMTEnabled)
		}
		rawCPUs, err := requiredCPUStringAttribute(device, DraDriverCpu_CPUIDs)
		if err != nil {
			return nil, err
		}
		cpus, err := parseCPUSet(rawCPUs)
		if err != nil {
			return nil, fmt.Errorf("device %q CPU IDs: %w", device.Name, err)
		}
		capacity, ok := device.Capacity[DraDriverCpu_Capacity]
		if !ok || capacity.Value.CmpInt64(int64(cpus.Size())) != 0 || capacity.RequestPolicy != nil {
			return nil, fmt.Errorf("device %q must expose CPU capacity equal to its allocatable CPU IDs without a request policy", device.Name)
		}
		if !ptr.Deref(device.AllowMultipleAllocations, false) {
			return nil, fmt.Errorf("socket device %q must allow multiple allocations", device.Name)
		}
		rawSMT, err := requiredCPUStringAttribute(device, DraDriverCpu_SMTLayout)
		if err != nil {
			return nil, err
		}
		siblings, err := decodeSMTLayout(rawSMT)
		if err != nil {
			return nil, fmt.Errorf("device %q SMT map: %w", device.Name, err)
		}
		for _, cpu := range cpus.List() {
			sibling, ok := siblings[cpu]
			if !ok {
				return nil, fmt.Errorf("CPU %d is missing from device %q SMT map", cpu, device.Name)
			}
			if !*smt.BoolValue && sibling >= 0 {
				return nil, fmt.Errorf("device %q SMT layout contradicts disabled SMT", device.Name)
			}
			core := cpu
			if sibling >= 0 && cpus.Contains(sibling) {
				core = min(cpu, sibling)
			}
			infos = append(infos, &CPUInfo{Name: device.Name, CpuID: cpu, CoreID: core, SocketID: socket, CoreType: CoreTypeStandard})
		}
	}
	return infos, nil
}

func requiredCPUIntegerAttribute(sliceName string, device resourcev1.Device, name resourcev1.QualifiedName) (int, error) {
	attribute, found := device.Attributes[name]
	if !found || attribute.IntValue == nil {
		return 0, fmt.Errorf("DRA CPU ResourceSlice %q device %q does not use the supported socket-device schema: attribute %q must be an integer", sliceName, device.Name, name)
	}
	if *attribute.IntValue < 0 || *attribute.IntValue > math.MaxInt32 {
		return 0, fmt.Errorf("device %q attribute %q must be between 0 and %d", device.Name, name, math.MaxInt32)
	}
	return int(*attribute.IntValue), nil
}

func requiredCPUStringAttribute(device resourcev1.Device, name resourcev1.QualifiedName) (string, error) {
	attribute, ok := device.Attributes[name]
	if !ok || attribute.StringValue == nil || *attribute.StringValue == "" {
		return "", fmt.Errorf("device %q attribute %q must be a non-empty string", device.Name, name)
	}
	return *attribute.StringValue, nil
}

// parseCPUSet bounds range expansion before parsing CPU IDs.
func parseCPUSet(raw string) (cpuset.CPUSet, error) {
	size := int64(0)
	for part := range strings.SplitSeq(raw, ",") {
		lo, hi, ranged := strings.Cut(part, "-")
		start, err := strconv.ParseInt(lo, 10, 32)
		if err != nil || start < 0 {
			return cpuset.New(), fmt.Errorf("invalid CPU range %q", part)
		}
		end := start
		if ranged {
			end, err = strconv.ParseInt(hi, 10, 32)
		}
		if err != nil || end < start {
			return cpuset.New(), fmt.Errorf("invalid CPU range %q", part)
		}
		size += end - start + 1
		if size > maxTopologyCPUs {
			return cpuset.New(), fmt.Errorf("CPU set exceeds supported size %d", maxTopologyCPUs)
		}
	}
	cpus, err := cpuset.Parse(raw)
	if err != nil {
		return cpuset.New(), err
	}
	if cpus.IsEmpty() || int64(cpus.Size()) != size {
		return cpuset.New(), fmt.Errorf("CPU set %q is empty or contains duplicate IDs", raw)
	}
	return cpus, nil
}

// decodeSMTLayout parses upstream's CPU-list>stride;CPU-list encoding.
// The layout includes reserved CPUs; callers select the allocatable CPUs.
func decodeSMTLayout(raw string) (map[int]int, error) {
	siblings := make(map[int]int)
	for atom := range strings.SplitSeq(raw, ";") {
		cpuList, strideText, strided := strings.Cut(atom, ">")
		cpus, err := parseCPUSet(cpuList)
		if err != nil {
			return nil, err
		}
		for _, cpu := range cpus.List() {
			if _, ok := siblings[cpu]; ok {
				return nil, fmt.Errorf("duplicate CPU %d in SMT map", cpu)
			}
		}
		stride := int64(0)
		if strided {
			stride, err = strconv.ParseInt(strideText, 10, 32)
			if err != nil || stride <= 0 {
				return nil, fmt.Errorf("invalid SMT stride %q", strideText)
			}
		}
		for _, cpu := range cpus.List() {
			if _, ok := siblings[cpu]; ok {
				continue
			}
			if !strided {
				siblings[cpu] = -1
				continue
			}
			other := int64(cpu) + stride
			if other > math.MaxInt32 || !cpus.Contains(int(other)) {
				return nil, fmt.Errorf("CPU %d has no sibling at stride %d", cpu, stride)
			}
			if _, exists := siblings[int(other)]; exists {
				return nil, fmt.Errorf("CPU %d has multiple siblings", other)
			}
			siblings[cpu], siblings[int(other)] = int(other), cpu
		}
		if len(siblings) > maxTopologyCPUs {
			return nil, fmt.Errorf("SMT map exceeds supported size")
		}
	}
	return siblings, nil
}
