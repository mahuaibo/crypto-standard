// Copyright (c) 2019 Oasis Labs Inc.  All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//   * Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//    * Redistributions in binary form must reproduce the above
// copyright notice, this list of conditions and the following disclaimer
// in the documentation and/or other materials provided with the
// distribution.
//    * Neither the name of Oasis Labs Inc. nor the names of its
// contributors may be used to endorse or promote products derived from
// this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

//+build !amd64

package ed25519

import (
	"bytes"
	cryptorand "crypto/rand"
	"crypto/sha512"
	"errors"
	"fmt"
	"github.com/ultramesh/crypto-standard/ed25519/curve25519"
	"github.com/ultramesh/crypto-standard/ed25519/ge25519"
	"github.com/ultramesh/crypto-standard/ed25519/modm"
	"io"
)

// Upstream: `ed25519-donna-batchverify.h`

const (
	maxBatchSize  = 64
	heapBatchSize = (maxBatchSize * 2) + 1

	// which limb is the 128th bit in?
	limb128bits = (128 + modm.BitsPerLimb - 1) / modm.BitsPerLimb
)

var (
	errArgCounts = errors.New("ed25519: argument count mismatch")

	testBatchY     [32]byte
	testBatchSaveY bool
)

type heapIndex int

type batchHeap struct {
	r       [maxBatchSize * 16]byte // 128 bit random values
	points  [heapBatchSize]ge25519.Ge25519
	scalars [heapBatchSize]modm.Bignum256
	Heap    [heapBatchSize]heapIndex
	Size    int
}

// swap two values in the Heap
func heapSwap(heap []heapIndex, a, b int) {
	// heap_swap(heap_index_t *Heap, size_t a, size_t b)
	heap[a], heap[b] = heap[b], heap[a]
}

// add the scalar at the end of the list to the Heap
func heapInsertNext(heap *batchHeap) {
	// heap_insert_next(batch_heap *Heap)
	var (
		node    = heap.Size
		pheap   = heap.Heap[:]
		scalars = heap.scalars[:]
	)

	// insert at the bottom
	pheap[node] = heapIndex(node)

	// sift node up to its sorted spot
	parent := (node - 1) / 2
	for (node != 0) && modm.LessThanVartime(&scalars[pheap[parent]], &scalars[pheap[node]], modm.LimbSize-1) {
		heapSwap(pheap, parent, node)
		node = parent
		parent = (node - 1) / 2
	}
	heap.Size++
}

// update the Heap when the root element is updated
func heapUpdatedRoot(heap *batchHeap, limbSize int) {
	// heap_updated_root(batch_heap *Heap, size_t limbsize)
	var (
		pheap   = heap.Heap[:]
		scalars = heap.scalars[:]
	)

	// sift root to the bottom
	parent := 0
	node := 1
	childl := 1
	childr := 2
	for childr < heap.Size {
		// Note: The termination check is nominally incorrect, in that
		// it will fail iff the number of nodes is even (only a left-child).
		if modm.LessThanVartime(&scalars[pheap[childl]], &scalars[pheap[childr]], limbSize) {
			node = childr
		} else {
			node = childl
		}
		heapSwap(pheap, parent, node)
		parent = node
		childl = (parent * 2) + 1
		childr = childl + 1

	}

	// sift root back up to its sorted spot
	parent = (node - 1) / 2
	for (node != 0) && modm.LessThanOrEqualVartime(&scalars[pheap[parent]], &scalars[pheap[node]], limbSize) {
		heapSwap(pheap, parent, node)
		node = parent
		parent = (node - 1) / 2
	}
}

// build the Heap with count elements, count must be >= 3 and MUST be odd.
func heapBuild(heap *batchHeap, count int) {
	// heap_build(batch_heap *Heap, size_t count)
	heap.Heap[0] = 0
	heap.Size = 0
	for heap.Size < count {
		heapInsertNext(heap)
	}
}

