// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"reflect"
	"testing"
)

func TestArtifactName(t *testing.T) {
	t.Parallel()

	if got, want := artifactName("TestScheduling/DRA resources"), "TestScheduling_DRA_resources"; got != want {
		t.Fatalf("artifactName() = %q, want %q", got, want)
	}
	if got, want := artifactName("///"), "unnamed"; got != want {
		t.Fatalf("artifactName() = %q, want %q", got, want)
	}
}

func TestUniqueStrings(t *testing.T) {
	t.Parallel()

	got := uniqueStrings([]string{"slurm", "slurm-bridge", "slurm", ""})
	want := []string{"slurm", "slurm-bridge", ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueStrings() = %v, want %v", got, want)
	}
}
