package msemaphore

import (
	"math"
	"math/rand"

	"perf"
	"utils"
)

// MAB Thompson-Sampling implementation. Mirrors
// caladan-all/m-semaphore/src/m_semaphore_mab_ts_impl.cpp.

const (
	// Generic constants.
	tsCtlDelayUs = 500
	tsWindowSz   = 10

	// Reward function constants.
	tsAlpha           = 0.8
	tsTargetNormMembw = 1.0
	tsMagnifyer       = 100.0

	// Thompson Sampling constants. Tuned for the magnified reward range.
	tsSigmaSq  = 25.0
	tsSigma0Sq = 625.0
	tsMu0      = 80.0

	// Softmax exploration constants.
	tsExplrProb = 0.3
	tsTau       = 30.0
)

type memSemaphoreMabTsImpl struct {
	mu utils.SpinLock

	cap      uint32
	count    uint32
	maxCap   uint32
	maxMembw float64

	// Sliding window of past rewards per arm. samples[arm] has length tsWindowSz.
	samples    [][]float64
	samplesIdx []int

	// Posterior distribution state per arm.
	counts  []uint64
	sums    []float64
	means   []float64
	vars    []float64
	stdDevs []float64

	// Scratch buffer for softmax probabilities.
	softmaxProbs []float64

	// Per-instance RNG to avoid contention on the global rand lock.
	rng *rand.Rand

	// Memory bandwidth measurement state.
	lastBytes uint64
	lastTime  uint64 // microseconds

	// Waiter queue.
	waiters waiterQueue
}

func newMemSemaphoreMabTsImpl(maxCap, initCap uint32) *memSemaphoreMabTsImpl {
	perf.PerfInit()

	m := &memSemaphoreMabTsImpl{
		cap:          initCap,
		maxCap:       maxCap,
		samples:      make([][]float64, maxCap+1),
		samplesIdx:   make([]int, maxCap+1),
		counts:       make([]uint64, maxCap+1),
		sums:         make([]float64, maxCap+1),
		means:        make([]float64, maxCap+1),
		vars:         make([]float64, maxCap+1),
		stdDevs:      make([]float64, maxCap+1),
		softmaxProbs: make([]float64, maxCap+1),
		rng:          rand.New(rand.NewSource(rand.Int63())),
	}
	for i := uint32(1); i <= maxCap; i++ {
		m.samples[i] = make([]float64, tsWindowSz)
		m.means[i] = tsMu0
		m.vars[i] = tsSigma0Sq
		m.stdDevs[i] = math.Sqrt(tsSigma0Sq)
	}
	m.waiters.init()
	m.lastBytes = perf.MemPmcGetMemAccesses()
	m.lastTime = utils.MicroTime()
	return m
}

// updateCapacity must be called with mu held.
func (m *memSemaphoreMabTsImpl) updateCapacity() {
	now := utils.MicroTime()
	if now-m.lastTime < tsCtlDelayUs {
		return
	}

	nowBytes := perf.MemPmcGetMemAccesses()
	membw := float64(nowBytes-m.lastBytes) / float64(now-m.lastTime)

	if membw > m.maxMembw {
		m.maxMembw = membw
	}

	normMembw := 0.0
	if m.maxMembw > 0 {
		normMembw = membw / m.maxMembw
	}
	if normMembw > tsTargetNormMembw {
		normMembw = tsTargetNormMembw
	}
	normMembw /= tsTargetNormMembw

	normCap := float64(m.cap) / float64(m.maxCap)
	reward := tsMagnifyer * (tsAlpha*normMembw - (1-tsAlpha)*normCap)

	arm := m.cap

	// Sliding window update.
	old := m.samples[arm][m.samplesIdx[arm]]
	m.samples[arm][m.samplesIdx[arm]] = reward
	m.samplesIdx[arm] = (m.samplesIdx[arm] + 1) % tsWindowSz

	m.counts[arm]++
	if m.counts[arm] > tsWindowSz {
		m.counts[arm] = tsWindowSz
	}
	m.sums[arm] += reward - old

	// Posterior update for the unknown mean reward (Normal-Normal conjugacy
	// with known variance).
	m.vars[arm] = 1.0 / ((1.0 / tsSigma0Sq) + float64(m.counts[arm])/tsSigmaSq)
	m.means[arm] = m.vars[arm] * (tsMu0/tsSigma0Sq + m.sums[arm]/tsSigmaSq)
	m.stdDevs[arm] = math.Sqrt(m.vars[arm])

	if m.rng.Float64() < tsExplrProb {
		// Explore: softmax over arms in [1..cap] (lower-than-current).
		// This probes for regime changes that lower the optimal capacity.
		maxMean := -1e18
		for i := uint32(1); i <= m.cap; i++ {
			if m.means[i] > maxMean {
				maxMean = m.means[i]
			}
		}
		sum := 0.0
		for i := uint32(1); i <= m.cap; i++ {
			m.softmaxProbs[i] = math.Exp((m.means[i] - maxMean) / tsTau)
			sum += m.softmaxProbs[i]
		}
		for i := uint32(1); i <= m.cap; i++ {
			m.softmaxProbs[i] /= sum
		}
		r := m.rng.Float64()
		for i := uint32(1); i <= m.cap; i++ {
			if r < m.softmaxProbs[i] {
				m.cap = i
				break
			}
			r -= m.softmaxProbs[i]
		}
	} else {
		// Vanilla Thompson sampling: sample one mean from each arm's
		// posterior, and pick the argmax.
		best := uint32(1)
		bestSample := -1e18
		for i := uint32(1); i <= m.maxCap; i++ {
			s := m.means[i] + m.stdDevs[i]*m.rng.NormFloat64()
			if s > bestSample {
				bestSample = s
				best = i
			}
		}
		m.cap = best
	}

	m.lastBytes = nowBytes
	m.lastTime = now
}

func (m *memSemaphoreMabTsImpl) TryWait() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.updateCapacity()

	if m.count < m.cap {
		m.count++
		return true
	}
	return false
}

func (m *memSemaphoreMabTsImpl) Wait() {
	m.mu.Lock()
	m.updateCapacity()

	for m.count < m.cap && !m.waiters.empty() {
		w := m.waiters.popFront()
		m.count++
		w.ch <- struct{}{}
	}

	if m.count < m.cap {
		m.count++
		m.mu.Unlock()
		return
	}

	w := acquireWaiter()
	w.enqueueTsc = utils.NanoTime()
	m.waiters.pushBack(w)
	m.mu.Unlock()

	<-w.ch
	releaseWaiter(w)
}

func (m *memSemaphoreMabTsImpl) QueueDelayTsc() uint64 { return 0 }

func (m *memSemaphoreMabTsImpl) QueueLength() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.waiters.length()
}

func (m *memSemaphoreMabTsImpl) Post() {
	m.mu.Lock()
	m.updateCapacity()

	if m.count > m.cap || m.waiters.empty() {
		m.count--
		m.mu.Unlock()
		return
	}

	w := m.waiters.popFront()
	m.mu.Unlock()
	w.ch <- struct{}{}
}

func (m *memSemaphoreMabTsImpl) GetCapacity() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int(m.cap)
}
