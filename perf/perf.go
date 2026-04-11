package perf

import (
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"netdelay"
	"pmc"
)

// The perf package provides a single, cached view of all performance-related
// counters the runtime/application cares about:
//
//   - Memory bandwidth counters (via the pmc package, cgo under the hood)
//   - Power/energy counters      (via the pmc package, cgo under the hood)
//   - Go runtime per-P queueing delay (via runtime.QueueDelay)
//
// A single background goroutine, pinned to its own OS thread, is responsible
// for refreshing every one of these values into process-local caches. User
// code reads from the caches via the exported Get* API and therefore never
// crosses the cgo boundary or takes the runtime allp lock on the hot path.
// This lets us collapse all perf telemetry onto one refresher thread.

// How often the single background goroutine refreshes all cached counters.
// Tune as needed. If set to 0, the monitoring goroutine is not started.
var perfRefreshPeriod = 20 * time.Microsecond

// If true, the refresher calls the aggregate MemPmcGetMemAccesses API once
// per tick and only updates the cached total. Per-channel cached values are
// left stale in this mode. If false, the refresher iterates each active
// channel via MemPmcGetMemChanAccesses, updating both the per-channel cache
// and the total.
const perfUseTotalOnly = true

// If true, GetQueueDelayMax/Avg include the eBPF-measured network stack
// delay (socket buffer residence time) in addition to the Go runtime
// queueing delay. Set to false to use only the Go runtime delay.
const includeNetDelay = true

// Cached perf state. A single refresher goroutine updates these fields; all
// readers go through the exported Get* functions below.
type perfState struct {
	// Memory counters (static after Init)
	maxMemChan    uint64
	activeMemChan uint64

	// Memory counters (refreshed periodically)
	chanAccesses []atomic.Uint64 // per-channel cached access counts
	totAccesses  atomic.Uint64   // cached total across active channels

	// Power counter (refreshed periodically). Stored as the IEEE-754 bit
	// pattern of a float64 so it can live in an atomic.Uint64.
	energyBits atomic.Uint64

	// Go runtime queueing delay (refreshed periodically, nanoseconds)
	qDelayMax atomic.Uint64
	qDelayAvg atomic.Uint64

	// Network stack delay measured by eBPF kprobes (nanoseconds).
	// Time from tcp_v4_do_rcv (packet enters TCP) to tcp_recvmsg
	// (userspace reads). Refreshed periodically.
	netStackDelay atomic.Uint64

	// Lifecycle
	stop atomic.Bool   // set by PerfDeInit to signal the refresher goroutine
	done chan struct{} // closed by the refresher goroutine on exit
}

var (
	perfMu    sync.Mutex
	perfStPtr atomic.Pointer[perfState]
)

// PerfInit initializes the underlying PMC modules and starts a single
// background goroutine that periodically refreshes every cached perf
// counter (memory bandwidth, energy, runtime queueing delay). The goroutine
// is pinned to a dedicated OS thread and busy-waits on a monotonic clock so
// that sub-100us refresh periods are achievable with low jitter. Subsequent
// reads via the exported Get* API return the cached values and do not
// enter cgo or the runtime's allp lock. Safe to call multiple times; only
// the first call performs initialization.
func PerfInit() {
	perfMu.Lock()
	defer perfMu.Unlock()

	if perfStPtr.Load() != nil {
		return
	}

	// Initialize the underlying C modules.
	pmc.MemPmcInit()
	pmc.PowPmcInit()
	netdelay.Init() // best-effort; returns -1 if eBPF unavailable

	// Snapshot the static memory values once.
	maxCh := pmc.MemPmcGetMaxMemChan()
	actCh := pmc.MemPmcGetActiveMemChan()

	st := &perfState{
		maxMemChan:    maxCh,
		activeMemChan: actCh,
		chanAccesses:  make([]atomic.Uint64, maxCh),
		done:          make(chan struct{}),
	}
	perfStPtr.Store(st)

	if perfRefreshPeriod > 0 {
		go perfRefresher(st)
	}
}

// PerfDeInit stops the background refresher goroutine and tears down the
// underlying PMC modules.
func PerfDeInit() {
	perfMu.Lock()
	defer perfMu.Unlock()

	st := perfStPtr.Load()
	if st == nil {
		return
	}

	// Signal and wait for the refresher to exit before tearing down C state.
	st.stop.Store(true)
	if perfRefreshPeriod > 0 {
		<-st.done
	}

	netdelay.DeInit()
	pmc.MemPmcDeInit()
	pmc.PowPmcDeInit()
	perfStPtr.Store(nil)
}

