package nocontrol

import (
	"fmt"
	"net"
	"sync"

	. "ovldctlrpc/common"
	. "utils"

	"perf"

	"github.com/kelindar/bitmap"
)

// Maximum supported window size
const (
	SncMaxWindowExp uint64 = 6
	SncMaxWindow    uint64 = 1 << SncMaxWindowExp
)

// Specialized server-side state for a single RPC request
type SncCtx struct {
	Cmn *SrpcCtx
	Ts  uint64
}

// Specialized server-side state for a single RPC client
type SncSession struct {
	Cmn            *SrpcSession
	Id             int
	NumPending     int
	Closed         bool
	Lock           sync.Mutex
	AvailSlots     bitmap.Bitmap
	CompletedSlots bitmap.Bitmap
	Slots          [SncMaxWindow]*SncCtx
	SendCondVar    *sync.Cond
	SendWaiter     sync.WaitGroup
}

func sncGetSlot(ops *SncOps, s *SncSession) int {
	s.Lock.Lock()
	defer s.Lock.Unlock()

	slot, ok := s.AvailSlots.Min()
	if !ok {
		return -1
	}
	s.AvailSlots.Remove(slot)
	s.Slots[slot] = ops.SncCtxPool.Get().(*SncCtx)
	s.Slots[slot].Cmn = ops.SrpcCtxPool.Get().(*SrpcCtx)
	s.Slots[slot].Cmn.Ext = s.Slots[slot]
	s.Slots[slot].Cmn.S = s.Cmn
	s.Slots[slot].Cmn.Idx = int(slot)

	return int(slot)
}

func sncPutSlot(ops *SncOps, s *SncSession, slot int) {
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
	ops.SncCtxPool.Put(c)
	s.Slots[slot] = nil
	s.AvailSlots.Set(uint32(slot))
}

