// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestNodeNameCreation(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{NodeName: "target"}}
	if errs := ValidateCreate(pod); len(errs) != 1 || errs[0].Field != "spec.nodeName" {
		t.Fatal(errs)
	}
	if errs := ValidateUpdate(pod.DeepCopy(), pod); len(errs) != 0 {
		t.Fatalf("normal bound update rejected: %v", errs)
	}
	pod.Spec.NodeName = ""
	if errs := ValidateCreate(pod); len(errs) != 0 {
		t.Fatal(errs)
	}
}