// perfRefresher is the single background goroutine responsible for
// periodically pulling all perf counters into the cache. It runs pinned to
// a dedicated OS thread and busy-waits between ticks using a monotonic
// clock.
func perfRefresher(st *perfState) {
	// Pin to a dedicated OS thread so scheduler latency does not inflate
	// the refresh period. The thread is destroyed on goroutine exit.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(st.done)

	next := time.Now().Add(perfRefreshPeriod)
	for {
		// Busy-wait until the next deadline, checking the stop flag so we
		// still exit promptly on PerfDeInit.
		for {
			if st.stop.Load() {
				return
			}
			if !time.Now().Before(next) {
				break
			}
		}

		// Advance the deadline. If we fell behind (e.g. a long cgo call
		// or an allp-lock contention spike), resync to "now" instead of
		// firing back-to-back catch-up ticks.
		next = next.Add(perfRefreshPeriod)
		if now := time.Now(); next.Before(now) {
			next = now.Add(perfRefreshPeriod)
		}

		// Refresh memory counters.
		if perfUseTotalOnly {
			st.totAccesses.Store(pmc.MemPmcGetMemAccesses())
		} else {
			var sum uint64
			for i := uint64(0); i < st.activeMemChan; i++ {
				v := pmc.MemPmcGetMemChanAccesses(int(i))
				st.chanAccesses[i].Store(v)
				sum += v
			}
			st.totAccesses.Store(sum)
		}

		// Refresh power counter.
		st.energyBits.Store(math.Float64bits(pmc.PowPmcGetEnergyConsumed()))

		// Refresh Go runtime queueing delay (nanoseconds). The custom
		// runtime exposes QueueDelay() returning (max, avg) across all Ps.
		qMax, qAvg := runtime.QueueDelay()
		st.qDelayMax.Store(qMax)
		st.qDelayAvg.Store(qAvg)

		// Refresh network stack delay from eBPF (nanoseconds).
		st.netStackDelay.Store(netdelay.ReadAndResetMaxDelay())
	}
}

/**
 * Memory PMC public API (cached, no cgo on the read path)
 */

// MemPmcGetMaxMemChan returns the maximum number of memory channels supported
// by the underlying microarchitecture.
func MemPmcGetMaxMemChan() uint64 {
	st := perfStPtr.Load()
	if st == nil {
		return 0
	}
	return st.maxMemChan
}

// MemPmcGetActiveMemChan returns the number of memory channels that are
// currently active.
func MemPmcGetActiveMemChan() uint64 {
	st := perfStPtr.Load()
	if st == nil {
		return 0
	}
	return st.activeMemChan
}

// MemPmcGetMemChanAccesses returns the cached total number of memory
// accesses observed on the given channel. Returns 0 if perfUseTotalOnly is
// true (per-channel values are not refreshed in that mode).
func MemPmcGetMemChanAccesses(chann int) uint64 {
	st := perfStPtr.Load()
	if st == nil || chann < 0 || uint64(chann) >= st.maxMemChan {
		return 0
	}
	return st.chanAccesses[chann].Load()
}

// MemPmcGetMemAccesses returns the cached total number of memory accesses
// across all active channels.
func MemPmcGetMemAccesses() uint64 {
	st := perfStPtr.Load()
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
	st := perfStPtr.Load()
	if st == nil {
		return 0.0
	}
	return math.Float64frombits(st.energyBits.Load())
}

/**
 * Runtime queueing delay public API (cached, no allp lock on the read path)
 */

// GetQueueDelayMax returns the cached maximum per-P runqueue queueing delay
// plus the network stack delay measured by eBPF (in nanoseconds) as of the
// last refresh tick. This gives the total delay a request experiences from
// arriving at the kernel TCP stack to actually running in a goroutine.
func GetQueueDelayMax() uint64 {
	st := perfStPtr.Load()
	if st == nil {
		return 0
	}
	d := st.qDelayMax.Load()
	if includeNetDelay {
		d += st.netStackDelay.Load()
	}
	return d
}

// GetQueueDelayAvg returns the cached average per-P runqueue queueing delay
// plus the network stack delay measured by eBPF (in nanoseconds) as of the
// last refresh tick.
func GetQueueDelayAvg() uint64 {
	st := perfStPtr.Load()
	if st == nil {
		return 0
	}
	d := st.qDelayAvg.Load()
	if includeNetDelay {
		d += st.netStackDelay.Load()
	}
	return d
}
