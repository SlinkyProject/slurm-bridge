// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"testing"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

func TestMain(m *testing.M) {
	rc := m.Run()
	os.Exit(rc)
}

func TestAdmissionSchemeSupportsLWS(t *testing.T) {
	if _, _, err := scheme.ObjectKinds(&lwsv1.LeaderWorkerSet{}); err != nil {
		t.Fatalf("admission scheme does not support LeaderWorkerSet: %v", err)
	}
}
