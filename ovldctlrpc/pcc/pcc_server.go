package pcc

import (
	"fmt"
	"net"
	"runtime"
	"sync"
	"math/rand"

	. "ovldctlrpc/common"
	. "utils"

	"perf"

	"github.com/kelindar/bitmap"
)

// Maximum number of Go Procs (max. concurrently running go routines)
const (
	SpccMaxProcs uint64 = 256
)

// Maximum supported window size
const (
	SpccMaxWindowExp uint64 = 6
	SpccMaxWindow    uint64 = 1 << SpccMaxWindowExp
)

// Specialized server-side state for a single RPC request
type SpccCtx struct {
	Cmn    *SrpcCtx
	TsSent uint64
	Opaque uint64
}

// PCC Controller states
type SpccCtlStateType uint32
const (
	SpccCtlStatePrepareMicroExp SpccCtlStateType = iota
	SpccCtlStateStartMicroExp
	SpccCtlStateEndMicroExp
	SpccCtlStateMakeDecision
)

// Number of microexperiments performed in each control cycle
const SpccMaxNumMicroExps = 2

// Stats recorded for each microexperiment
type SpccMicroExpStats struct {
	Duration       uint64
	InReqs         uint64
	OutResps       uint64
	GoodOutResps   uint64
	DropReqs       uint64
	QDelay         uint64
	MemAccesses    uint64
	EnergyConsumed float64
	Utility        float64
}

// Credit pool update direction
type SpccDirType int
const (
	SpccDirMinus SpccDirType = -1
	SpccDirStay SpccDirType = 0
	SpccDirPlus  SpccDirType = 1
)

// Drained sessions list
//
// Must be cacheline aligned, as we want one such structure per CPU core
type SpccDrainedList struct {
	Lock  sync.Mutex
	ListH ListHead[SpccSession]
	ListL ListHead[SpccSession]
	_     [8]byte // padding must be verified manually
}

// Specialized server-side state for a single RPC client
type SpccSession struct {
	Cmn            *SrpcSession
	Id             int
	NumPending     int
	Closed         bool
	Lock           sync.Mutex
	AvailSlots     bitmap.Bitmap
	CompletedSlots bitmap.Bitmap
	Slots          [SpccMaxWindow]*SpccCtx
	SendCondVar    *sync.Cond
	SendWaiter     sync.WaitGroup

	DrainedLink ListNode[SpccSession]
	DrainedProc int
	IsLinked    bool
	DrainedTs   uint64

	WakeUp        bool
	Credit        int
	Advertised    int
	Demand        uint64
	NeedECredit   bool
	LastECreditTs uint64
}

func spccGetSlot(ops *SpccOps, s *SpccSession) int {
	s.Lock.Lock()
	defer s.Lock.Unlock()

	slot, ok := s.AvailSlots.Min()
	if !ok {
		return -1
	}
	s.AvailSlots.Remove(slot)
	s.Slots[slot] = ops.SpccCtxPool.Get().(*SpccCtx)
	s.Slots[slot].Cmn = ops.SrpcCtxPool.Get().(*SrpcCtx)
	s.Slots[slot].Cmn.Ext = s.Slots[slot]
	s.Slots[slot].Cmn.S = s.Cmn
	s.Slots[slot].Cmn.Idx = int(slot)
	s.Slots[slot].Cmn.DsCredit = 0
	s.Slots[slot].Cmn.Drop = false

	return int(slot)
}

func spccPutSlot(ops *SpccOps, s *SpccSession, slot int) {
	s.Lock.Lock()
	defer s.Lock.Unlock()

	ops.SrpcCtxPool.Put(s.Slots[slot].Cmn)
	ops.SpccCtxPool.Put(s.Slots[slot])
	s.Slots[slot] = nil
	s.AvailSlots.Set(uint32(slot))
}

func spccUpdateCredit(ops *SpccOps, s *SpccSession, reqDropped bool) {
	creditPool := int(AtomicGetUint64(&ops.SpccCreditPool))
	creditDs := int(AtomicGetUint64(&ops.SpccCreditDs))
	creditUsed := int(AtomicGetUint64(&ops.SpccCreditUsed))
	numSess := int(AtomicGetUint64(&ops.SpccNumSess))
	oldCredit := int(s.Credit)

	if creditDs > 0 {
		creditPool = Min(creditPool, creditDs)
	}

	if s.DrainedProc != -1 {
		return
	}

	creditUnused := creditPool - creditUsed
	maxOverprovision := Max(creditUnused/numSess, 1)
	if creditUsed < creditPool {
		s.Credit = Min(s.NumPending+int(s.Demand)+maxOverprovision,
			s.Credit+creditUnused)
	} else if creditUsed > creditPool {
		s.Credit--
	}

	if s.WakeUp || numSess <= runtime.GOMAXPROCS(0) {
		s.Credit = Max(s.Credit, maxOverprovision)
	}

	if oldCredit > 0 && s.Credit == 0 && !reqDropped {
		s.Credit = maxOverprovision
	}

	s.Credit = Max(s.Credit, s.NumPending)
	s.Credit = Min(s.Credit, int(SpccMaxWindow)-1)
	s.Credit = Min(s.Credit, s.NumPending+int(s.Demand)+maxOverprovision)

	creditDiff := s.Credit - oldCredit
	AtomicAddUint64(&ops.SpccCreditUsed, uint64(creditDiff))
}

