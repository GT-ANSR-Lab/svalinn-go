package main

// #cgo CFLAGS: -msse2 -O3
// #cgo LDFLAGS: -lnuma -lm
// #include "synthetic_worker.c"
// #include <stdint.h>
import "C"
import (
	"fmt"
)

/**
 * Memory bandwidth antagonist worker
 */

// Worker handle
type SqrtWorker struct {
	ptr *C.SqrtWorker
}

// Create the worker
func NewSqrtWorker() (*SqrtWorker, error) {
	w := C.sqrt_worker_create()
	if w == nil {
		return nil, fmt.Errorf("failed to allocate SqrtWorker")
	}
	return &SqrtWorker{ptr: w}, nil
}

// Run the work
func (w *SqrtWorker) Work(n uint64) {
	C.sqrt_worker_work(w.ptr, C.uint64_t(n))
}

// Destroy the worker
func (w *SqrtWorker) Close() {
	C.sqrt_worker_destroy(w.ptr)
	w.ptr = nil
}

/**
 * Memory bandwidth antagonist worker
 */

// Worker handle
type MemBWAntagonistWorker struct {
	ptr *C.MemBWAntagonistWorker
}

// Create the worker
func NewMemBWAntagonistWorker(size int, nopPeriod int, nopNum int) (*MemBWAntagonistWorker, error) {
	w := C.membw_worker_create(C.size_t(size), C.int(nopPeriod), C.int(nopNum))
	if w == nil {
		return nil, fmt.Errorf("failed to allocate MemBWAntagonistWorker")
	}
	return &MemBWAntagonistWorker{ptr: w}, nil
}

// Run the work
func (w *MemBWAntagonistWorker) Work(n uint64) {
	C.membw_worker_work(w.ptr, C.uint64_t(n))
}

// Destroy the worker
func (w *MemBWAntagonistWorker) Close() {
	C.membw_worker_destroy(w.ptr)
	w.ptr = nil
}
