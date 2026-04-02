package utils

import (
	"sync/atomic"
)

// AtomicAddUint64 atomically adds n to *val and returns the new value.
func AtomicAddUint64(val *uint64, n uint64) uint64 {
	return atomic.AddUint64(val, n)
}

// AtomicSubUint64 atomically subtracts n from *val and returns the new value.
func AtomicSubUint64(val *uint64, n uint64) uint64 {
	// Use two's complement trick: subtract n by adding ^(n-1)
	return atomic.AddUint64(val, ^(n - 1))
}

// AtomicSetUint64 atomically sets *val to newVal.
func AtomicSetUint64(val *uint64, newVal uint64) {
	atomic.StoreUint64(val, newVal)
}

// AtomicSetUint64 atomically gets *val.
func AtomicGetUint64(val *uint64) uint64 {
	return atomic.LoadUint64(val)
}
