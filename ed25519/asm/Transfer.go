//+build !amd64

package asm

import "unsafe"

func Get2DArray(out []uint64, in [][]byte) {
	for i := range in {
		out[i] = *(*uint64)(unsafe.Pointer(&in[i]))
	}
}