func spccSendCompletionVector(ops *SpccOps, s *SpccSession, vec *bitmap.Bitmap) int {

	vec.Range(func(idx uint32) {
		c := s.Slots[idx]
		var len uint64
		var buf *[SrpcBufSize]byte
		var flags uint8 = 0

		if !c.Cmn.Drop {
			len = c.Cmn.RespLen
			buf = &c.Cmn.RespBuf
		} else {
			len = c.Cmn.ReqLen
			buf = &c.Cmn.ReqBuf
			flags |= uint8(PccSFlagDrop)
		}

		// Prepare the header
		shdr := SpccHdr{
			Magic:  PccRespMagic,
			Op:     PccOpCall,
			Len:    len,
			Id:     c.Cmn.Id,
			Credit: uint64(s.Credit),
			TsSent: c.TsSent,
			Flags:  flags,
		}

		// Send the header
		n, err := WriteFull(s.Cmn.C, ToBytes(&shdr))
		if err != nil {
			if n != 0 {
				fmt.Println("Failed to send response header")
			}
			goto failed
		}

		// Send the response payload
		n, err = WriteFull(s.Cmn.C, (*buf)[:len])
		if err != nil {
			if n != 0 {
				fmt.Println("Failed to send response payload")
			}
			goto failed
		}

		// Update stats
		AtomicAddUint64(&ops.SpccStatRespTx, 1)
	failed:
		AtomicSubUint64(&ops.SpccNumPending, 1)

		// Free the slot for reuse
		spccPutSlot(ops, s, int(idx))
	})

	return 0
}

func spccSendECredit(ops *SpccOps, s *SpccSession) int {
	shdr := SpccHdr{
		Magic:  PccRespMagic,
		Op:     PccOpCredit,
		Len:    0,
		Credit: uint64(s.Credit),
	}

	// Send the header
	n, err := WriteFull(s.Cmn.C, ToBytes(&shdr))
	if err != nil {
		if n != 0 {
			fmt.Println("Failed to send credit response header")
		}
		return -1
	}

	AtomicAddUint64(&ops.SpccStatECreditTx, 1)
	return 0
}

func spccSender(ops *SpccOps, s *SpccSession) {
	for {
		s.Lock.Lock()

		for {
			if !s.Closed &&
				!s.NeedECredit &&
				!s.WakeUp &&
				s.CompletedSlots.Count() == 0 {

				s.SendCondVar.Wait()
			} else {
				break
			}
		}

		// Exit
		if s.Closed {
			s.Lock.Unlock()
			break
		}

		// Get a copy of the completed slots
		var tmp bitmap.Bitmap
		s.CompletedSlots.Clone(&tmp)

		// Clear the completed slots
		s.CompletedSlots.Xor(tmp)

		// Check if there is a dropped request
		reqDropped := false
		tmp.Range(func(idx uint32) {
			c := s.Slots[idx]
			if c.Cmn.Drop {
				reqDropped = true
			}
		})

		if s.WakeUp {
			spccRemoveFromDrainedList(ops, s)
		}

		drainedProc := s.DrainedProc
		numResp := tmp.Count()
		s.NumPending -= numResp
		oldCredit := s.Credit
		spccUpdateCredit(ops, s, reqDropped)
		credit := s.Credit
		creditIssued := Max(0, credit-oldCredit+numResp)
		AtomicAddUint64(&ops.SpccStatCreditTx, uint64(creditIssued))

		sendECredit := (s.NeedECredit || s.WakeUp) &&
			numResp == 0 &&
			s.Advertised < s.Credit
		if numResp > 0 || sendECredit {
			s.Advertised = s.Credit
		}
		s.NeedECredit = false
		s.WakeUp = false
		if sendECredit {
			s.LastECreditTs = MicroTime()
		}

		s.Lock.Unlock()

		// Send credit message
		if sendECredit {
			ret := spccSendECredit(ops, s)
			if ret != 0 {
				goto close
			}
			continue
		}

		// Send the responses
		_ = spccSendCompletionVector(ops, s, &tmp)

		if credit == 0 &&
			drainedProc == -1 &&
			s.AvailSlots.Count() == int(SpccMaxWindow) {

			procId := runtime.GetProcId()
			s.Lock.Lock()
			drainedList := &ops.SpccDrained[procId]
			drainedList.Lock.Lock()
			if s.Demand > 0 {
				// Positive demand, put to high priority queue
				drainedList.ListH.Add(&s.DrainedLink)
			} else {
				// Zero demand, put to low priority queue
				drainedList.ListL.AddTail(&s.DrainedLink)
			}
			s.IsLinked = true
			drainedList.Lock.Unlock()
			s.DrainedProc = procId
			AtomicAddUint64(&ops.SpccNumDrained, 1)
			s.Lock.Unlock()
		}
	}

close:
	// Wait for inflight requests to complete
	s.Lock.Lock()
	for !s.Closed || s.AvailSlots.Count()+s.CompletedSlots.Count() < int(SpccMaxWindow) {
		s.SendCondVar.Wait()
	}
	spccRemoveFromDrainedList(ops, s)
	s.Lock.Unlock()

	// Cleanup any remaining slots
	for i := uint64(0); i < SpccMaxWindow; i++ {
		if s.Slots[i] != nil {
			spccPutSlot(ops, s, int(i))
		}
	}

	// Signal the server thread that we are done
	s.SendWaiter.Done()
}

