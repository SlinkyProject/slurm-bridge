// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestNodeSelector(t *testing.T) {
	for _, selector := range []map[string]string{nil, {}, {"rack": "a"}} {
		pod := &corev1.Pod{Spec: corev1.PodSpec{NodeSelector: selector}}
		if errs := validateNodeSelector(pod); (len(errs) > 0) != (len(selector) > 0) {
			t.Fatal(errs)
		}
	}
}
