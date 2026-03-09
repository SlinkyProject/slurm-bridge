// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package bitmaputil

import (
	"reflect"
	"testing"

	"github.com/kelindar/bitmap"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/utils/cpuset"
)

func newFrom(s string) bitmap.Bitmap {
	out, err := NewFrom(s)
	if err != nil {
		panic("failed to do NewFrom()")
	}
	return out
}

func TestNew(t *testing.T) {
	type args struct {
		idx []int
	}
	tests := []struct {
		name string
		args args
		want bitmap.Bitmap
	}{
		{
			name: "empty",
			args: args{
				idx: []int{},
			},
			want: bitmap.Bitmap{},
		},
		{
			name: "one",
			args: args{
				idx: []int{0},
			},
			want: bitmap.Bitmap{0x1},
		},
		{
			name: "blocks of data",
			args: args{
				idx: []int{0, 65},
			},
			want: bitmap.Bitmap{0x1, 0x2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := New(tt.args.idx...); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("New() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewFrom(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name    string
		args    args
		want    bitmap.Bitmap
		wantErr bool
	}{
		{
			name: "empty",
			args: args{
				s: "0x",
			},
			want:    New(),
			wantErr: false,
		},
		{
			name: "data",
			args: args{
				s: "0x1",
			},
			want:    New(0),
			wantErr: false,
		},
		{
			name: "bad data",
			args: args{
				s: "bad data",
			},
			want:    New(),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewFrom(tt.args.s)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewFrom() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NewFrom() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestString(t *testing.T) {
	type args struct {
		bm bitmap.Bitmap
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "[New] empty",
			args: args{
				bm: New(),
			},
			want: "0x",
		},
		{
			name: "[New] one",
			args: args{
				bm: New(0),
			},
			want: "0x1",
		},
		{
			name: "[New] blocks of data",
			args: args{
				bm: New(0, 65),
			},
			want: "0x20000000000000001",
		},
		{
			name: "[NewFrom] empty",
			args: args{
				bm: newFrom("0x"),
			},
			want: "0x",
		},
		{
			name: "[NewFrom] one",
			args: args{
				bm: newFrom("0x1"),
			},
			want: "0x1",
		},
		{
			name: "[NewFrom] blocks of data",
			args: args{
				bm: newFrom("0x20000000000000001"),
			},
			want: "0x20000000000000001",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := String(tt.args.bm); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func newBitmap(str string) bitmap.Bitmap {
	bm, err := NewFrom(str)
	if err != nil {
		panic(err)
	}
	return bm
}

func TestToCPUSet(t *testing.T) {
	tests := []struct {
		name string
		bm   bitmap.Bitmap
		want cpuset.CPUSet
	}{
		{
			name: "empty",
			bm:   newBitmap("0x0"),
			want: cpuset.New(),
		},
		{
			name: "0-3",
			bm:   newBitmap("0x0F"),
			want: cpuset.New(0, 1, 2, 3),
		},
		{
			name: "4-7",
			bm:   newBitmap("0xF0"),
			want: cpuset.New(4, 5, 6, 7),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToCPUSet(tt.bm)
			if !equality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("ToCPUSet() = %v, want %v", got, tt.want)
			}
		})
	}
}