func spccRemoveFromDrainedList(ops *SpccOps, s *SpccSession) {

	if s.DrainedProc == -1 {
		return
	}

	drainedList := &ops.SpccDrained[s.DrainedProc]

	drainedList.Lock.Lock()
	if s.IsLinked {
		s.DrainedLink.Del()
		s.IsLinked = false
		AtomicSubUint64(&ops.SpccNumDrained, 1)
	}
	drainedList.Lock.Unlock()
	s.DrainedProc = -1
}

func spccChooseDrainedH(ops *SpccOps, procID int) *SpccSession {
	now := MicroTime()
	demandTimeout := uint64(Max(int(CpccMaxClientDelayUs)-int(SpccRttUs), int(0)))
	drainedList := &ops.SpccDrained[procID]

	if drainedList.ListH.Empty() {
		return nil
	}

	drainedList.Lock.Lock()

	for {
		s := drainedList.ListH.Tail()
		if s == nil {
			break
		}

		s.Lock.Lock()
		if now > s.DrainedTs+demandTimeout {
			s.DrainedLink.Del()
			drainedList.ListL.AddTail(&s.DrainedLink)
		} else {
			s.Lock.Unlock()
			break
		}
		s.Lock.Unlock()
	}

	if drainedList.ListH.Empty() {
		drainedList.Lock.Unlock()
		return nil
	}

	s := drainedList.ListH.Pop()
	s.IsLinked = false
	drainedList.Lock.Unlock()
	s.Lock.Lock()
	s.DrainedProc = -1
	s.Lock.Unlock()
	AtomicSubUint64(&ops.SpccNumDrained, 1)

	return s
}

func spccChooseDrainedL(ops *SpccOps, procID int) *SpccSession {

	drainedList := &ops.SpccDrained[procID]

	if drainedList.ListL.Empty() {
		return nil
	}

	drainedList.Lock.Lock()
	if drainedList.ListL.Empty() {
		drainedList.Lock.Unlock()
		return nil
	}

	s := drainedList.ListL.Pop()
	s.IsLinked = false
	drainedList.Lock.Unlock()
	s.Lock.Lock()
	s.DrainedProc = -1
	s.Lock.Unlock()
	AtomicSubUint64(&ops.SpccNumDrained, 1)

	return s
}

func spccWakeUpDrainedSession(ops *SpccOps, numSess uint64) {
	procID := runtime.GetProcId()
	maxProcs := runtime.GOMAXPROCS(0)

	for numSess > 0 {
		// First check for a drained session on high priority queue
		// on the local proc
		s := spccChooseDrainedH(ops, procID)

		// Then check for a drained session on high priority queue
		// on remote procs
		i := (procID + 1) % maxProcs
		for s == nil && i != procID {
			s = spccChooseDrainedH(ops, i)
			i = (i + 1) % maxProcs
		}

		// Then check for a drained session on low priority queues
		if s == nil {
			// First local
			s = spccChooseDrainedL(ops, procID)

			// Then remote
			i := (procID + 1) % maxProcs
			for s == nil && i != procID {
				s = spccChooseDrainedL(ops, i)
				i = (i + 1) % maxProcs
			}
		}

		// Exit if there are no sessions to wake up
		if s == nil {
			break
		}

		// Update credits and signal the sender
		s.Lock.Lock()
		s.WakeUp = true
		s.Credit = 1
		s.SendCondVar.Signal()
		s.Lock.Unlock()

		AtomicAddUint64(&ops.SpccCreditUsed, 1)
		numSess--
	}
}

func spccDecrCp(ops *SpccOps) uint64 {

	creditPool := AtomicGetUint64(&ops.SpccCreditPool)
	numSess := AtomicGetUint64(&ops.SpccNumSess)

	newCreditPool := creditPool - SpccEpsilon
	newCreditPool = Max(newCreditPool, uint64(runtime.GOMAXPROCS(0)))
	newCreditPool = Min(newCreditPool, numSess<<SpccMaxWindowExp)

	return newCreditPool
}

