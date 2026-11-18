// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package schedulingpolicy

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestVolumeSourceAllowlist(t *testing.T) {
	allowed := map[string]bool{"EmptyDir": true, "ConfigMap": true, "Secret": true, "Projected": true, "DownwardAPI": true, "Image": true, "NFS": true, "PersistentVolumeClaim": true}
	dedicated := map[string]bool{"HostPath": true, "CSI": true, "Ephemeral": true}
	typ := reflect.TypeOf(corev1.VolumeSource{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		t.Run(name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data"}}}}
			source := reflect.ValueOf(&pod.Spec.Volumes[0].VolumeSource).Elem()
			source.Field(i).Set(reflect.New(source.Field(i).Type().Elem()))
			errs := ValidatePod(pod)
			if (len(errs) == 0) != allowed[name] {
				t.Fatalf("source %s: %v", name, errs)
			}
			if dedicated[name] && len(errs) != 1 {
				t.Fatalf("duplicate dedicated error: %v", errs)
			}
		})
	}
}
