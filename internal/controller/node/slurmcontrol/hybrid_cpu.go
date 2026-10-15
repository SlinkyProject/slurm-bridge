// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmcontrol

import (
	"fmt"
	"strings"

	"k8s.io/utils/ptr"

	api "github.com/SlinkyProject/slurm-client/api/v0044"
	slurmutils "github.com/SlinkyProject/slurm-client/pkg/utils"

	"github.com/SlinkyProject/slurm-bridge/internal/dra"
	"github.com/SlinkyProject/slurm-bridge/internal/nodeinfo"
)

// hybridNodeExtra validates Slurm geometry and adds the CPU fingerprint to Extra.
// Slurm's node API does not expose Linux CPU ordering.
func hybridNodeExtra(node *api.V0044Node, info *nodeinfo.NodeInfo, extra string) (string, error) {
	current := ptr.Deref(node.Extra, "")
	var applied string
	if strings.HasPrefix(current, dra.AppliedInventoryExtraPrefix) {
		var err error
		applied, err = dra.AppliedCPUFingerprint(current)
		if err != nil {
			return "", err
		}
	}

	sockets, cores, threads, err := info.Geometry()
	if err != nil {
		return "", err
	}
	if sockets == 0 {
		if applied != "" {
			return "", fmt.Errorf("hybrid node %q lost its applied CPU inventory; refusing CPU fallback", node.GetKey())
		}
	} else {
		if int(ptr.Deref(node.Sockets, 0)) != sockets || int(ptr.Deref(node.Cores, 0)) != cores ||
			int(ptr.Deref(node.Threads, 0)) != threads || int(ptr.Deref(node.Cpus, 0)) != sockets*cores*threads {
			return "", fmt.Errorf("hybrid node %q CPU geometry differs from DRA: Slurm has Sockets=%d CoresPerSocket=%d ThreadsPerCore=%d CPUs=%d; DRA requires %d/%d/%d/%d; correct slurmd registration before admitting bridge jobs",
				node.GetKey(), ptr.Deref(node.Sockets, 0), ptr.Deref(node.Cores, 0), ptr.Deref(node.Threads, 0), ptr.Deref(node.Cpus, 0), sockets, cores, threads, sockets*cores*threads)
		}
		extra, err = info.EncodeApplied(extra)
		if err != nil {
			return "", err
		}
		if err := info.ValidateCPUFingerprint(current); err != nil {
			state := slurmutils.GetStateAsSet(node.State)
			if !state.Has(api.V0044NodeStateIDLE) || state.HasAny(api.V0044NodeStateALLOCATED, api.V0044NodeStateMIXED, api.V0044NodeStateCOMPLETING) || ptr.Deref(node.AllocCpus, 0) != 0 {
				return "", fmt.Errorf("hybrid node %q must be idle before recording its CPU topology", node.GetKey())
			}
			if applied != "" && (!state.Has(api.V0044NodeStateDRAIN) || state.Has(api.V0044NodeStateUNDRAIN)) {
				return "", fmt.Errorf("hybrid node %q CPU topology changed; drain the node before re-registering it", node.GetKey())
			}
		}
	}
	if extra != "" && current != "" && !strings.HasPrefix(current, dra.AppliedInventoryExtraPrefix) {
		return "", fmt.Errorf("cannot record applied DRA inventory on Slurm node %q: Extra field is already in use", node.GetKey())
	}
	return extra, nil
}
