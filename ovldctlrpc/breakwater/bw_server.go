package breakwater

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
	SbwMaxProcs uint64 = 256
)

// Maximum supported window size
const (
	SbwMaxWindowExp uint64 = 6
	SbwMaxWindow    uint64 = 1 << SbwMaxWindowExp
)

// Specialized server-side state for a single RPC request
type SbwCtx struct {
	Cmn    *SrpcCtx
	TsSent uint64
}

// Drained sessions list
//
// Must be cacheline aligned, as we want one such structure per CPU core
type SbwDrainedList struct {
	Lock  sync.Mutex
	ListH ListHead[SbwSession]
	ListL ListHead[SbwSession]
	_     [8]byte // padding must be verified manually
}

// Specialized server-side state for a single RPC client
type SbwSession struct {
	Cmn            *SrpcSession
	Id             int
	NumPending     int
	Closed         bool
	Lock           sync.Mutex
	AvailSlots     bitmap.Bitmap
	CompletedSlots bitmap.Bitmap
	Slots          [SbwMaxWindow]*SbwCtx
	SendCondVar    *sync.Cond
	SendWaiter     sync.WaitGroup

	DrainedLink ListNode[SbwSession]
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

func sbwGetSlot(ops *SbwOps, s *SbwSession) int {
	s.Lock.Lock()
	defer s.Lock.Unlock()

	slot, ok := s.AvailSlots.Min()
	if !ok {
		return -1
	}
	s.AvailSlots.Remove(slot)
	s.Slots[slot] = ops.SbwCtxPool.Get().(*SbwCtx)
	s.Slots[slot].Cmn = ops.SrpcCtxPool.Get().(*SrpcCtx)
	s.Slots[slot].Cmn.Ext = s.Slots[slot]
	s.Slots[slot].Cmn.S = s.Cmn
	s.Slots[slot].Cmn.Idx = int(slot)
	s.Slots[slot].Cmn.DsCredit = 0
	s.Slots[slot].Cmn.Drop = false

	return int(slot)
}

func sbwPutSlot(ops *SbwOps, s *SbwSession, slot int) {
	s.Lock.Lock()
	defer s.Lock.Unlock()

	ops.SrpcCtxPool.Put(s.Slots[slot].Cmn)
	ops.SbwCtxPool.Put(s.Slots[slot])
	s.Slots[slot] = nil
	s.AvailSlots.Set(uint32(slot))
}

func sbwUpdateCredit(ops *SbwOps, s *SbwSession, reqDropped bool) {
	creditPool := int(AtomicGetUint64(&ops.SbwCreditPool))
	creditDs := int(AtomicGetUint64(&ops.SbwCreditDs))
	creditUsed := int(AtomicGetUint64(&ops.SbwCreditUsed))
	numSess := int(AtomicGetUint64(&ops.SbwNumSess))
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
	s.Credit = Min(s.Credit, int(SbwMaxWindow)-1)
	s.Credit = Min(s.Credit, s.NumPending+int(s.Demand)+maxOverprovision)

	creditDiff := s.Credit - oldCredit
	AtomicAddUint64(&ops.SbwCreditUsed, uint64(creditDiff))
}

func sbwSendCompletionVector(ops *SbwOps, s *SbwSession, vec *bitmap.Bitmap) int {

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
			flags |= uint8(BwSFlagDrop)
		}

		// Prepare the header
		shdr := SbwHdr{
			Magic:  BwRespMagic,
			Op:     BwOpCall,
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
		AtomicAddUint64(&ops.SbwStatRespTx, 1)
	failed:
		AtomicSubUint64(&ops.SbwNumPending, 1)

		// Free the slot for reuse
		sbwPutSlot(ops, s, int(idx))

	})

	return 0
}

