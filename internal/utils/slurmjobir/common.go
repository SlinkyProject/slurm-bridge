// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package slurmjobir

import (
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
)

func ConvStrTo32(input string) (output *int32, err error) {
	out, err := strconv.ParseInt(input, 10, 32)
	if err != nil {
		return nil, err
	}
	retVal := int32(out)

	return &retVal, err
}

func ParseSlurmJobId(input string) int32 {
	out, err := strconv.ParseUint(input, 10, 32)
	if err != nil {
		return 0
	}
	return int32(out) //nolint:gosec // disable G115
}

func GetMemoryFromQuantity(quantity *resource.Quantity) int64 {
	val := quantity.Value()
	return val / 1048576 // value for 1024x1024 to follow what we need for slurm job IR
}