func spccIncrCp(ops *SpccOps) uint64 {

	creditPool := AtomicGetUint64(&ops.SpccCreditPool)
	numSess := AtomicGetUint64(&ops.SpccNumSess)

	newCreditPool := creditPool + SpccEpsilon
	newCreditPool = Max(newCreditPool, uint64(runtime.GOMAXPROCS(0)))
	newCreditPool = Min(newCreditPool, numSess<<SpccMaxWindowExp)

	return newCreditPool
}

func spccUpdateCp(ops *SpccOps, dir int) uint64 {

	var newCreditPool uint64

	creditPool := AtomicGetUint64(&ops.SpccCreditPool)
	numSess := AtomicGetUint64(&ops.SpccNumSess)

	if dir == -1 {
		newCreditPool = creditPool - SpccEpsilon
	} else if dir == 1 {
		newCreditPool = creditPool + SpccEpsilon
	} else {
		newCreditPool = creditPool
	}
	newCreditPool = Max(newCreditPool, uint64(runtime.GOMAXPROCS(0)))
	newCreditPool = Min(newCreditPool, numSess<<SpccMaxWindowExp)

	return newCreditPool
}

func spccUpdateCreditPool(ops *SpccOps) {

	var now uint64
	var newCp uint64
	var creditUsed uint64
	var creditUnused uint64
	var doWakeUp bool
	var microExpId uint64
	var stats [SpccMaxNumMicroExps+1]SpccMicroExpStats
	var plusMicroExpId uint64
	var minusMicroExpId uint64
	var updateDir SpccDirType

	if !ops.SpccCtlLock.TryLock() {
		return
	}

	doWakeUp = false

	now = MicroTime()
	if now < ops.SpccNextUpdate {
		goto exit
	}

	switch ops.SpccCtlState {
	case SpccCtlStatePrepareMicroExp:
		// Get the microexperiment index
		microExpId = ops.SpccNumMicroExps + 1

		// Perturb the credit pool if needed
		if SpccMicroExpPerturbCp {
			newCp = spccUpdateCp(ops, int(ops.SpccMicroExpDirs[microExpId]))
			AtomicSetUint64(&ops.SpccCreditPool, newCp)
			doWakeUp = true
		}

		// Update the state
		ops.SpccCtlState = SpccCtlStateStartMicroExp
		ops.SpccNextUpdate = MicroTime() + SpccPreMonIntUs
	case SpccCtlStateStartMicroExp:
		// Get the microexperiment index
		microExpId = ops.SpccNumMicroExps + 1

		// Clear the stats
		AtomicSetUint64(&ops.SpccInReqs[microExpId], 0)
		AtomicSetUint64(&ops.SpccOutResps[microExpId], 0)
		AtomicSetUint64(&ops.SpccGoodOutResps[microExpId], 0)
		AtomicSetUint64(&ops.SpccDropReqs[microExpId], 0)
		AtomicSetUint64(&ops.SpccQDelay[microExpId], perf.GetQueueDelayMax() / 1000)
		ops.SpccMemAccesses[microExpId] = perf.MemPmcGetMemAccesses()
		ops.SpccEnergyConsumed[microExpId] = perf.PowPmcGetEnergyConsumed()

		// Set the start time for the monitor interval
		AtomicSetUint64(&ops.SpccStartTs[microExpId], MicroTime())
		AtomicSetUint64(&ops.SpccEndTs[microExpId], MicroTime())

		// Set the stat index to be used by the workers
		ops.SpccMicroExpId = microExpId

		// Update the state
		ops.SpccCtlState = SpccCtlStateEndMicroExp
		ops.SpccNextUpdate = MicroTime() + SpccMonIntUs
	case SpccCtlStateEndMicroExp:
		// Get the microexperiment index
		microExpId = ops.SpccMicroExpId

		// Update any remaining stats
		ops.SpccMemAccesses[microExpId] = perf.MemPmcGetMemAccesses() - ops.SpccMemAccesses[microExpId]
		ops.SpccEnergyConsumed[microExpId] = perf.PowPmcGetEnergyConsumed() - ops.SpccEnergyConsumed[microExpId]

		// Stop the microexperiment
		ops.SpccMicroExpId = 0
		ops.SpccNumMicroExps++

		// Perform the remaining microexperiments, if any
		if ops.SpccNumMicroExps < SpccMaxNumMicroExps {
			ops.SpccCtlState = SpccCtlStatePrepareMicroExp
			ops.SpccNextUpdate = MicroTime() // move immediately
			break
		}

		if SpccMicroExpPerturbCp {
			// Reset the rate
			AtomicSetUint64(&ops.SpccCreditPool, ops.SpccOrigCp)
			doWakeUp = true
		}

		// Update the state
		ops.SpccCtlState = SpccCtlStateMakeDecision
		ops.SpccNextUpdate = MicroTime() // move immediately
	case SpccCtlStateMakeDecision:

		for i := uint64(1); i <= ops.SpccNumMicroExps; i++ {
			stats[i].Duration = AtomicGetUint64(&ops.SpccEndTs[i]) -
				AtomicGetUint64(&ops.SpccStartTs[i])
			stats[i].InReqs = AtomicGetUint64(&ops.SpccInReqs[i])
			stats[i].OutResps = AtomicGetUint64(&ops.SpccOutResps[i])
			stats[i].GoodOutResps = AtomicGetUint64(&ops.SpccGoodOutResps[i])
			stats[i].DropReqs = AtomicGetUint64(&ops.SpccDropReqs[i])
			stats[i].QDelay = AtomicGetUint64(&ops.SpccQDelay[i])
			stats[i].MemAccesses = ops.SpccMemAccesses[i]
			stats[i].EnergyConsumed = ops.SpccEnergyConsumed[i]

			// Calculate the user-defined utility
			stats[i].Utility = SpccCalcUtilFn(&stats[i])
		}

		// Get the current credit pool size
		newCp = AtomicGetUint64(&ops.SpccCreditPool)

		if SpccMicroExpStrictLabelling {
			if ops.SpccMicroExpDirs[1] == 1 {
				plusMicroExpId = 1
				minusMicroExpId = 2
			} else {
				plusMicroExpId = 2
				minusMicroExpId = 1
			}
			if stats[plusMicroExpId].InReqs <= stats[minusMicroExpId].InReqs {
				goto skip_make_decision
			}
		}

		if stats[1].InReqs == 0 || stats[2].InReqs == 0 {
			updateDir = SpccDirPlus
		} else if stats[1].InReqs > stats[2].InReqs {
			updateDir = SpccCompUtilFn(&stats[2], &stats[1])
		} else if stats[2].InReqs > stats[1].InReqs {
			updateDir = SpccCompUtilFn(&stats[1], &stats[2])
		} else {
			// Rare case
		}

		// Update the rate
		switch updateDir {
		case SpccDirMinus:
			newCp = spccDecrCp(ops)
		case SpccDirStay:
		case SpccDirPlus:
			newCp = spccIncrCp(ops)
		default:
		}

		// Update the rate
		AtomicSetUint64(&ops.SpccCreditPool, newCp)
		doWakeUp = true

	skip_make_decision:
		ops.SpccNumMicroExps = 0
		ops.SpccMicroExpId = 0
		spccGenMicroExpDirs(ops)
		ops.SpccOrigCp = newCp
		ops.SpccCtlState = SpccCtlStatePrepareMicroExp
		ops.SpccNextUpdate = MicroTime() // move immediately
	default:
	}

exit:
	now = MicroTime()
	if doWakeUp || now - ops.SpccLastWakeup > SpccRttUs {
		newCp = AtomicGetUint64(&ops.SpccCreditPool)
		creditUsed = AtomicGetUint64(&ops.SpccCreditUsed)
		if newCp > creditUsed {
			creditUnused = newCp - creditUsed
		} else {
			creditUnused = 0
		}
		spccWakeUpDrainedSession(ops, creditUnused)
		ops.SpccLastWakeup = now
	}

	ops.SpccCtlLock.Unlock()
}