func sbwSendECredit(ops *SbwOps, s *SbwSession) int {
	shdr := SbwHdr{
		Magic:  BwRespMagic,
		Op:     BwOpCredit,
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

	AtomicAddUint64(&ops.SbwStatECreditTx, 1)
	return 0
}

func sbwSender(ops *SbwOps, s *SbwSession) {
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
			sbwRemoveFromDrainedList(ops, s)
		}

		drainedProc := s.DrainedProc
		numResp := tmp.Count()
		s.NumPending -= numResp
		oldCredit := s.Credit
		sbwUpdateCredit(ops, s, reqDropped)
		credit := s.Credit
		creditIssued := Max(0, credit-oldCredit+numResp)
		AtomicAddUint64(&ops.SbwStatCreditTx, uint64(creditIssued))

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
			ret := sbwSendECredit(ops, s)
			if ret != 0 {
				goto close
			}
			continue
		}

		// Send the responses
		_ = sbwSendCompletionVector(ops, s, &tmp)

		if credit == 0 &&
			drainedProc == -1 &&
			s.AvailSlots.Count() == int(SbwMaxWindow) {

			procId := runtime.GetProcId()
			s.Lock.Lock()
			drainedList := &ops.SbwDrained[procId]
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
			AtomicAddUint64(&ops.SbwNumDrained, 1)
			s.Lock.Unlock()
		}
	}

close:
	// Wait for inflight requests to complete
	s.Lock.Lock()
	for !s.Closed || s.AvailSlots.Count()+s.CompletedSlots.Count() < int(SbwMaxWindow) {
		s.SendCondVar.Wait()
	}
	sbwRemoveFromDrainedList(ops, s)
	s.Lock.Unlock()

	// Cleanup any remaining slots
	for i := uint64(0); i < SbwMaxWindow; i++ {
		if s.Slots[i] != nil {
			sbwPutSlot(ops, s, int(i))
		}
	}

	// Signal the server thread that we are done
	s.SendWaiter.Done()
}

func sbwRemoveFromDrainedList(ops *SbwOps, s *SbwSession) {

	if s.DrainedProc == -1 {
		return
	}

	drainedList := &ops.SbwDrained[s.DrainedProc]

	drainedList.Lock.Lock()
	if s.IsLinked {
		s.DrainedLink.Del()
		s.IsLinked = false
		AtomicSubUint64(&ops.SbwNumDrained, 1)
	}
	drainedList.Lock.Unlock()
	s.DrainedProc = -1
}

func sbwChooseDrainedH(ops *SbwOps, procID int) *SbwSession {
	now := MicroTime()
	demandTimeout := uint64(Max(int(CbwMaxClientDelayUs)-int(SbwRttUs), int(0)))
	drainedList := &ops.SbwDrained[procID]

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
	AtomicSubUint64(&ops.SbwNumDrained, 1)

	return s
}

func sbwChooseDrainedL(ops *SbwOps, procID int) *SbwSession {

	drainedList := &ops.SbwDrained[procID]

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
	AtomicSubUint64(&ops.SbwNumDrained, 1)

	return s
}

