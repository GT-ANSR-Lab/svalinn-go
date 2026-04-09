package protego

import (
	"fmt"
	"net"
	"runtime"
	"sync"

	. "ovldctlrpc/common"
	. "utils"

	"perf"

	"github.com/kelindar/bitmap"
)

// Maximum number of Go Procs (max. concurrently running go routines)
const (
	SpgMaxProcs uint64 = 256
)

// Maximum supported window size
const (
	SpgMaxWindowExp uint64 = 6
	SpgMaxWindow    uint64 = 1 << SpgMaxWindowExp
)

// Specialized server-side state for a single RPC request
type SpgCtx struct {
	Cmn    *SrpcCtx
	TsSent uint64
}

// Drained sessions list
//
// Must be cacheline aligned, as we want one such structure per CPU core
type SpgDrainedList struct {
	Lock  sync.Mutex
	ListH ListHead[SpgSession]
	ListL ListHead[SpgSession]
	_     [8]byte // padding must be verified manually
}

// Specialized server-side state for a single RPC client
type SpgSession struct {
	Cmn            *SrpcSession
	Id             int
	NumPending     int
	Closed         bool
	Lock           sync.Mutex
	AvailSlots     bitmap.Bitmap
	CompletedSlots bitmap.Bitmap
	Slots          [SpgMaxWindow]*SpgCtx
	SendCondVar    *sync.Cond
	SendWaiter     sync.WaitGroup

	DrainedLink ListNode[SpgSession]
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

func spgGetSlot(ops *SpgOps, s *SpgSession) int {
	s.Lock.Lock()
	defer s.Lock.Unlock()

	slot, ok := s.AvailSlots.Min()
	if !ok {
		return -1
	}
	s.AvailSlots.Remove(slot)
	s.Slots[slot] = ops.SpgCtxPool.Get().(*SpgCtx)
	s.Slots[slot].Cmn = ops.SrpcCtxPool.Get().(*SrpcCtx)
	s.Slots[slot].Cmn.Ext = s.Slots[slot]
	s.Slots[slot].Cmn.S = s.Cmn
	s.Slots[slot].Cmn.Idx = int(slot)
	s.Slots[slot].Cmn.DsCredit = 0
	s.Slots[slot].Cmn.Drop = false

	return int(slot)
}

func spgPutSlot(ops *SpgOps, s *SpgSession, slot int) {
	s.Lock.Lock()
	defer s.Lock.Unlock()

	// Break cyclic references before returning the objects to their pools.
	// Without this, each pool entry would keep its paired object reachable
	// (and transitively its SrpcSession / big ReqBuf+RespBuf), pinning memory
	// until both pool slots are re-issued.
	c := s.Slots[slot]
	cmn := c.Cmn
	cmn.Ext = nil
	cmn.S = nil
	c.Cmn = nil

	ops.SrpcCtxPool.Put(cmn)
	ops.SpgCtxPool.Put(c)
	s.Slots[slot] = nil
	s.AvailSlots.Set(uint32(slot))
}

func spgUpdateCredit(ops *SpgOps, s *SpgSession, reqDropped bool) {
	creditPool := int(AtomicGetUint64(&ops.SpgCreditPool))
	creditDs := int(AtomicGetUint64(&ops.SpgCreditDs))
	creditUsed := int(AtomicGetUint64(&ops.SpgCreditUsed))
	numSess := int(AtomicGetUint64(&ops.SpgNumSess))
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
	s.Credit = Min(s.Credit, int(SpgMaxWindow)-1)
	s.Credit = Min(s.Credit, s.NumPending+int(s.Demand)+maxOverprovision)

	creditDiff := s.Credit - oldCredit
	AtomicAddUint64(&ops.SpgCreditUsed, uint64(creditDiff))
}

func spgSendCompletionVector(ops *SpgOps, s *SpgSession, vec *bitmap.Bitmap) int {

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
			flags |= uint8(PgSFlagDrop)
		}

		// Prepare the header
		shdr := SpgHdr{
			Magic:  PgRespMagic,
			Op:     PgOpCall,
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
		AtomicAddUint64(&ops.SpgStatRespTx, 1)
	failed:
		AtomicSubUint64(&ops.SpgNumPending, 1)

		// Free the slot for reuse
		spgPutSlot(ops, s, int(idx))
	})

	return 0
}