// extend the Heap to contain new_count elements
func heapExtend(heap *batchHeap, newCount int) {
	for heap.Size < newCount {
		heapInsertNext(heap)
	}
}

// get the top 2 elements of the Heap
func heapGetTop2(heap *batchHeap, limbSize int) (max1, max2 heapIndex) {
	// heap_get_top2(batch_heap *Heap, heap_index_t *max1, heap_index_t *max2, size_t limbsize)
	h0, h1, h2 := heap.Heap[0], heap.Heap[1], heap.Heap[2]
	if modm.LessThanVartime(&heap.scalars[h1], &heap.scalars[h2], limbSize) {
		h1 = h2
	}
	max1 = h0
	max2 = h1
	return
}

func multiScalarmultVartimeFinal(r, point *ge25519.Ge25519, scalar *modm.Bignum256) {
	// ge25519_multi_scalarmult_vartime_final(ge25519 *r, ge25519 *point, bignum256modm scalar)
	const topbit = modm.Element(1) << (modm.BitsPerLimb - 1)

	limb := limb128bits
	if modm.IsOneVartime(scalar) {
		// this will happen most of the time after Bos-Coster
		*r = *point
		return
	} else if modm.IsZeroVartime(scalar) {
		// this will only happen if all scalars == 0
		r.Zero()
		r.Y[0] = 1
		r.Z[0] = 1
		return
	}

	*r = *point

	// find the limb where first bit is set
	for scalar[limb] == 0 {
		limb--
	}

	// find the first bit
	flag := topbit
	for scalar[limb]&flag == 0 {
		flag >>= 1
	}

	// exponentiate
	for {
		ge25519.Double(r, r)
		if scalar[limb]&flag != 0 {
			ge25519.Add(r, r, point)
		}

		flag >>= 1
		if flag == 0 {
			if limb == 0 {
				break
			}
			limb--

			flag = topbit
		}
	}
}

// count must be >= 5 and MUST be odd.
func multiScalarmultVartime(r *ge25519.Ge25519, heap *batchHeap, count int) {
	// ge25519_multi_scalarmult_vartime(ge25519 *r, batch_heap *Heap, size_t count)

	// start with the full limb Size
	limbSize := modm.LimbSize - 1

	// whether the Heap has been extended to include the 128 bit scalars
	var extended bool

	// grab an odd number of scalars to build the Heap, unknown limb sizes
	heapBuild(heap, ((count+1)/2)|1)

	var max1, max2 heapIndex
	for {
		max1, max2 = heapGetTop2(heap, limbSize)
		// only one scalar remaining, we're done
		if modm.IsZeroVartime(&heap.scalars[max2]) {
			break
		}

		// exhausted another limb?
		if heap.scalars[max1][limbSize] == 0 {
			limbSize -= 1
		}

		// can we extend to the 128 bit scalars?
		if !extended && modm.IsAtMost128bitsVartime(&heap.scalars[max1]) {
			heapExtend(heap, count)
			max1, max2 = heapGetTop2(heap, limbSize)
			extended = true
		}

		modm.SubVartime(&heap.scalars[max1], &heap.scalars[max1], &heap.scalars[max2], limbSize)
		ge25519.Add(&heap.points[max2], &heap.points[max2], &heap.points[max1])
		heapUpdatedRoot(heap, limbSize)
	}

	multiScalarmultVartimeFinal(r, &heap.points[max1], &heap.scalars[max1])
}

func isNeutralVartime(p *ge25519.Ge25519) bool {
	// static int ge25519_is_neutral_vartime(const ge25519 *p)
	var zero [32]byte
	var pointBuffer [3][32]byte
	curve25519.Contract(pointBuffer[0][:], &p.X)
	curve25519.Contract(pointBuffer[1][:], &p.Y)
	curve25519.Contract(pointBuffer[2][:], &p.Z)
	if testBatchSaveY {
		// Save off the final Y coord if we are testing the batch verification.
		copy(testBatchY[:], pointBuffer[1][:])
	}
	return bytes.Equal(pointBuffer[0][:], zero[:]) && bytes.Equal(pointBuffer[1][:], pointBuffer[2][:])
}

