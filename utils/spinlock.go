package utils

import (
	"runtime"
	"sync/atomic"
)

// SpinLock is a lightweight, non-reentrant mutual exclusion lock.
// It spins briefly using atomic CAS and then yields via runtime.Gosched()
// to avoid starving the lock holder. Suitable for very short critical
// sections (bitmap set/clear, field updates) where the overhead of
// sync.Mutex goroutine parking is undesirable.
type SpinLock struct {
	state uint32
}

const (
	spinMaxIter = 1000 // number of CAS attempts before yielding
)

// Lock acquires the spinlock. It busy-spins for a bounded number of
// iterations and then yields the processor to prevent priority inversion
// or starvation under the Go cooperative scheduler.
func (l *SpinLock) Lock() {
	for {
		for i := 0; i < spinMaxIter; i++ {
			if atomic.CompareAndSwapUint32(&l.state, 0, 1) {
				return
			}
		}
		runtime.Gosched()
	}
}

// TryLock attempts to acquire the spinlock without blocking.
// Returns true if the lock was acquired, false otherwise.
func (l *SpinLock) TryLock() bool {
	return atomic.CompareAndSwapUint32(&l.state, 0, 1)
}

// Unlock releases the spinlock.
func (l *SpinLock) Unlock() {
	atomic.StoreUint32(&l.state, 0)
}
