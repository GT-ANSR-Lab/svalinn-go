package main

import (
	"math"
	"unsafe"
)

const cacheLineSize = 64

// ---------------------------------------------------------------
// SqrtWorker — pure-Go CPU-intensive worker.
// Repeatedly computes sqrt(i * kNumber) for the given iterations.
// ---------------------------------------------------------------

type SqrtWorker struct {
	_ [cacheLineSize]byte // padding
}

func NewSqrtWorker() *SqrtWorker {
	return &SqrtWorker{}
}

func (w *SqrtWorker) Work(n uint64) {
	const kNumber = 2350845.545
	for i := uint64(0); i < n; i++ {
		v := math.Sqrt(float64(i) * kNumber)
		// Prevent the compiler from optimizing the computation away.
		sinkFloat64 = v
	}
}

func (w *SqrtWorker) Close() {}

// ---------------------------------------------------------------
// MemBWWorker — pure-Go memory-bandwidth worker.
// Streams writes over a buffer in cacheline-sized steps. Multiple
// workers with distinct buffers spread memory traffic across DRAM.
// ---------------------------------------------------------------

type MemBWWorker struct {
	buf  []byte
	size int
}

func NewMemBWWorker(size int) *MemBWWorker {
	// Allocate a cacheline-aligned buffer.
	raw := make([]byte, size+cacheLineSize)
	base := uintptr(unsafe.Pointer(&raw[0]))
	offset := int((cacheLineSize - (base % cacheLineSize)) % cacheLineSize)
	buf := raw[offset : offset+size]

	return &MemBWWorker{
		buf:  buf,
		size: size,
	}
}

func (w *MemBWWorker) Work(n uint64) {
	buf := w.buf
	size := w.size
	// Stream-write the buffer n times. Each cacheline write is a full
	// 64-byte store. With many workers owning distinct buffers, concurrent
	// requests naturally spread memory traffic and saturate DRAM bandwidth.
	for k := uint64(0); k < n; k++ {
		for i := 0; i < size; i += cacheLineSize {
			// Write 64 bytes (one cache line) at position i.
			p := buf[i : i+cacheLineSize]
			_ = p[63] // bounds check elimination hint
			p[0] = 0
			p[8] = 0
			p[16] = 0
			p[24] = 0
			p[32] = 0
			p[40] = 0
			p[48] = 0
			p[56] = 0
		}
	}
}

func (w *MemBWWorker) Close() {}

// sinkFloat64 prevents the compiler from optimizing away pure computations.
var sinkFloat64 float64
