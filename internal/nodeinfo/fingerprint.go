// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package nodeinfo

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/utils/ptr"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
)

// Geometry returns socket, core, and thread counts after excluding reserved CPUs.
// Sockets must have equal core counts, and cores must have equal thread counts.
func (n *NodeInfo) Geometry() (sockets, coresPerSocket, threadsPerCore int, err error) {
	if n == nil || len(n.CpuMap.AbstractToMachine) == 0 {
		return 0, 0, 0, nil
	}
	cores := make(map[int]int)
	for _, cpus := range n.CpuMap.AbstractToMachine {
		if threadsPerCore == 0 {
			threadsPerCore = cpus.Size()
		}
		if cpus.Size() != threadsPerCore {
			return 0, 0, 0, fmt.Errorf("non-uniform threads per core")
		}
		socket := n.CpuMap.CPUInfoMap[cpus.List()[0]].SocketID
		cores[socket]++
	}
	for _, count := range cores {
		if coresPerSocket == 0 {
			coresPerSocket = count
		}
		if count != coresPerSocket {
			return 0, 0, 0, fmt.Errorf("non-uniform cores per socket after CPU reservations")
		}
	}
	deviceSockets := make(map[int]struct{})
	for _, device := range n.CPUDevices {
		deviceSockets[cpuDeviceGroupOf(device).socket] = struct{}{}
	}
	if len(cores) != len(deviceSockets) {
		return 0, 0, 0, fmt.Errorf("CPU device inventory does not match CPU mapping")
	}
	return len(cores), coresPerSocket, threadsPerCore, nil
}

// cpuFingerprint hashes the canonical bitmap mapping and selected device data.
// Publication generations and equivalent CPU/SMT encodings do not change it.
// Bump the prefix version if the mapping or canonicalization rules change.
// The hash identifies topology; claims retain the allocated CPU sets.
func (n *NodeInfo) cpuFingerprint() (string, error) {
	if n == nil || len(n.CPUDevices) == 0 {
		return "", fmt.Errorf("missing grouped CPU inventory")
	}
	if _, _, _, err := n.Geometry(); err != nil {
		return "", err
	}
	out := struct {
		Pool    string
		Devices []resourcev1.Device
		Cores   []string
	}{Pool: n.CpuMap.Pool}
	for _, name := range n.sortedCPUDeviceNames() {
		device := n.CPUDevices[name]
		d := device.DeepCopy()
		delete(d.Attributes, DraDriverCpu_SMTLayout)
		cpus, err := parseCPUSet(ptr.Deref(d.Attributes[DraDriverCpu_CPUIDs].StringValue, ""))
		if err != nil {
			return "", err
		}
		d.Attributes[DraDriverCpu_CPUIDs] = resourcev1.DeviceAttribute{StringValue: ptr.To(cpus.String())}
		out.Devices = append(out.Devices, *d)
	}
	for _, core := range n.CpuMap.AbstractToMachine {
		out.Cores = append(out.Cores, core.String())
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("v1:%x", sha256.Sum256(data)), nil
}

// EncodeApplied adds the CPU fingerprint to the GRES inventory in Extra.
func (n *NodeInfo) EncodeApplied(extra string) (string, error) {
	if n == nil || len(n.CPUDevices) == 0 {
		return extra, nil
	}
	fingerprint, err := n.cpuFingerprint()
	if err != nil {
		return "", err
	}
	return dra.WithAppliedCPUFingerprint(extra, fingerprint)
}

// ValidateCPUFingerprint checks that this topology matches Slurm's registration.
// Use the same NodeInfo to translate the allocation bitmap.
func (n *NodeInfo) ValidateCPUFingerprint(extra string) error {
	applied, err := dra.AppliedCPUFingerprint(extra)
	if err != nil {
		return err
	}
	fingerprint, err := n.cpuFingerprint()
	if err != nil {
		return err
	}
	if applied != fingerprint {
		return fmt.Errorf("CPU topology fingerprint is missing or differs from Slurm registration; waiting for drain and registration")
	}
	return nil
}
