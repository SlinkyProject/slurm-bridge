// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"testing"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

func TestMain(m *testing.M) {
	rc := m.Run()
	os.Exit(rc)
}