func spccHandleReqDrop(ops *SpccOps, s *SpccSession, delay uint64) {
	spccUpdateCreditPool(ops)
}

func spccWorker(ops *SpccOps, s *SpccSession, c *SpccCtx) {
	microExpId := c.Opaque

	c.Cmn.Drop = false
	ops.SpccHandler(c.Cmn)

	if !c.Cmn.Drop {
		AtomicSetUint64(&ops.SpccCreditDs, c.Cmn.DsCredit)
	} else {
		AtomicAddUint64(&ops.SpccStatReqDropped, 1)
	}

	if ops.SpccMicroExpId == microExpId {
		if !c.Cmn.Drop {
			AtomicAddUint64(&ops.SpccOutResps[microExpId], 1)
			AtomicAddUint64(&ops.SpccGoodOutResps[microExpId], 1) // XXX: Needs update
		} else {
			AtomicAddUint64(&ops.SpccDropReqs[microExpId], 1)
		}
		AtomicSetUint64(&ops.SpccEndTs[microExpId], MicroTime())
		maxQueueDelay := perf.GetQueueDelayMax() / 1000
		if maxQueueDelay > AtomicGetUint64(&ops.SpccQDelay[microExpId]) {
			AtomicSetUint64(&ops.SpccQDelay[microExpId], maxQueueDelay)
		}
	}

	spccUpdateCreditPool(ops)

	// Signal the sender
	s.Lock.Lock()
	s.CompletedSlots.Set(uint32(c.Cmn.Idx))
	s.SendCondVar.Signal()
	s.Lock.Unlock()
}

