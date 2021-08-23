package asm

import (
	"github.com/stretchr/testify/assert"
	"testing"
	"unsafe"
)

func TestArray2dPtr(t *testing.T) {
	dirs := make([][]byte, 4, 4)
	for i := 0; i < 4; i++ {
		dirs[i] = make([]byte, 16)
	}
	out := make([]uint64, len(dirs))
	Get2DArray(out, dirs)
	assert.Equal(t, uintptr(out[0]), uintptr(unsafe.Pointer(&dirs[0][0])))
	assert.Equal(t, uintptr(out[1]), uintptr(unsafe.Pointer(&dirs[1][0])))
	assert.Equal(t, uintptr(out[2]), uintptr(unsafe.Pointer(&dirs[2][0])))
	assert.Equal(t, uintptr(out[3]), uintptr(unsafe.Pointer(&dirs[3][0])))
}
