package utils

import (
	"sync/atomic"
)

type SpinLock struct {
	state uint32
}

func (l *SpinLock) Lock() {
	for {
		if atomic.CompareAndSwapUint32(&l.state, 0, 1) {
			return
		}
	}
}

func (l *SpinLock) TryLock() bool {
	return atomic.CompareAndSwapUint32(&l.state, 0, 1)
}

func (l *SpinLock) Unlock() {
	atomic.StoreUint32(&l.state, 0)
}