func spccRecvOne(ops *SpccOps, s *SpccSession) int {

	var chdr CpccHdr
	var tmpBuf [SrpcBufSize]byte
	var creditDiff int
	var ctx *SpccCtx
	var procID int
	var maxQueueDelay uint64
	var microExpId uint64

again:
	// Read the request header
	n, err := ReadFull(s.Cmn.C, ToBytes(&chdr))
	if err != nil {
		if n != 0 {
			fmt.Println("Failed to read the request header")
		}
		return -1
	}

	if chdr.Magic != PccReqMagic {
		fmt.Println("Got invalid magic")
		return -1
	}
	if chdr.Len > SrpcBufSize {
		fmt.Println("Request too large")
		return -1
	}

	switch chdr.Op {
	case PccOpCall:
		microExpId = ops.SpccMicroExpId

		AtomicAddUint64(&ops.SpccStatReqRx, 1)
		if ops.SpccMicroExpId == microExpId {
			AtomicAddUint64(&ops.SpccInReqs[microExpId], 1)
		}

		// Find an available slot
		idx := spccGetSlot(ops, s)
		if idx < 0 {
			ReadFull(s.Cmn.C, tmpBuf[:chdr.Len])
			AtomicAddUint64(&ops.SpccStatReqDropped, 1)
			if ops.SpccMicroExpId == microExpId {
				AtomicAddUint64(&ops.SpccDropReqs[microExpId], 1)
				AtomicSetUint64(&ops.SpccEndTs[microExpId], MicroTime())
				maxQueueDelay = perf.GetQueueDelayMax() / 1000
				if maxQueueDelay > AtomicGetUint64(&ops.SpccQDelay[microExpId]) {
					AtomicSetUint64(&ops.SpccQDelay[microExpId], maxQueueDelay)
				}
			}
			goto again
		}
		ctx = s.Slots[idx]

		// Retrieve the payload
		n, err = ReadFull(s.Cmn.C, ctx.Cmn.ReqBuf[:chdr.Len])
		if err != nil {
			if n != 0 {
				fmt.Println("Failed to read the request payload")
			}
			spccPutSlot(ops, s, idx)
			return -1
		}
		ctx.Cmn.ReqLen = chdr.Len
		ctx.Cmn.RespLen = 0
		ctx.Cmn.Id = chdr.Id
		ctx.TsSent = chdr.TsSent
		ctx.Opaque = microExpId

		s.Lock.Lock()

		s.Demand = chdr.Demand
		spccRemoveFromDrainedList(ops, s)
		s.NumPending++
		// Adjust the credits if demand changed
		if s.Credit > s.NumPending+int(s.Demand) {
			creditDiff = s.Credit - (s.NumPending + int(s.Demand))
			s.Credit = s.NumPending + int(s.Demand)
			AtomicSubUint64(&ops.SpccCreditUsed, uint64(creditDiff))
		}
		AtomicAddUint64(&ops.SpccNumPending, 1)

		// Perform AQM
		maxQueueDelay = perf.GetQueueDelayMax() / 1000
		if maxQueueDelay >= SpccQdelayBudget {
			spccHandleReqDrop(ops, s, maxQueueDelay)
			ctx.Cmn.Drop = true
			s.CompletedSlots.Set(uint32(idx))
			s.SendCondVar.Signal()
			s.Lock.Unlock()
			AtomicAddUint64(&ops.SpccStatReqDropped, 1)
			if ops.SpccMicroExpId == microExpId {
				AtomicAddUint64(&ops.SpccDropReqs[microExpId], 1)
				AtomicSetUint64(&ops.SpccEndTs[microExpId], MicroTime())
				if maxQueueDelay > AtomicGetUint64(&ops.SpccQDelay[microExpId]) {
					AtomicSetUint64(&ops.SpccQDelay[microExpId], maxQueueDelay)
				}
			}
			goto again
		}

		s.Lock.Unlock()

		// Spawn the worker to handle the request
		go spccWorker(ops, s, ctx)
	case PccOpCredit:
		if chdr.Len != 0 {
			fmt.Println("Invalid credit message")
			return -1
		}

		s.Lock.Lock()
		s.Demand = chdr.Demand

		// If s.NumPending > 0 do nothing, as the sender thread will
		// handle that case.
		if s.NumPending == 0 && s.Demand > 0 {
			// If s.NumPending == 0, but there is a positive demand,
			// we need to tell the sender thread to send explicit
			// credits
			s.NeedECredit = true
		} else if s.NumPending == 0 {
			// If there is no demand, then push the client session
			// to low priority drained queue
			procID = runtime.GetProcId()
			drainedList := &ops.SpccDrained[procID]
			drainedList.Lock.Lock()
			drainedList.ListL.AddTail(&s.DrainedLink)
			s.IsLinked = true
			drainedList.Lock.Unlock()
			s.DrainedProc = procID
			AtomicAddUint64(&ops.SpccNumDrained, 1)
			s.Advertised = 0
		}

		// Adjust the credits if demand changed
		if s.Credit > s.NumPending+int(s.Demand) {
			creditDiff = s.Credit - (s.NumPending + int(s.Demand))
			s.Credit = s.NumPending + int(s.Demand)
			AtomicSubUint64(&ops.SpccCreditUsed, uint64(creditDiff))
		}

		// Signal the sender
		s.SendCondVar.Signal()
		s.Lock.Unlock()

		AtomicAddUint64(&ops.SpccStatCUpdateRx, 1)
	default:
		fmt.Println("Invalid op")
		return -1
	}

	return 0
}

