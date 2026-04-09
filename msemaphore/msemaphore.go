package msemaphore

import (
	"fmt"
	"runtime"
	"sync"
)

// implChoice selects which underlying memory-semaphore algorithm is used.
// This is intentionally NOT exposed to the user — change the value here to
// pick an implementation. Default is epsilon greedy.
type implKind int

const (
	implMabEpsilonGreedy implKind = iota
	implMabThompsonSampling
)

const implChoice = implMabEpsilonGreedy

// MemSemaphore is the public, singleton memory semaphore. Obtain a reference
// via GetInstance(); construction is intentionally not exported.
type MemSemaphore struct {
	impl memSemaphoreImpl
}

var (
	instance     *MemSemaphore
	instanceOnce sync.Once
)

// GetInstance returns a reference to the singleton MemSemaphore.
func GetInstance() *MemSemaphore {
	instanceOnce.Do(func() {
		var impl memSemaphoreImpl
		switch implChoice {
		case implMabEpsilonGreedy:
			fmt.Println("Using MAB Epsilon Greedy Memory Semaphore Implementation")
			impl = newMemSemaphoreMabEgImpl(uint32(runtime.GOMAXPROCS(0)), defInitCap)
		case implMabThompsonSampling:
			fmt.Println("Using MAB Thompson Sampling Memory Semaphore Implementation")
			impl = newMemSemaphoreMabTsImpl(uint32(runtime.GOMAXPROCS(0)), defInitCap)
		default:
			fmt.Println("Using MAB Epsilon Greedy Memory Semaphore Implementation (default)")
			impl = newMemSemaphoreMabEgImpl(uint32(runtime.GOMAXPROCS(0)), defInitCap)
		}
		instance = &MemSemaphore{impl: impl}
	})
	return instance
}

// TryWait attempts to acquire the semaphore without blocking. Returns true on
// success.
func (m *MemSemaphore) TryWait() bool { return m.impl.TryWait() }

// Wait blocks until the semaphore is acquired.
func (m *MemSemaphore) Wait() { m.impl.Wait() }

// QueueDelayTsc returns the queueing delay (in TSC ticks) of the list of
// waiters. (Currently unimplemented in both impls — returns 0.)
func (m *MemSemaphore) QueueDelayTsc() uint64 { return m.impl.QueueDelayTsc() }

// QueueLength returns the number of threads currently waiting to acquire
// the semaphore.
func (m *MemSemaphore) QueueLength() uint64 { return m.impl.QueueLength() }

// Post releases the semaphore.
func (m *MemSemaphore) Post() { m.impl.Post() }

// GetCapacity returns the current semaphore capacity.
func (m *MemSemaphore) GetCapacity() int { return m.impl.GetCapacity() }