func sncSendCompletionVector(ops *SncOps, s *SncSession, vec *bitmap.Bitmap) {

	vec.Range(func(idx uint32) {
		c := s.Slots[idx]

		// Prepare the header
		shdr := SncHdr{
			Magic: NcRespMagic,
			Op:    NcOpCall,
			Len:   c.Cmn.RespLen,
			Id:    c.Cmn.Id,
			Ts:    c.Ts,
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
		n, err = WriteFull(s.Cmn.C, c.Cmn.RespBuf[:c.Cmn.RespLen])
		if err != nil {
			if n != 0 {
				fmt.Println("Failed to send response payload")
			}
			goto failed
		}

		// Update stats
		AtomicAddUint64(&ops.SncStatRespTx, 1)
	failed:
		AtomicSubUint64(&ops.SncNumPending, 1)

		// Free the slot for reuse
		sncPutSlot(ops, s, int(idx))
	})
}

func sncSender(ops *SncOps, s *SncSession) {

	for {
		s.Lock.Lock()

		for {
			if !s.Closed && s.CompletedSlots.Count() == 0 {
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

		// Update stats
		numResp := tmp.Count()
		s.NumPending -= numResp

		s.Lock.Unlock()

		// Send the responses
		sncSendCompletionVector(ops, s, &tmp)
	}

	// Wait for inflight requests to complete
	s.Lock.Lock()
	for !s.Closed || s.AvailSlots.Count()+s.CompletedSlots.Count() < int(SncMaxWindow) {
		s.SendCondVar.Wait()
	}
	s.Lock.Unlock()

	// Cleanup any remaining slots
	for i := uint64(0); i < SncMaxWindow; i++ {
		if s.Slots[i] != nil {
			sncPutSlot(ops, s, int(i))
		}
	}

	// Signal the server thread that we are done
	s.SendWaiter.Done()
}

func sncWorker(ops *SncOps, s *SncSession, c *SncCtx) {
	c.Cmn.Drop = false
	ops.SncHandler(c.Cmn)

	if c.Cmn.Drop {
		AtomicAddUint64(&ops.SncStatReqDropped, 1)
	}

	s.Lock.Lock()
	s.CompletedSlots.Set(uint32(c.Cmn.Idx))
	s.SendCondVar.Signal()
	s.Lock.Unlock()
}

func sncRecvOne(ops *SncOps, s *SncSession) int {

	var chdr CncHdr
	var tmpBuf [SrpcBufSize]byte
	var ctx *SncCtx

again:
	// Read the request header
	n, err := ReadFull(s.Cmn.C, ToBytes(&chdr))
	if err != nil {
		if n != 0 {
			fmt.Println("Failed to read the request header")
		}
		return -1
	}

	if chdr.Magic != NcReqMagic {
		fmt.Println("Got invalid magic")
		return -1
	}
	if chdr.Len > SrpcBufSize {
		fmt.Println("Request too large")
		return -1
	}

	switch chdr.Op {
	case NcOpCall:
		AtomicAddUint64(&ops.SncStatReqRx, 1)

		// Find an available slot
		idx := sncGetSlot(ops, s)
		if idx < 0 {
			ReadFull(s.Cmn.C, tmpBuf[:chdr.Len])
			AtomicAddUint64(&ops.SncStatReqDropped, 1)
			goto again
		}
		ctx = s.Slots[idx]

		// Retrieve the payload
		n, err = ReadFull(s.Cmn.C, ctx.Cmn.ReqBuf[:chdr.Len])
		if err != nil {
			if n != 0 {
				fmt.Println("Failed to read the request payload")
			}
			sncPutSlot(ops, s, idx)
			return -1
		}
		ctx.Cmn.ReqLen = chdr.Len
		ctx.Cmn.RespLen = 0
		ctx.Cmn.Id = chdr.Id
		ctx.Ts = chdr.Ts

		// Update stats
		s.Lock.Lock()
		s.NumPending++
		AtomicAddUint64(&ops.SncNumPending, 1)
		if SncAqmOn {
			// Get the runtime queueing delay
			maxQueueDelay := perf.GetQueueDelayMax()
			maxQueueDelay = maxQueueDelay / 1000
			// Check if we need to drop the request
			if maxQueueDelay >= SncAqmThresh {
				ctx.Cmn.Drop = true
				s.CompletedSlots.Set(uint32(idx))
				s.SendCondVar.Signal()
				s.Lock.Unlock()
				AtomicAddUint64(&ops.SncStatReqDropped, 1)
				goto again
			}
		}
		s.Lock.Unlock()

		// Spawn the worker to handle the request
		go sncWorker(ops, s, ctx)
	case NcOpWinUpdate:
		fmt.Println("Invalid op")
		return -1
	default:
		fmt.Println("Invalid op")
		return -1
	}

	return 0
}

func sncServer(ops *SncOps, conn *net.TCPConn) {
	defer conn.Close()

	// Initialize the session state
	s := &SncSession{}
	s.Cmn = &SrpcSession{}
	s.Cmn.Ext = s
	s.Cmn.C = conn
	s.Id = int(AtomicAddUint64(&ops.SncNumSess, 1))
	s.NumPending = 0
	s.Closed = false
	s.AvailSlots.Grow(uint32(SncMaxWindow))
	s.CompletedSlots.Grow(uint32(SncMaxWindow))
	for i := uint64(0); i < SncMaxWindow; i++ {
		s.AvailSlots.Set(uint32(i))
		s.CompletedSlots.Remove(uint32(i))
		s.Slots[i] = nil
	}
	s.SendCondVar = sync.NewCond(&s.Lock)
	s.SendWaiter.Add(1)

	// Start the sender
	go sncSender(ops, s)

	// Receive the requests
	for {
		ret := sncRecvOne(ops, s)
		if ret != 0 {
			break
		}
	}

	// Update a few stats and signal the sender
	s.Lock.Lock()
	AtomicSubUint64(&ops.SncNumPending, uint64(s.NumPending))
	s.NumPending = 0
	s.Closed = true
	s.SendCondVar.Signal()
	s.Lock.Unlock()

	// Cleanup
	AtomicSubUint64(&ops.SncNumSess, 1)
	s.SendWaiter.Wait()

	// Break the SrpcSession <-> SncSession cycle. Both objects become
	// unreachable when this function returns, but clearing the cross-links
	// ensures the GC can collect them independently and turns any stale
	// access into a nil-panic instead of silent garbage.
	s.Cmn.Ext = nil
	s.Cmn = nil
}

func sncListener(ops *SncOps, listenWaiter *sync.WaitGroup) {

	// Initialize the state
	AtomicSetUint64(&ops.SncNumSess, 0)
	AtomicSetUint64(&ops.SncNumActive, 0)
	AtomicSetUint64(&ops.SncNumPending, 0)
	AtomicSetUint64(&ops.SncStatReqRx, 0)
	AtomicSetUint64(&ops.SncStatReqDropped, 0)
	AtomicSetUint64(&ops.SncStatRespTx, 0)

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
		go sncServer(ops, conn)
	}
}

type SncOps struct {
	// Request handler
	SncHandler SrpcFn
	// Number of client sessions
	SncNumSess uint64
	// Number of clients with non-zero credits
	SncNumActive uint64
	// Number of pending requests (whose responses are not yet sent)
	SncNumPending uint64
	// Statistics
	SncStatReqRx      uint64
	SncStatReqDropped uint64
	SncStatRespTx     uint64
	// Lock
	Lock sync.Mutex
	// Memory pool for datapath allocations
	SncCtxPool  sync.Pool
	SrpcCtxPool sync.Pool
}

func (ops *SncOps) SrpcEnable(handler SrpcFn) int {

	// Set the request handler
	ops.Lock.Lock()
	if ops.SncHandler != nil {
		ops.Lock.Unlock()
		return -1
	}
	ops.SncHandler = handler
	ops.Lock.Unlock()

	// Initialize the memory pools
	ops.SncCtxPool = sync.Pool{
		New: func() any {
			return new(SncCtx)
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
	go sncListener(ops, &listenWaiter)
	listenWaiter.Wait()

	return 0
}

func (ops *SncOps) SrpcDrop() {}

func (ops *SncOps) SrpcStatCUpdateRx() uint64 { return 0 }

func (ops *SncOps) SrpcStatECreditTx() uint64 { return 0 }

func (ops *SncOps) SrpcStatCreditTx() uint64 { return 0 }

func (ops *SncOps) SrpcStatReqRx() uint64 {
	return AtomicGetUint64(&ops.SncStatReqRx)
}

func (ops *SncOps) SrpcStatReqDropped() uint64 {
	return AtomicGetUint64(&ops.SncStatReqDropped)
}

func (ops *SncOps) SrpcStatRespTx() uint64 {
	return AtomicGetUint64(&ops.SncStatRespTx)
}