func spccServer(ops *SpccOps, conn *net.TCPConn) {
	defer conn.Close()

	// Initialize the session state
	s := &SpccSession{}
	s.Cmn = &SrpcSession{}
	s.Cmn.Ext = s
	s.Cmn.C = conn
	s.Id = int(AtomicAddUint64(&ops.SpccNumSess, 1))
	s.NumPending = 0
	s.Closed = false
	s.AvailSlots.Grow(uint32(SpccMaxWindow))
	s.CompletedSlots.Grow(uint32(SpccMaxWindow))
	for i := uint64(0); i < SpccMaxWindow; i++ {
		s.AvailSlots.Set(uint32(i))
		s.CompletedSlots.Remove(uint32(i))
		s.Slots[i] = nil
	}
	s.SendCondVar = sync.NewCond(&s.Lock)
	s.SendWaiter.Add(1)
	s.DrainedLink.Init(s)
	s.DrainedProc = -1
	s.IsLinked = false

	// Start the sender
	go spccSender(ops, s)

	// Receive the requests
	for {
		ret := spccRecvOne(ops, s)
		if ret != 0 {
			break
		}
	}

	// Update a few stats and signal the sender
	s.Lock.Lock()
	if s.IsLinked {
		spccRemoveFromDrainedList(ops, s)
	}
	AtomicSubUint64(&ops.SpccCreditUsed, uint64(s.Credit))
	AtomicSubUint64(&ops.SpccNumPending, uint64(s.NumPending))
	s.NumPending = 0
	s.Demand = 0
	s.Closed = true
	s.SendCondVar.Signal()
	s.Lock.Unlock()

	// Cleanup
	AtomicSubUint64(&ops.SpccNumSess, 1)
	s.SendWaiter.Wait()

	// Re-Initialize credits, if there are no clients
	if AtomicGetUint64(&ops.SpccNumSess) == 0 {
		AtomicSetUint64(&ops.SpccCreditUsed, 0)
		AtomicSetUint64(&ops.SpccCreditPool, uint64(runtime.GOMAXPROCS(0)))
		AtomicSetUint64(&ops.SpccCreditDs, 0)
	}
}

func spccListener(ops *SpccOps, listenWaiter *sync.WaitGroup) {

	// Initialize the drained lists
	for i := uint64(0); i < SpccMaxProcs; i++ {
		ops.SpccDrained[i].ListH.Init()
		ops.SpccDrained[i].ListL.Init()
	}

	// Initialize the state
	AtomicSetUint64(&ops.SpccNumSess, 0)
	AtomicSetUint64(&ops.SpccNumDrained, 0)
	AtomicSetUint64(&ops.SpccCreditPool, uint64(runtime.GOMAXPROCS(0)))
	AtomicSetUint64(&ops.SpccCreditUsed, 0)
	AtomicSetUint64(&ops.SpccCreditDs, 0)
	AtomicSetUint64(&ops.SpccNumActive, 0)
	AtomicSetUint64(&ops.SpccNumPending, 0)
	ops.SpccCreditCarry = 0.0

	AtomicSetUint64(&ops.SpccStatCUpdateRx, 0)
	AtomicSetUint64(&ops.SpccStatECreditTx, 0)
	AtomicSetUint64(&ops.SpccStatCreditTx, 0)
	AtomicSetUint64(&ops.SpccStatReqRx, 0)
	AtomicSetUint64(&ops.SpccStatReqDropped, 0)
	AtomicSetUint64(&ops.SpccStatRespTx, 0)

	ops.SpccCtlState = SpccCtlStatePrepareMicroExp
	ops.SpccNextUpdate = MicroTime()
	ops.SpccLastWakeup = MicroTime()
	ops.SpccMicroExpId = 0
	ops.SpccNumMicroExps = 0
	spccGenMicroExpDirs(ops)
	ops.SpccOrigCp = AtomicGetUint64(&ops.SpccCreditPool)

	// Open the server connection
	laddr := &net.TCPAddr{
		IP:   net.IPv4zero,
		Port: SrpcPort,
	}
	l, err := net.ListenTCP("tcp", laddr)
	if err != nil {
		panic(err)
	}
	defer l.Close()

	// Signal the enabler that initialization is complete
	listenWaiter.Done()

	for {
		// Accept new connection
		conn, err := l.AcceptTCP()
		if err != nil {
			fmt.Println("Failed to accept connection:", err)
			conn.Close()
			continue
		}

		// Handle the new connection
		go spccServer(ops, conn)
	}
}

