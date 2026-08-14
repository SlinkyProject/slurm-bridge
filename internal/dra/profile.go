// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package dra

import "fmt"

// Backend describes how Slurm represents and enforces devices in a
// DeviceProfile.
type Backend interface {
	fmt.Stringer
}

// CoreBitmapBackend represents devices allocated through Slurm's CPU bitmap.
type CoreBitmapBackend struct{}

// String returns the stable name of the backend.
func (CoreBitmapBackend) String() string {
	return "core-bitmap"
}

// IndexedGRESBackend represents devices allocated through an indexed Slurm
// GRES.
type IndexedGRESBackend struct {
	GRESName string
}

// String returns the stable name of the backend.
func (IndexedGRESBackend) String() string {
	return "indexed-gres"
}

// DeviceProfile describes a disjoint physical device pool that Slurm can
// enforce. Selector is the canonical CEL expression a DeviceClass must use to
// resolve to this profile.
type DeviceProfile struct {
	Name     string
	Driver   string
	Selector string
	Backend  Backend
}