// VerifyBatch reports whether sigs are valid signatures of messages by
// publicKeys, using entropy from rand.  If rand is nil, crypto/rand.Reader
// will be used.  For convenience, the function will return true iff
// every single signature is valid.
//
// Note: Unlike VerifyWithOptions, this routine will not panic on malformed
// inputs in the batch, and instead just mark the particular signature as
// having failed verification.
func batchVerifyInit(batch *batchHeap, publicKeys, sigs, messages [][]byte) bool {
	//f, context, err := opts.unwrap()
	//if err != nil {
	//	return false
	//}

	num := len(publicKeys)
	if num != len(messages) || len(messages) != len(sigs) {
		return false
	}
	rand := cryptorand.Reader

	var (
		hash [64]byte
		h    = sha512.New()
	)

	batchSize := len(messages)

	// generate r (scalars[batchsize+1]..scalars[2*batchsize]
	if _, err := io.ReadFull(rand, batch.r[:16*batchSize]); err != nil {
		return false
	}
	rScalars := batch.scalars[batchSize+1:]
	for i := 0; i < batchSize; i++ {
		modm.Expand(&rScalars[i], batch.r[16*i:16*(i+1)])
	}

	// compute scalars[0] = ((r1s1 + r2s2 + ...))
	for i := 0; i < batchSize; i++ {
		// The signature should be sized correctly as a signature.
		if l := len(sigs[i]); l != EddsaSignLen {
			return false
		}

		// https://tools.ietf.org/html/rfc8032#section-5.1.7
		// requires that s be in the range [0, order) in order
		// to prevent signature malleability.
		if !scMinimal(sigs[i][32:]) {
			return false
		}

		modm.Expand(&batch.scalars[i], sigs[i][32:])
		modm.Mul(&batch.scalars[i], &batch.scalars[i], &rScalars[i])
	}

	for i := 1; i < batchSize; i++ {
		modm.Add(&batch.scalars[0], &batch.scalars[0], &batch.scalars[i])
	}

	// compute scalars[1]..scalars[batchsize] as r[i]*H(R[i],A[i],m[i])
	for i := 0; i < batchSize; i++ {
		// The public key should be sized correctly as a public key.
		if l := len(publicKeys[i]); l != EddsaPKLen {
			return false
		}

		_, _ = h.Write(sigs[i][:32])
		_, _ = h.Write(publicKeys[i][:])
		_, _ = h.Write(messages[i])
		h.Sum(hash[:0])

		modm.Expand(&batch.scalars[i+1], hash[:])
		modm.Mul(&batch.scalars[i+1], &batch.scalars[i+1], &rScalars[i])

		h.Reset()
	}

	// compute points
	batch.points[0] = ge25519.Basepoint
	for i := 0; i < batchSize; i++ {
		if !ge25519.UnpackVartime(&batch.points[i+1], publicKeys[i][:], true) {
			return false
		}
	}
	for i := 0; i < batchSize; i++ {
		if !ge25519.UnpackVartime(&batch.points[batchSize+i+1], sigs[i], true) {
			return false
		}
	}

	return true
}
func batchVerifyEnd(batch *batchHeap, batchSize int) bool {
	var p ge25519.Ge25519
	multiScalarmultVartime(&p, batch, (batchSize*2)+1)
	return isNeutralVartime(&p)
}
func batchTestHeap(batch *batchHeap, length int) {
	fmt.Println(batch.Size)
	fmt.Printf("1:%d,10:%d,19:%d,40:%d,heap_batch_size-1:%d\n", batch.Heap[1], batch.Heap[10], batch.Heap[19], batch.Heap[40], batch.Heap[heapBatchSize-1])
	for i := 0; i < heapBatchSize; i++ {
		batch.Heap[i] = 233
	}
}