// Helper function to generate the microexperiment directions
func spccGenMicroExpDirs(ops *SpccOps) {
	doIncr := rand.Intn(2) == 1
	if doIncr {
		ops.SpccMicroExpDirs[1] = 1
	} else {
		ops.SpccMicroExpDirs[1] = -1
	}
	ops.SpccMicroExpDirs[2] = -ops.SpccMicroExpDirs[1]
}

type SpccOps struct {
	// Request handler
	SpccHandler SrpcFn
	// Number of client sessions
	SpccNumSess uint64
	// Number of drained client sessions
	SpccNumDrained uint64
	// Number of clients with non-zero credits
	SpccNumActive uint64
	// Global credit pool
	SpccCreditPool uint64
	// Global credit pool used
	SpccCreditUsed uint64
	// Downstream credit for multi-hierarchy
	SpccCreditDs uint64
	// Number of pending requests (whose responses are not yet sent)
	SpccNumPending uint64
	// Partial credit carry
	SpccCreditCarry float64

	// Statistics
	SpccStatCUpdateRx  uint64
	SpccStatECreditTx  uint64
	SpccStatCreditTx   uint64
	SpccStatReqRx      uint64
	SpccStatReqDropped uint64
	SpccStatRespTx     uint64

	// (P)erformance-oriented (C)ongestion (C)ontrol state
	SpccCtlLock        sync.Mutex
	SpccLastWakeup     uint64
	SpccNextUpdate     uint64
	SpccCtlState	   SpccCtlStateType
	SpccInReqs	       [SpccMaxNumMicroExps+1]uint64
	SpccOutResps	   [SpccMaxNumMicroExps+1]uint64
	SpccGoodOutResps   [SpccMaxNumMicroExps+1]uint64
	SpccDropReqs	   [SpccMaxNumMicroExps+1]uint64
	SpccStartTs        [SpccMaxNumMicroExps+1]uint64
	SpccEndTs          [SpccMaxNumMicroExps+1]uint64
	SpccQDelay	       [SpccMaxNumMicroExps+1]uint64
	SpccMemAccesses    [SpccMaxNumMicroExps+1]uint64
	SpccEnergyConsumed [SpccMaxNumMicroExps+1]float64
	SpccMicroExpId     uint64
	SpccNumMicroExps   uint64
	SpccMicroExpDirs   [SpccMaxNumMicroExps+1]int
	SpccOrigCp         uint64

	// Per-core drained session lists
	SpccDrained [SpccMaxProcs]SpccDrainedList

	// Lock
	Lock sync.Mutex

	// Memory pool for datapath allocations
	SpccCtxPool  sync.Pool
	SrpcCtxPool sync.Pool
}

func (ops *SpccOps) SrpcEnable(handler SrpcFn) int {

	// Set the request handler
	ops.Lock.Lock()
	if ops.SpccHandler != nil {
		ops.Lock.Unlock()
		return -1
	}
	ops.SpccHandler = handler
	ops.Lock.Unlock()

	// Initialize the memory pools
	ops.SpccCtxPool = sync.Pool{
		New: func() any {
			return new(SpccCtx)
		},
	}
	ops.SrpcCtxPool = sync.Pool{
		New: func() any {
			return new(SrpcCtx)
		},
	}

	// Start the listener thread (wait till the init is done)
	var listenWaiter sync.WaitGroup
	listenWaiter.Add(1)
	go spccListener(ops, &listenWaiter)
	listenWaiter.Wait()

	return 0
}

func (ops *SpccOps) SrpcDrop() {}

func (ops *SpccOps) SrpcStatCUpdateRx() uint64 {
	return AtomicGetUint64(&ops.SpccStatCUpdateRx)
}

func (ops *SpccOps) SrpcStatECreditTx() uint64 {
	return AtomicGetUint64(&ops.SpccStatECreditTx)
}

func (ops *SpccOps) SrpcStatCreditTx() uint64 {
	return AtomicGetUint64(&ops.SpccStatCreditTx)
}

func (ops *SpccOps) SrpcStatReqRx() uint64 {
	return AtomicGetUint64(&ops.SpccStatReqRx)
}

func (ops *SpccOps) SrpcStatReqDropped() uint64 {
	return AtomicGetUint64(&ops.SpccStatReqDropped)
}

func (ops *SpccOps) SrpcStatRespTx() uint64 {
	return AtomicGetUint64(&ops.SpccStatRespTx)
}
