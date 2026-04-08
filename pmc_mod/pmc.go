package pmc

import (
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// How often the single background goroutine refreshes both the memory and
// power counters from the underlying C counters. Tune as needed.
const pmcRefreshPeriod = 20 * time.Microsecond

// If true, the refresher calls the aggregate MemPmc_GetMemAccesses API once
// per tick and only updates the cached total. Per-channel cached values are
// left stale in this mode. If false, the refresher iterates each active
// channel via MemPmc_GetMemChanAccesses, updating both the per-channel cache
// and the total.
const pmcUseTotalOnly = true

// Cached PMC state. All reads from the exported API go through this state so
// that the user never crosses the cgo boundary on the hot path. A single
// background goroutine (see pmcRefresher) is responsible for refreshing all
// fields, so one pinned OS thread covers both memory and power telemetry for
// the whole process.
type pmcState struct {
	// Memory counters (static after Init)
	maxMemChan    uint64
	activeMemChan uint64

	// Memory counters (refreshed periodically)
	chanAccesses []atomic.Uint64 // per-channel cached access counts
	totAccesses  atomic.Uint64   // cached total across active channels

	// Power counters (refreshed periodically). Stored as the IEEE-754 bit
	// pattern of a float64 so it can live in an atomic.Uint64.
	energyBits atomic.Uint64

	// Lifecycle
	stop atomic.Bool   // set by PmcDeInit to signal the refresher goroutine
	done chan struct{} // closed by the refresher goroutine on exit
}

var (
	pmcMu    sync.Mutex
	pmcStPtr atomic.Pointer[pmcState]
)

// PmcInit initializes both the memory and power PMC modules and starts a
// single background goroutine that periodically refreshes all cached
// counters from the underlying C counters. The goroutine is pinned to a
// dedicated OS thread so that it can busy-wait on a monotonic clock and
// deliver sub-100us refresh periods with low jitter. Subsequent reads via
// the exported Get* API return the cached values and do not enter cgo code.
// Safe to call multiple times; only the first call performs initialization.
func PmcInit() {
	pmcMu.Lock()
	defer pmcMu.Unlock()

	if pmcStPtr.Load() != nil {
		return
	}

	// Initialize the underlying C modules
	cMemPmcInit()
	cPowPmcInit()

	// Snapshot the static memory values once
	maxCh := cMemPmcGetMaxMemChan()
	actCh := cMemPmcGetActiveMemChan()

	st := &pmcState{
		maxMemChan:    maxCh,
		activeMemChan: actCh,
		chanAccesses:  make([]atomic.Uint64, maxCh),
		done:          make(chan struct{}),
	}
	pmcStPtr.Store(st)

	go pmcRefresher(st)
}

// PmcDeInit stops the background refresher goroutine and tears down both the
// memory and power PMC modules.
func PmcDeInit() {
	pmcMu.Lock()
	defer pmcMu.Unlock()

	st := pmcStPtr.Load()
	if st == nil {
		return
	}

	// Signal and wait for the refresher to exit before tearing down C state
	st.stop.Store(true)
	<-st.done

	cMemPmcDeInit()
	cPowPmcDeInit()
	pmcStPtr.Store(nil)
}

// pmcRefresher is the single background goroutine responsible for
// periodically pulling all PMC counters from the C layer into the cache. It
// runs pinned to a dedicated OS thread and busy-waits between ticks using a
// monotonic clock.
func pmcRefresher(st *pmcState) {
	// Pin to a dedicated OS thread so scheduler latency does not inflate
	// the refresh period. The thread is destroyed on goroutine exit.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(st.done)

	next := time.Now().Add(pmcRefreshPeriod)
	for {
		// Busy-wait until the next deadline, checking the stop flag so we
		// still exit promptly on PmcDeInit.
		for {
			if st.stop.Load() {
				return
			}
			if !time.Now().Before(next) {
				break
			}
		}

		// Advance the deadline. If we fell behind (e.g. a long cgo call),
		// resync to "now" instead of firing back-to-back catch-up ticks.
		next = next.Add(pmcRefreshPeriod)
		if now := time.Now(); next.Before(now) {
			next = now.Add(pmcRefreshPeriod)
		}

		// Refresh memory counters
		if pmcUseTotalOnly {
			st.totAccesses.Store(cMemPmcGetMemAccesses())
		} else {
			var sum uint64
			for i := uint64(0); i < st.activeMemChan; i++ {
				v := cMemPmcGetMemChanAccesses(int(i))
				st.chanAccesses[i].Store(v)
				sum += v
			}
			st.totAccesses.Store(sum)
		}

		// Refresh power counters
		st.energyBits.Store(math.Float64bits(cPowPmcGetEnergyConsumed()))
	}
}

/**
 * Memory PMC public API (cached, no cgo on the read path)
 */

// MemPmcGetMaxMemChan returns the maximum number of memory channels supported
// by the underlying microarchitecture.
func MemPmcGetMaxMemChan() uint64 {
	st := pmcStPtr.Load()
	if st == nil {
		return 0
	}
	return st.maxMemChan
}

// MemPmcGetActiveMemChan returns the number of memory channels that are
// currently active.
func MemPmcGetActiveMemChan() uint64 {
	st := pmcStPtr.Load()
	if st == nil {
		return 0
	}
	return st.activeMemChan
}

// MemPmcGetMemChanAccesses returns the cached total number of memory
// accesses observed on the given channel. Returns 0 if pmcUseTotalOnly is
// true (per-channel values are not refreshed in that mode).
func MemPmcGetMemChanAccesses(chann int) uint64 {
	st := pmcStPtr.Load()
	if st == nil || chann < 0 || uint64(chann) >= st.maxMemChan {
		return 0
	}
	return st.chanAccesses[chann].Load()
}

// MemPmcGetMemAccesses returns the cached total number of memory accesses
// across all active channels.
func MemPmcGetMemAccesses() uint64 {
	st := pmcStPtr.Load()
	if st == nil {
		return 0
	}
	return st.totAccesses.Load()
}

/**
 * Power PMC public API (cached, no cgo on the read path)
 */

// PowPmcGetEnergyConsumed returns the cached energy consumed (in Joules)
// since the module was initialized.
func PowPmcGetEnergyConsumed() float64 {
	st := pmcStPtr.Load()
	if st == nil {
		return 0.0
	}
	return math.Float64frombits(st.energyBits.Load())
}
