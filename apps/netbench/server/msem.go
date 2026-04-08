package main

import (
	"math"
	"math/rand"
	. "netbench_go/utils"
	"runtime"
	"sync"
)

const (
	CTL_DELAY_US      = 3000
	DEF_INIT_CAP      = 1
	WINDOW_SZ         = 10
	ALPHA             = 0.8
	TARGET_NORM_MEMBW = 1.0
	MAGNIFYER         = 100.0

	SIGMA_SQ   = 25.0
	SIGMA0_SQ  = 625.0
	MU0        = 80.0
	EXPLR_PROB = 0.3
	TAU        = 30.0
)

const MAX_CORES = 256
const CACHE_LINE_SIZE = 64

type MemSemaphoreSimpleImpl struct {
	mu sync.Mutex

	cap      uint32
	count    uint32
	maxCap   uint32
	maxMembw float64

	samples   [][]float64
	sampleIdx []int

	counts  []uint64
	sums    []float64
	means   []float64
	vars    []float64
	stdDevs []float64

	softmaxP []float64

	// PCM state
	lastBytes uint64
	numCh     uint64

	lastTime int64
}

func NewMemSemaphoreSimpleImpl() *MemSemaphoreSimpleImpl {
	m := &MemSemaphoreSimpleImpl{
		cap:    DEF_INIT_CAP,
		maxCap: uint32(runtime.GOMAXPROCS(0)),

		samples:   make([][]float64, MAX_CORES+1),
		sampleIdx: make([]int, MAX_CORES+1),

		counts:  make([]uint64, MAX_CORES+1),
		sums:    make([]float64, MAX_CORES+1),
		means:   make([]float64, MAX_CORES+1),
		vars:    make([]float64, MAX_CORES+1),
		stdDevs: make([]float64, MAX_CORES+1),

		softmaxP: make([]float64, MAX_CORES+1),
	}

	for i := 1; i <= int(m.maxCap); i++ {
		m.samples[i] = make([]float64, WINDOW_SZ)
		m.means[i] = MU0
		m.vars[i] = SIGMA0_SQ
		m.stdDevs[i] = math.Sqrt(SIGMA0_SQ)
	}

	InitPCM(0)
	m.numCh = uint64(GetActiveChannelCount())
	m.lastBytes = 0
	m.lastTime = int64(MicroTime())

	return m
}

func (m *MemSemaphoreSimpleImpl) getByteCount() uint64 {
	cas := uint64(GetCASCount(0))
	return cas * CACHE_LINE_SIZE * m.numCh
}

func (m *MemSemaphoreSimpleImpl) updateCapacity() {
	now := int64(MicroTime())

	if now-m.lastTime < CTL_DELAY_US {
		return
	}

	nowBytes := m.getByteCount()
	membw := float64(nowBytes-m.lastBytes) / float64(now-m.lastTime)

	if membw > m.maxMembw {
		m.maxMembw = membw
	}

	// Reward computation
	normMembw := membw / m.maxMembw
	if normMembw > TARGET_NORM_MEMBW {
		normMembw = TARGET_NORM_MEMBW
	}
	normMembw /= TARGET_NORM_MEMBW

	normCap := float64(m.cap) / float64(m.maxCap)
	reward := MAGNIFYER * (ALPHA*normMembw - (1-ALPHA)*normCap)

	arm := m.cap

	// Sliding window update
	old := m.samples[arm][m.sampleIdx[arm]]
	m.samples[arm][m.sampleIdx[arm]] = reward
	m.sampleIdx[arm] = (m.sampleIdx[arm] + 1) % WINDOW_SZ

	if m.counts[arm] < WINDOW_SZ {
		m.counts[arm]++
	}
	m.sums[arm] += reward - old

	// Posterior update
	m.vars[arm] = 1.0 / ((1.0 / SIGMA0_SQ) + float64(m.counts[arm])/SIGMA_SQ)
	m.means[arm] = m.vars[arm] * (MU0/SIGMA0_SQ + m.sums[arm]/SIGMA_SQ)
	m.stdDevs[arm] = math.Sqrt(m.vars[arm])

	// ----------------------------------------------------------------------
	//  Exploration (softmax) or Thompson Sampling
	// ----------------------------------------------------------------------

	if rand.Float64() < EXPLR_PROB {
		// ---------------- Softmax over [1..cap] ----------------
		maxMean := -1e18
		for i := uint32(1); i <= m.cap; i++ {
			if m.means[i] > maxMean {
				maxMean = m.means[i]
			}
		}

		sum := 0.0
		for i := uint32(1); i <= m.cap; i++ {
			m.softmaxP[i] = math.Exp((m.means[i] - maxMean) / TAU)
			sum += m.softmaxP[i]
		}
		for i := uint32(1); i <= m.cap; i++ {
			m.softmaxP[i] /= sum
		}

		r := rand.Float64()
		for i := uint32(1); i <= m.cap; i++ {
			if r < m.softmaxP[i] {
				m.cap = i
				break
			}
			r -= m.softmaxP[i]
		}

	} else {
		// ------------- Thompson Sampling ---------------
		best := uint32(1)
		maxScore := -1e18

		for i := uint32(1); i <= m.maxCap; i++ {
			sample := m.means[i] + m.stdDevs[i]*rand.NormFloat64()
			if sample > maxScore {
				maxScore = sample
				best = i
			}
		}

		m.cap = best
	}

	m.lastBytes = nowBytes
	m.lastTime = now
}

func (m *MemSemaphoreSimpleImpl) TryWait() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.updateCapacity()

	if m.count < m.cap {
		m.count++
		return true
	}
	return false
}

func (m *MemSemaphoreSimpleImpl) Post() {
	m.mu.Lock()
	if m.count > 0 {
		m.count--
	}
	m.mu.Unlock()
}

func (m *MemSemaphoreSimpleImpl) GetCapacity() uint32 {
	m.mu.Lock()
	c := m.cap
	m.mu.Unlock()
	return c
}