func spgSendECredit(ops *SpgOps, s *SpgSession) int {
	shdr := SpgHdr{
		Magic:  PgRespMagic,
		Op:     PgOpCredit,
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

	AtomicAddUint64(&ops.SpgStatECreditTx, 1)
	return 0
}

func spgSender(ops *SpgOps, s *SpgSession) {
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
			spgRemoveFromDrainedList(ops, s)
		}

		drainedProc := s.DrainedProc
		numResp := tmp.Count()
		s.NumPending -= numResp
		oldCredit := s.Credit
		spgUpdateCredit(ops, s, reqDropped)
		credit := s.Credit
		creditIssued := Max(0, credit-oldCredit+numResp)
		AtomicAddUint64(&ops.SpgStatCreditTx, uint64(creditIssued))

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
			ret := spgSendECredit(ops, s)
			if ret != 0 {
				goto close
			}
			continue
		}

		// Send the responses
		_ = spgSendCompletionVector(ops, s, &tmp)

		if credit == 0 &&
			drainedProc == -1 &&
			s.AvailSlots.Count() == int(SpgMaxWindow) {

			procId := runtime.GetProcId()
			s.Lock.Lock()
			drainedList := &ops.SpgDrained[procId]
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
			AtomicAddUint64(&ops.SpgNumDrained, 1)
			s.Lock.Unlock()
		}
	}

close:
	// Wait for inflight requests to complete
	s.Lock.Lock()
	for !s.Closed || s.AvailSlots.Count()+s.CompletedSlots.Count() < int(SpgMaxWindow) {
		s.SendCondVar.Wait()
	}
	spgRemoveFromDrainedList(ops, s)
	s.Lock.Unlock()

	// Cleanup any remaining slots
	for i := uint64(0); i < SpgMaxWindow; i++ {
		if s.Slots[i] != nil {
			spgPutSlot(ops, s, int(i))
		}
	}

	// Signal the server thread that we are done
	s.SendWaiter.Done()
}

func spgRemoveFromDrainedList(ops *SpgOps, s *SpgSession) {

	if s.DrainedProc == -1 {
		return
	}

	drainedList := &ops.SpgDrained[s.DrainedProc]

	drainedList.Lock.Lock()
	if s.IsLinked {
		s.DrainedLink.Del()
		s.IsLinked = false
		AtomicSubUint64(&ops.SpgNumDrained, 1)
	}
	drainedList.Lock.Unlock()
	s.DrainedProc = -1
}

func spgChooseDrainedH(ops *SpgOps, procID int) *SpgSession {
	now := MicroTime()
	demandTimeout := uint64(Max(int(CpgMaxClientDelayUs)-int(SpgRttUs), int(0)))
	drainedList := &ops.SpgDrained[procID]

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
	AtomicSubUint64(&ops.SpgNumDrained, 1)

	return s
}

func spgChooseDrainedL(ops *SpgOps, procID int) *SpgSession {

	drainedList := &ops.SpgDrained[procID]

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
	AtomicSubUint64(&ops.SpgNumDrained, 1)

	return s
}

