package msemaphore

import (
	"math/rand"
	"sync"

	"perf"
	"utils"
)

// MAB Epsilon-Greedy implementation. Mirrors
// caladan-all/m-semaphore/src/m_semaphore_mab_eg_impl.cpp.

const (
	// Generic constants.
	egCtlDelayUs = 500
	defInitCap   = 1

	// Reward function constants.
	egAlpha            = 0.8
	egTargetNormMembw  = 1.0
	egRewardEwmaWeight = 0.8

	// Epsilon-greedy constants.
	egExplrProb = 0.3
)

type memSemaphoreMabEgImpl struct {
	mu sync.Mutex

	cap      uint32 // current capacity
	count    uint32 // number of holders
	maxCap   uint32 // maximum capacity
	maxMembw float64

	ewmaRewards []float64 // length maxCap+1, indexed 1..maxCap

	// Memory bandwidth measurement state
	lastBytes uint64
	lastTime  uint64 // microseconds

	// Waiter queue
	waiters waiterQueue
}

func newMemSemaphoreMabEgImpl(maxCap, initCap uint32) *memSemaphoreMabEgImpl {
	// The perf subsystem must be running for the controller to read memory
	// bandwidth. Init is a no-op if already initialized.
	perf.PerfInit()

	m := &memSemaphoreMabEgImpl{
		cap:         initCap,
		maxCap:      maxCap,
		ewmaRewards: make([]float64, maxCap+1),
	}
	m.waiters.init()
	m.lastBytes = perf.MemPmcGetMemAccesses()
	m.lastTime = utils.MicroTime()
	return m
}

// updateCapacity must be called with mu held.
func (m *memSemaphoreMabEgImpl) updateCapacity() {
	now := utils.MicroTime()
	if now-m.lastTime < egCtlDelayUs {
		return
	}

	nowBytes := perf.MemPmcGetMemAccesses()
	membw := float64(nowBytes-m.lastBytes) / float64(now-m.lastTime)

	if membw > m.maxMembw {
		m.maxMembw = membw
	}

	// Reward for the current capacity arm.
	normMembw := 0.0
	if m.maxMembw > 0 {
		normMembw = membw / m.maxMembw
	}
	if normMembw > egTargetNormMembw {
		normMembw = egTargetNormMembw
	}
	normMembw /= egTargetNormMembw

	normCap := float64(m.cap) / float64(m.maxCap)
	reward := egAlpha*normMembw - (1-egAlpha)*normCap

	// EWMA update for the current arm.
	m.ewmaRewards[m.cap] = (1-egRewardEwmaWeight)*m.ewmaRewards[m.cap] +
		egRewardEwmaWeight*reward

	// Find the arm with the maximum EWMA reward.
	bestCap := uint32(1)
	for i := uint32(1); i <= m.maxCap; i++ {
		if m.ewmaRewards[bestCap] < m.ewmaRewards[i] {
			bestCap = i
		}
	}

	if rand.Float64() < egExplrProb {
		// Explore: move at most one step away from the best arm.
		dir := int32(1)
		if rand.Intn(2) == 0 {
			dir = -1
		}
		next := int32(bestCap) + dir
		if next < 1 {
			next = 1
		}
		if next > int32(m.maxCap) {
			next = int32(m.maxCap)
		}
		m.cap = uint32(next)
	} else {
		// Exploit.
		m.cap = bestCap
	}

	m.lastBytes = nowBytes
	m.lastTime = now
}

func (m *memSemaphoreMabEgImpl) TryWait() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.updateCapacity()

	if m.count < m.cap {
		m.count++
		return true
	}
	return false
}

func (m *memSemaphoreMabEgImpl) Wait() {
	m.mu.Lock()
	m.updateCapacity()

	// Drain the waiter queue while capacity is available so older waiters
	// get served first.
	for m.count < m.cap && !m.waiters.empty() {
		w := m.waiters.popFront()
		m.count++
		// Wake the waiter. The buffered channel ensures we never block
		// while holding the lock.
		w.ch <- struct{}{}
	}

	if m.count < m.cap {
		m.count++
		m.mu.Unlock()
		return
	}

	// Park the current goroutine on a pooled waiter — no allocations.
	w := acquireWaiter()
	w.enqueueTsc = utils.NanoTime()
	m.waiters.pushBack(w)
	m.mu.Unlock()

	<-w.ch
	releaseWaiter(w)
}

func (m *memSemaphoreMabEgImpl) QueueDelayTsc() uint64 {
	// Not implemented (matches the C++ version).
	return 0
}

func (m *memSemaphoreMabEgImpl) QueueLength() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.waiters.length()
}

func (m *memSemaphoreMabEgImpl) Post() {
	m.mu.Lock()
	m.updateCapacity()

	// If we are over capacity (capacity shrunk while we were holding it),
	// or there is no waiter to wake, just decrement the holder count.
	if m.count > m.cap || m.waiters.empty() {
		m.count--
		m.mu.Unlock()
		return
	}

	w := m.waiters.popFront()
	// The holder count stays the same: we're handing our slot to the next
	// waiter rather than freeing it.
	m.mu.Unlock()
	w.ch <- struct{}{}
}

func (m *memSemaphoreMabEgImpl) GetCapacity() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int(m.cap)
}