func sbwWakeUpDrainedSession(ops *SbwOps, numSess uint64) {
	procID := runtime.GetProcId()
	maxProcs := runtime.GOMAXPROCS(0)

	for numSess > 0 {
		// First check for a drained session on high priority queue
		// on the local proc
		s := sbwChooseDrainedH(ops, procID)

		// Then check for a drained session on high priority queue
		// on remote procs
		i := (procID + 1) % maxProcs
		for s == nil && i != procID {
			s = sbwChooseDrainedH(ops, i)
			i = (i + 1) % maxProcs
		}

		// Then check for a drained session on low priority queues
		if s == nil {
			// First local
			s = sbwChooseDrainedL(ops, procID)

			// Then remote
			i := (procID + 1) % maxProcs
			for s == nil && i != procID {
				s = sbwChooseDrainedL(ops, i)
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

		AtomicAddUint64(&ops.SbwCreditUsed, 1)
		numSess--
	}
}

func sbwDecrCreditPool(ops *SbwOps, delay uint64) uint64 {

	creditPool := AtomicGetUint64(&ops.SbwCreditPool)
	numSess := AtomicGetUint64(&ops.SbwNumSess)

	alpha := float64(delay-SbwDelayTarget) / float64(SbwDelayTarget)
	alpha *= SbwMD
	alpha = Max(1.0-alpha, 0.5)

	creditPool = Min(uint64(float64(creditPool)*alpha), creditPool-1)
	ops.SbwCreditCarry = 0.0

	creditPool = Max(creditPool, uint64(runtime.GOMAXPROCS(0)))
	creditPool = Min(creditPool, numSess<<SbwMaxWindowExp)

	return creditPool
}

func sbwIncrCreditPool(ops *SbwOps, delay uint64) uint64 {

	creditPool := AtomicGetUint64(&ops.SbwCreditPool)
	numSess := AtomicGetUint64(&ops.SbwNumSess)

	ops.SbwCreditCarry += float64(numSess) * SbwAI
	if ops.SbwCreditCarry >= 1.0 {
		newCreditInt := uint64(ops.SbwCreditCarry)
		creditPool += newCreditInt
		ops.SbwCreditCarry -= float64(newCreditInt)
	}

	creditPool = Max(creditPool, uint64(runtime.GOMAXPROCS(0)))
	creditPool = Min(creditPool, numSess<<SbwMaxWindowExp)

	return creditPool
}

func sbwUpdateCreditPool(ops *SbwOps) {
	now := MicroTime()

	if now-ops.SbwLastCpUpdate < SbwRttUs {
		return
	}
	ops.SbwLastCpUpdate = now

	newCp := AtomicGetUint64(&ops.SbwCreditPool)
	creditUsed := AtomicGetUint64(&ops.SbwCreditUsed)

	maxQueueDelay := perf.GetQueueDelayMax()
	maxQueueDelay /= 1000
	if maxQueueDelay >= SbwDelayTarget {
		newCp = sbwDecrCreditPool(ops, maxQueueDelay)
	} else {
		newCp = sbwIncrCreditPool(ops, maxQueueDelay)
	}

	// newCp may have been decreased below creditUsed; avoid uint64 underflow
	var creditUnused uint64
	if newCp > creditUsed {
		creditUnused = newCp - creditUsed
	} else {
		creditUnused = 0
	}
	sbwWakeUpDrainedSession(ops, creditUnused)
	AtomicSetUint64(&ops.SbwCreditPool, newCp)
}

func sbwHandleReqDrop(ops *SbwOps, s *SbwSession, delay uint64) {
	now := MicroTime()

	if now-ops.SbwLastCpUpdate < SbwRttUs {
		return
	}
	ops.SbwLastCpUpdate = now
	newCp := sbwDecrCreditPool(ops, delay)
	AtomicSetUint64(&ops.SbwCreditPool, uint64(newCp))
}

func sbwWorker(ops *SbwOps, s *SbwSession, c *SbwCtx) {
	c.Cmn.Drop = false
	ops.SbwHandler(c.Cmn)

	if !c.Cmn.Drop {
		AtomicSetUint64(&ops.SbwCreditDs, c.Cmn.DsCredit)
		sbwUpdateCreditPool(ops)
	} else {
		AtomicAddUint64(&ops.SbwStatReqDropped, 1)
	}

	// Signal the sender
	s.Lock.Lock()
	s.CompletedSlots.Set(uint32(c.Cmn.Idx))
	s.SendCondVar.Signal()
	s.Lock.Unlock()
}

func sbwRecvOne(ops *SbwOps, s *SbwSession) int {

	var chdr CbwHdr
	var tmpBuf [SrpcBufSize]byte
	var creditDiff int
	var ctx *SbwCtx
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

	if chdr.Magic != BwReqMagic {
		fmt.Println("Got invalid magic")
		return -1
	}
	if chdr.Len > SrpcBufSize {
		fmt.Println("Request too large")
		return -1
	}

	switch chdr.Op {
	case BwOpCall:
		AtomicAddUint64(&ops.SbwStatReqRx, 1)

		// Find an available slot
		idx := sbwGetSlot(ops, s)
		if idx < 0 {
			ReadFull(s.Cmn.C, tmpBuf[:chdr.Len])
			AtomicAddUint64(&ops.SbwStatReqDropped, 1)
			goto again
		}
		ctx = s.Slots[idx]

		// Retrieve the payload
		n, err = ReadFull(s.Cmn.C, ctx.Cmn.ReqBuf[:chdr.Len])
		if err != nil {
			if n != 0 {
				fmt.Println("Failed to read the request payload")
			}
			sbwPutSlot(ops, s, idx)
			return -1
		}
		ctx.Cmn.ReqLen = chdr.Len
		ctx.Cmn.RespLen = 0
		ctx.Cmn.Id = chdr.Id
		ctx.TsSent = chdr.TsSent

		s.Lock.Lock()

		s.Demand = chdr.Demand
		sbwRemoveFromDrainedList(ops, s)
		s.NumPending++
		// Adjust the credits if demand changed
		if s.Credit > s.NumPending+int(s.Demand) {
			creditDiff = s.Credit - (s.NumPending + int(s.Demand))
			s.Credit = s.NumPending + int(s.Demand)
			AtomicSubUint64(&ops.SbwCreditUsed, uint64(creditDiff))
		}
		AtomicAddUint64(&ops.SbwNumPending, 1)

		// Perform AQM
		maxQueueDelay := perf.GetQueueDelayMax()
		maxQueueDelay = maxQueueDelay / 1000
		if maxQueueDelay >= SbwDropThresh {
			sbwHandleReqDrop(ops, s, maxQueueDelay)
			ctx.Cmn.Drop = true
			s.CompletedSlots.Set(uint32(idx))
			s.SendCondVar.Signal()
			s.Lock.Unlock()
			AtomicAddUint64(&ops.SbwStatReqDropped, 1)
			goto again
		}

		s.Lock.Unlock()

		// Spawn the worker to handle the request
		go sbwWorker(ops, s, ctx)
	case BwOpCredit:
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
			drainedList := &ops.SbwDrained[procID]
			drainedList.Lock.Lock()
			drainedList.ListL.AddTail(&s.DrainedLink)
			s.IsLinked = true
			drainedList.Lock.Unlock()
			s.DrainedProc = procID
			AtomicAddUint64(&ops.SbwNumDrained, 1)
			s.Advertised = 0
		}

		// Adjust the credits if demand changed
		if s.Credit > s.NumPending+int(s.Demand) {
			creditDiff = s.Credit - (s.NumPending + int(s.Demand))
			s.Credit = s.NumPending + int(s.Demand)
			AtomicSubUint64(&ops.SbwCreditUsed, uint64(creditDiff))
		}

		// Signal the sender
		s.SendCondVar.Signal()
		s.Lock.Unlock()

		AtomicAddUint64(&ops.SbwStatCUpdateRx, 1)
	default:
		fmt.Println("Invalid op")
		return -1
	}

	return 0
}

func sbwServer(ops *SbwOps, conn *net.TCPConn) {
	defer conn.Close()

	// Initialize the session state
	s := &SbwSession{}
	s.Cmn = &SrpcSession{}
	s.Cmn.Ext = s
	s.Cmn.C = conn
	s.Id = int(AtomicAddUint64(&ops.SbwNumSess, 1))
	s.NumPending = 0
	s.Closed = false
	s.AvailSlots.Grow(uint32(SbwMaxWindow))
	s.CompletedSlots.Grow(uint32(SbwMaxWindow))
	for i := uint64(0); i < SbwMaxWindow; i++ {
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
	go sbwSender(ops, s)

	// Receive the requests
	for {
		ret := sbwRecvOne(ops, s)
		if ret != 0 {
			break
		}
	}

	// Update a few stats and signal the sender
	s.Lock.Lock()
	if s.IsLinked {
		sbwRemoveFromDrainedList(ops, s)
	}
	AtomicSubUint64(&ops.SbwCreditUsed, uint64(s.Credit))
	AtomicSubUint64(&ops.SbwNumPending, uint64(s.NumPending))
	s.NumPending = 0
	s.Demand = 0
	s.Closed = true
	s.SendCondVar.Signal()
	s.Lock.Unlock()

	// Cleanup
	AtomicSubUint64(&ops.SbwNumSess, 1)
	s.SendWaiter.Wait()

	// Re-Initialize credits, if there are no clients
	if AtomicGetUint64(&ops.SbwNumSess) == 0 {
		AtomicSetUint64(&ops.SbwCreditUsed, 0)
		AtomicSetUint64(&ops.SbwCreditPool, uint64(runtime.GOMAXPROCS(0)))
		ops.SbwLastCpUpdate = MicroTime()
		AtomicSetUint64(&ops.SbwCreditDs, 0)
	}
}

func sbwListener(ops *SbwOps, listenWaiter *sync.WaitGroup) {

	// Initialize the drained lists
	for i := uint64(0); i < SbwMaxProcs; i++ {
		ops.SbwDrained[i].ListH.Init()
		ops.SbwDrained[i].ListL.Init()
	}

	// Initialize the state
	AtomicSetUint64(&ops.SbwNumSess, 0)
	AtomicSetUint64(&ops.SbwNumDrained, 0)
	AtomicSetUint64(&ops.SbwCreditPool, uint64(runtime.GOMAXPROCS(0)))
	AtomicSetUint64(&ops.SbwCreditUsed, 0)
	AtomicSetUint64(&ops.SbwCreditDs, 0)
	AtomicSetUint64(&ops.SbwNumActive, 0)
	AtomicSetUint64(&ops.SbwNumPending, 0)
	ops.SbwCreditCarry = 0.0
	ops.SbwLastCpUpdate = MicroTime()

	AtomicSetUint64(&ops.SbwStatCUpdateRx, 0)
	AtomicSetUint64(&ops.SbwStatECreditTx, 0)
	AtomicSetUint64(&ops.SbwStatCreditTx, 0)
	AtomicSetUint64(&ops.SbwStatReqRx, 0)
	AtomicSetUint64(&ops.SbwStatReqDropped, 0)
	AtomicSetUint64(&ops.SbwStatRespTx, 0)

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
		go sbwServer(ops, conn)
	}
}

type SbwOps struct {
	// Request handler
	SbwHandler SrpcFn
	// Number of client sessions
	SbwNumSess uint64
	// Number of drained client sessions
	SbwNumDrained uint64
	// Number of clients with non-zero credits
	SbwNumActive uint64
	// Global credit pool
	SbwCreditPool uint64
	// Time of last credit pool update
	SbwLastCpUpdate uint64
	// Global credit pool used
	SbwCreditUsed uint64
	// Downstream credit for multi-hierarchy
	SbwCreditDs uint64
	// Number of pending requests (whose responses are not yet sent)
	SbwNumPending uint64
	// Partial credit carry
	SbwCreditCarry float64

	// Statistics
	SbwStatCUpdateRx  uint64
	SbwStatECreditTx  uint64
	SbwStatCreditTx   uint64
	SbwStatReqRx      uint64
	SbwStatReqDropped uint64
	SbwStatRespTx     uint64

	// Per-core drained session lists
	SbwDrained [SbwMaxProcs]SbwDrainedList

	// Lock
	Lock sync.Mutex

	// Memory pool for datapath allocations
	SbwCtxPool  sync.Pool
	SrpcCtxPool sync.Pool
}

func (ops *SbwOps) SrpcEnable(handler SrpcFn) int {

	// Set the request handler
	ops.Lock.Lock()
	if ops.SbwHandler != nil {
		ops.Lock.Unlock()
		return -1
	}
	ops.SbwHandler = handler
	ops.Lock.Unlock()

	// Initialize the memory pools
	ops.SbwCtxPool = sync.Pool{
		New: func() any {
			return new(SbwCtx)
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
	go sbwListener(ops, &listenWaiter)
	listenWaiter.Wait()

	return 0
}

func (ops *SbwOps) SrpcDrop() {}

func (ops *SbwOps) SrpcStatCUpdateRx() uint64 {
	return AtomicGetUint64(&ops.SbwStatCUpdateRx)
}

func (ops *SbwOps) SrpcStatECreditTx() uint64 {
	return AtomicGetUint64(&ops.SbwStatECreditTx)
}

func (ops *SbwOps) SrpcStatCreditTx() uint64 {
	return AtomicGetUint64(&ops.SbwStatCreditTx)
}

func (ops *SbwOps) SrpcStatReqRx() uint64 {
	return AtomicGetUint64(&ops.SbwStatReqRx)
}

func (ops *SbwOps) SrpcStatReqDropped() uint64 {
	return AtomicGetUint64(&ops.SbwStatReqDropped)
}

func (ops *SbwOps) SrpcStatRespTx() uint64 {
	return AtomicGetUint64(&ops.SbwStatRespTx)
}