func spgWakeUpDrainedSession(ops *SpgOps, numSess uint64) {
	procID := runtime.GetProcId()
	maxProcs := runtime.GOMAXPROCS(0)

	for numSess > 0 {
		// First check for a drained session on high priority queue
		// on the local proc
		s := spgChooseDrainedH(ops, procID)

		// Then check for a drained session on high priority queue
		// on remote procs
		i := (procID + 1) % maxProcs
		for s == nil && i != procID {
			s = spgChooseDrainedH(ops, i)
			i = (i + 1) % maxProcs
		}

		// Then check for a drained session on low priority queues
		if s == nil {
			// First local
			s = spgChooseDrainedL(ops, procID)

			// Then remote
			i := (procID + 1) % maxProcs
			for s == nil && i != procID {
				s = spgChooseDrainedL(ops, i)
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

		AtomicAddUint64(&ops.SpgCreditUsed, 1)
		numSess--
	}
}

func spgDecrCreditPool(ops *SpgOps, delay uint64) uint64 {

	creditPool := int64(AtomicGetUint64(&ops.SpgCreditPool))
	numSess := AtomicGetUint64(&ops.SpgNumSess)

	decr := int64(Max(uint64(float64(numSess)*SpgAI), 1))
	creditPool -= decr
	ops.SpgCreditCarry = 0.0

	if creditPool < int64(runtime.GOMAXPROCS(0)) {
		creditPool = int64(runtime.GOMAXPROCS(0))
	}
	maxCp := int64(numSess << SpgMaxWindowExp)
	if creditPool > maxCp {
		creditPool = maxCp
	}

	return uint64(creditPool)
}

func spgIncrCreditPool(ops *SpgOps, delay uint64) uint64 {

	creditPool := AtomicGetUint64(&ops.SpgCreditPool)
	numSess := AtomicGetUint64(&ops.SpgNumSess)

	ops.SpgCreditCarry += Max(float64(numSess)*SpgAI, 1.0)
	if ops.SpgCreditCarry >= 1.0 {
		newCreditInt := uint64(ops.SpgCreditCarry)
		creditPool += newCreditInt
		ops.SpgCreditCarry -= float64(newCreditInt)
	}

	creditPool = Max(creditPool, uint64(runtime.GOMAXPROCS(0)))
	creditPool = Min(creditPool, numSess<<SpgMaxWindowExp)

	return creditPool
}

func spgUpdateCreditPool(ops *SpgOps) {

	var now uint64
	var creditUsed uint64
	var newCp uint64
	var creditUnused uint64
	var inCnt uint64
	var outCnt uint64
	var lastInCnt uint64
	var lastOutCnt uint64
	var tDiff uint64

	if !ops.SpgCmLock.TryLock() {
		return
	}

	now = MicroTime()
	tDiff = now - ops.SpgCmLastUpdate

	if tDiff < SpgCmP99Rtt {
		ops.SpgCmLock.Unlock()
		return
	}
	if !ops.SpgCmResetStat {
		AtomicSetUint64(&ops.SpgCmInCnt, 0)
		AtomicSetUint64(&ops.SpgCmOutCnt, 0)
		AtomicSetUint64(&ops.SpgCmDropCnt, 0)
		ops.SpgCmResetStat = true
	}
	if tDiff < SpgCmUpdateInterval {
		ops.SpgCmLock.Unlock()
		return
	}

	ops.SpgCmLastUpdate = now
	ops.SpgCmResetStat = false

	creditUsed = AtomicGetUint64(&ops.SpgCreditUsed)
	newCp = AtomicGetUint64(&ops.SpgCreditPool)
	inCnt = AtomicGetUint64(&ops.SpgCmInCnt)
	outCnt = AtomicGetUint64(&ops.SpgCmOutCnt)
	lastInCnt = AtomicGetUint64(&ops.SpgCmLastInCnt)
	lastOutCnt = AtomicGetUint64(&ops.SpgCmLastOutCnt)

	if inCnt == 0 {
		newCp = spgIncrCreditPool(ops, 0)
	} else if inCnt >= lastInCnt {
		// Incr phase
		if outCnt > lastOutCnt &&
		   SpgCmSlopeInv * (outCnt - lastOutCnt) >= (inCnt - lastInCnt) &&
		   creditUsed >= newCp {
			newCp = spgIncrCreditPool(ops, 0)
		} else {
			newCp = spgDecrCreditPool(ops, 0)
		}
	} else {
		// Decr phase
		if lastOutCnt > outCnt &&
		   SpgCmSlopeInv * (lastOutCnt - outCnt) >= (lastInCnt - inCnt) &&
		   creditUsed >= newCp {
			newCp = spgIncrCreditPool(ops, 0)
		} else {
			newCp = spgDecrCreditPool(ops, 0)
		}
	}

	if newCp > creditUsed {
		creditUnused = newCp - creditUsed
	} else {
		creditUnused = 0
	}
	spgWakeUpDrainedSession(ops, creditUnused)
	AtomicSetUint64(&ops.SpgCreditPool, newCp)

	AtomicSetUint64(&ops.SpgCmLastInCnt, inCnt)
	AtomicSetUint64(&ops.SpgCmLastOutCnt, outCnt)
	AtomicSetUint64(&ops.SpgCmInCnt, 0)
	AtomicSetUint64(&ops.SpgCmOutCnt, 0)
	AtomicSetUint64(&ops.SpgCmDropCnt, 0)

	ops.SpgCmLock.Unlock()
}

func spgHandleReqDrop(ops *SpgOps, s *SpgSession, delay uint64) {
	spgUpdateCreditPool(ops)
	AtomicAddUint64(&ops.SpgCmDropCnt, 1)
	AtomicAddUint64(&ops.SpgStatReqDropped, 1)
}

func spgWorker(ops *SpgOps, s *SpgSession, c *SpgCtx) {
	c.Cmn.Drop = false
	ops.SpgHandler(c.Cmn)

	if !c.Cmn.Drop {
		AtomicSetUint64(&ops.SpgCreditDs, c.Cmn.DsCredit)
		AtomicAddUint64(&ops.SpgCmOutCnt, 1)
	} else {
		AtomicAddUint64(&ops.SpgCmDropCnt, 1)
		AtomicAddUint64(&ops.SpgStatReqDropped, 1)
	}

	spgUpdateCreditPool(ops)

	// Signal the sender
	s.Lock.Lock()
	s.CompletedSlots.Set(uint32(c.Cmn.Idx))
	s.SendCondVar.Signal()
	s.Lock.Unlock()
}

func spgRecvOne(ops *SpgOps, s *SpgSession) int {

	var chdr CpgHdr
	var tmpBuf [SrpcBufSize]byte
	var creditDiff int
	var ctx *SpgCtx
	var procID int

again:
	// Read the request header
	n, err := ReadFull(s.Cmn.C, ToBytes(&chdr))
	if err != nil {
		if n != 0 {
			fmt.Println("Failed to read the request header")
		}
		return -1
	}

	if chdr.Magic != PgReqMagic {
		fmt.Println("Got invalid magic")
		return -1
	}
	if chdr.Len > SrpcBufSize {
		fmt.Println("Request too large")
		return -1
	}

	switch chdr.Op {
	case PgOpCall:
		AtomicAddUint64(&ops.SpgStatReqRx, 1)

		// Find an available slot
		idx := spgGetSlot(ops, s)
		if idx < 0 {
			ReadFull(s.Cmn.C, tmpBuf[:chdr.Len])
			AtomicAddUint64(&ops.SpgCmDropCnt, 1)
			AtomicAddUint64(&ops.SpgStatReqDropped, 1)
			goto again
		}
		ctx = s.Slots[idx]

		// Retrieve the payload
		n, err = ReadFull(s.Cmn.C, ctx.Cmn.ReqBuf[:chdr.Len])
		if err != nil {
			if n != 0 {
				fmt.Println("Failed to read the request payload")
			}
			spgPutSlot(ops, s, idx)
			return -1
		}
		ctx.Cmn.ReqLen = chdr.Len
		ctx.Cmn.RespLen = 0
		ctx.Cmn.Id = chdr.Id
		ctx.TsSent = chdr.TsSent

		s.Lock.Lock()

		s.Demand = chdr.Demand
		spgRemoveFromDrainedList(ops, s)
		s.NumPending++
		// Adjust the credits if demand changed
		if s.Credit > s.NumPending+int(s.Demand) {
			creditDiff = s.Credit - (s.NumPending + int(s.Demand))
			s.Credit = s.NumPending + int(s.Demand)
			AtomicSubUint64(&ops.SpgCreditUsed, uint64(creditDiff))
		}
		AtomicAddUint64(&ops.SpgNumPending, 1)
		AtomicAddUint64(&ops.SpgCmInCnt, 1)

		// Perform AQM
		maxQueueDelay := perf.GetQueueDelayMax()
		maxQueueDelay = maxQueueDelay / 1000
		if maxQueueDelay >= SpgLatencyBudget {
			spgHandleReqDrop(ops, s, maxQueueDelay)
			ctx.Cmn.Drop = true
			s.CompletedSlots.Set(uint32(idx))
			s.SendCondVar.Signal()
			s.Lock.Unlock()
			goto again
		}

		s.Lock.Unlock()

		// Spawn the worker to handle the request
		go spgWorker(ops, s, ctx)
	case PgOpCredit:
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
			drainedList := &ops.SpgDrained[procID]
			drainedList.Lock.Lock()
			drainedList.ListL.AddTail(&s.DrainedLink)
			s.IsLinked = true
			drainedList.Lock.Unlock()
			s.DrainedProc = procID
			AtomicAddUint64(&ops.SpgNumDrained, 1)
			s.Advertised = 0
		}

		// Adjust the credits if demand changed
		if s.Credit > s.NumPending+int(s.Demand) {
			creditDiff = s.Credit - (s.NumPending + int(s.Demand))
			s.Credit = s.NumPending + int(s.Demand)
			AtomicSubUint64(&ops.SpgCreditUsed, uint64(creditDiff))
		}

		// Signal the sender
		s.SendCondVar.Signal()
		s.Lock.Unlock()

		AtomicAddUint64(&ops.SpgStatCUpdateRx, 1)
	default:
		fmt.Println("Invalid op")
		return -1
	}

	return 0
}

func spgServer(ops *SpgOps, conn *net.TCPConn) {
	defer conn.Close()

	// Initialize the session state
	s := &SpgSession{}
	s.Cmn = &SrpcSession{}
	s.Cmn.Ext = s
	s.Cmn.C = conn
	s.Id = int(AtomicAddUint64(&ops.SpgNumSess, 1))
	s.NumPending = 0
	s.Closed = false
	s.AvailSlots.Grow(uint32(SpgMaxWindow))
	s.CompletedSlots.Grow(uint32(SpgMaxWindow))
	for i := uint64(0); i < SpgMaxWindow; i++ {
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
	go spgSender(ops, s)

	// Receive the requests
	for {
		ret := spgRecvOne(ops, s)
		if ret != 0 {
			break
		}
	}

	// Update a few stats and signal the sender
	s.Lock.Lock()
	if s.IsLinked {
		spgRemoveFromDrainedList(ops, s)
	}
	AtomicSubUint64(&ops.SpgCreditUsed, uint64(s.Credit))
	AtomicSubUint64(&ops.SpgNumPending, uint64(s.NumPending))
	s.NumPending = 0
	s.Demand = 0
	s.Closed = true
	s.SendCondVar.Signal()
	s.Lock.Unlock()

	// Cleanup
	AtomicSubUint64(&ops.SpgNumSess, 1)
	s.SendWaiter.Wait()

	// Re-Initialize credits, if there are no clients
	if AtomicGetUint64(&ops.SpgNumSess) == 0 {
		AtomicSetUint64(&ops.SpgCreditUsed, 0)
		AtomicSetUint64(&ops.SpgCreditPool, uint64(runtime.GOMAXPROCS(0)))
		AtomicSetUint64(&ops.SpgCreditDs, 0)
	}

	// Break the SrpcSession <-> SpgSession cycle. Both objects become
	// unreachable when this function returns, but clearing the cross-links
	// ensures the GC can collect them independently and turns any stale
	// access into a nil-panic instead of silent garbage.
	s.Cmn.Ext = nil
	s.Cmn = nil
}

func spgListener(ops *SpgOps, listenWaiter *sync.WaitGroup) {

	// Initialize the drained lists
	for i := uint64(0); i < SpgMaxProcs; i++ {
		ops.SpgDrained[i].ListH.Init()
		ops.SpgDrained[i].ListL.Init()
	}

	// Initialize the state
	AtomicSetUint64(&ops.SpgNumSess, 0)
	AtomicSetUint64(&ops.SpgNumDrained, 0)
	AtomicSetUint64(&ops.SpgCreditPool, uint64(runtime.GOMAXPROCS(0)))
	AtomicSetUint64(&ops.SpgCreditUsed, 0)
	AtomicSetUint64(&ops.SpgCreditDs, 0)
	AtomicSetUint64(&ops.SpgNumActive, 0)
	AtomicSetUint64(&ops.SpgNumPending, 0)
	ops.SpgCreditCarry = 0.0

	AtomicSetUint64(&ops.SpgStatCUpdateRx, 0)
	AtomicSetUint64(&ops.SpgStatECreditTx, 0)
	AtomicSetUint64(&ops.SpgStatCreditTx, 0)
	AtomicSetUint64(&ops.SpgStatReqRx, 0)
	AtomicSetUint64(&ops.SpgStatReqDropped, 0)
	AtomicSetUint64(&ops.SpgStatRespTx, 0)

	AtomicSetUint64(&ops.SpgCmInCnt, 0)
	AtomicSetUint64(&ops.SpgCmOutCnt, 0)
	AtomicSetUint64(&ops.SpgCmDropCnt, 0)
	AtomicSetUint64(&ops.SpgCmLastInCnt, 0)
	AtomicSetUint64(&ops.SpgCmLastOutCnt, 0)
	ops.SpgCmLastUpdate = MicroTime()

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
		go spgServer(ops, conn)
	}
}

type SpgOps struct {
	// Request handler
	SpgHandler SrpcFn
	// Number of client sessions
	SpgNumSess uint64
	// Number of drained client sessions
	SpgNumDrained uint64
	// Number of clients with non-zero credits
	SpgNumActive uint64
	// Global credit pool
	SpgCreditPool uint64
	// Global credit pool used
	SpgCreditUsed uint64
	// Downstream credit for multi-hierarchy
	SpgCreditDs uint64
	// Number of pending requests (whose responses are not yet sent)
	SpgNumPending uint64
	// Partial credit carry
	SpgCreditCarry float64

	// Statistics
	SpgStatCUpdateRx  uint64
	SpgStatECreditTx  uint64
	SpgStatCreditTx   uint64
	SpgStatReqRx      uint64
	SpgStatReqDropped uint64
	SpgStatRespTx     uint64

	// Throughput-based credit management state
	SpgCmLock       sync.Mutex
	SpgCmInCnt      uint64
	SpgCmOutCnt     uint64
	SpgCmDropCnt    uint64
	SpgCmLastInCnt  uint64
	SpgCmLastOutCnt uint64
	SpgCmLastUpdate uint64
	SpgCmResetStat  bool

	// Per-core drained session lists
	SpgDrained [SpgMaxProcs]SpgDrainedList

	// Lock
	Lock sync.Mutex

	// Memory pool for datapath allocations
	SpgCtxPool  sync.Pool
	SrpcCtxPool sync.Pool
}

func (ops *SpgOps) SrpcEnable(handler SrpcFn) int {

	// Set the request handler
	ops.Lock.Lock()
	if ops.SpgHandler != nil {
		ops.Lock.Unlock()
		return -1
	}
	ops.SpgHandler = handler
	ops.Lock.Unlock()

	// Initialize the memory pools
	ops.SpgCtxPool = sync.Pool{
		New: func() any {
			return new(SpgCtx)
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
	go spgListener(ops, &listenWaiter)
	listenWaiter.Wait()

	return 0
}

func (ops *SpgOps) SrpcDrop() {}

func (ops *SpgOps) SrpcStatCUpdateRx() uint64 {
	return AtomicGetUint64(&ops.SpgStatCUpdateRx)
}

func (ops *SpgOps) SrpcStatECreditTx() uint64 {
	return AtomicGetUint64(&ops.SpgStatECreditTx)
}

func (ops *SpgOps) SrpcStatCreditTx() uint64 {
	return AtomicGetUint64(&ops.SpgStatCreditTx)
}

func (ops *SpgOps) SrpcStatReqRx() uint64 {
	return AtomicGetUint64(&ops.SpgStatReqRx)
}

func (ops *SpgOps) SrpcStatReqDropped() uint64 {
	return AtomicGetUint64(&ops.SpgStatReqDropped)
}

func (ops *SpgOps) SrpcStatRespTx() uint64 {
	return AtomicGetUint64(&ops.SpgStatRespTx)
}
