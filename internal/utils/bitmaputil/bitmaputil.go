// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package bitmaputil

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/kelindar/bitmap"
	"k8s.io/utils/cpuset"
)

// New creates a new Bitmap with the given size and bits set.
func New(idx ...int) bitmap.Bitmap {
	o := make(bitmap.Bitmap, 0)
	for _, i := range idx {
		o.Set(uint32(i)) //nolint:gosec
	}
	return o
}

func NewFrom(s string) (bitmap.Bitmap, error) {
	s = strings.TrimPrefix(s, "0x")
	if len(s)%2 != 0 {
		s = fmt.Sprintf("0%s", s)
	}

	b, err := hex.DecodeString(s)
	switch {
	case err != nil:
		return New(), err
	case len(b) == 0:
		return New(), nil
	}

	// convert BigEndian => LittleEndian
	for l, r := 0, len(b)-1; l < r; l, r = l+1, r-1 {
		b[l], b[r] = b[r], b[l]
	}

	return fromBytes(b)

}

func fromBytes(buffer []byte) (bitmap.Bitmap, error) {
	for len(buffer)%8 != 0 {
		buffer = append(buffer, 0)
	}

	out := make(bitmap.Bitmap, len(buffer)/8)
	blkIdx := 0
	for i := 0; i < len(buffer); i += 8 {
		out[blkIdx] = binary.LittleEndian.Uint64(buffer[i : i+8])
		blkIdx++
	}
	return out, nil
}

func String(bm bitmap.Bitmap) string {
	var s string
	for blkIdx := range bm {
		s = fmt.Sprintf("%016x%s", bm[blkIdx], s)
	}
	return "0x" + strings.TrimLeft(s, "0")
}

func ToCPUSet(bm bitmap.Bitmap) cpuset.CPUSet {
	cpuIds := []int{}
	bm.Range(func(idx uint32) {
		cpuIds = append(cpuIds, int(idx))
	})
	return cpuset.New(cpuIds...)
}
