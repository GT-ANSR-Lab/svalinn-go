package seda

import (
	"fmt"
	"net"
	"sync"

	. "ovldctlrpc/common"
	. "utils"

	"github.com/kelindar/bitmap"
)

// Maximum supported window size
const (
	SsdMaxWindowExp uint64 = 6
	SsdMaxWindow    uint64 = 1 << SsdMaxWindowExp
)

// Specialized server-side state for a single RPC request
type SsdCtx struct {
	Cmn *SrpcCtx
	Ts  uint64
}

// Specialized server-side state for a single RPC client
type SsdSession struct {
	Cmn            *SrpcSession
	Id             int
	NumPending     int
	Closed         bool
	Lock           sync.Mutex
	AvailSlots     bitmap.Bitmap
	CompletedSlots bitmap.Bitmap
	Slots          [SsdMaxWindow]*SsdCtx
	SendCondVar    *sync.Cond
	SendWaiter     sync.WaitGroup
}

func ssdGetSlot(ops *SsdOps, s *SsdSession) int {
	s.Lock.Lock()
	defer s.Lock.Unlock()

	slot, ok := s.AvailSlots.Min()
	if !ok {
		return -1
	}
	s.AvailSlots.Remove(slot)
	s.Slots[slot] = ops.SsdCtxPool.Get().(*SsdCtx)
	s.Slots[slot].Cmn = ops.SrpcCtxPool.Get().(*SrpcCtx)
	s.Slots[slot].Cmn.Ext = s.Slots[slot]
	s.Slots[slot].Cmn.S = s.Cmn
	s.Slots[slot].Cmn.Idx = int(slot)

	return int(slot)
}

func ssdPutSlot(ops *SsdOps, s *SsdSession, slot int) {
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
	ops.SsdCtxPool.Put(c)
	s.Slots[slot] = nil
	s.AvailSlots.Set(uint32(slot))
}

func ssdSendCompletionVector(ops *SsdOps, s *SsdSession, vec *bitmap.Bitmap) {

	vec.Range(func(idx uint32) {
		c := s.Slots[idx]

		// Prepare the header
		shdr := SsdHdr{
			Magic: SdRespMagic,
			Op:    SdOpCall,
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
		AtomicAddUint64(&ops.SsdStatRespTx, 1)
	failed:
		AtomicSubUint64(&ops.SsdNumPending, 1)

		// Free the slot for reuse
		ssdPutSlot(ops, s, int(idx))
	})
}

func ssdSender(ops *SsdOps, s *SsdSession) {

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
		ssdSendCompletionVector(ops, s, &tmp)
	}

	// Wait for inflight requests to complete
	s.Lock.Lock()
	for !s.Closed || s.AvailSlots.Count()+s.CompletedSlots.Count() < int(SsdMaxWindow) {
		s.SendCondVar.Wait()
	}
	s.Lock.Unlock()

	// Cleanup any remaining slots
	for i := uint64(0); i < SsdMaxWindow; i++ {
		if s.Slots[i] != nil {
			ssdPutSlot(ops, s, int(i))
		}
	}

	// Signal the server thread that we are done
	s.SendWaiter.Done()
}

func ssdWorker(ops *SsdOps, s *SsdSession, c *SsdCtx) {
	c.Cmn.Drop = false
	ops.SsdHandler(c.Cmn)

	if c.Cmn.Drop {
		AtomicAddUint64(&ops.SsdStatReqDropped, 1)
	}

	s.Lock.Lock()
	s.CompletedSlots.Set(uint32(c.Cmn.Idx))
	s.SendCondVar.Signal()
	s.Lock.Unlock()
}

func ssdRecvOne(ops *SsdOps, s *SsdSession) int {

	var chdr CsdHdr
	var tmpBuf [SrpcBufSize]byte
	var ctx *SsdCtx

again:
	// Read the request header
	n, err := ReadFull(s.Cmn.C, ToBytes(&chdr))
	if err != nil {
		if n != 0 {
			fmt.Println("Failed to read the request header")
		}
		return -1
	}

	if chdr.Magic != SdReqMagic {
		fmt.Println("Got invalid magic")
		return -1
	}
	if chdr.Len > SrpcBufSize {
		fmt.Println("Request too large")
		return -1
	}

	switch chdr.Op {
	case SdOpCall:
		AtomicAddUint64(&ops.SsdStatReqRx, 1)

		// Find an available slot
		idx := ssdGetSlot(ops, s)
		if idx < 0 {
			ReadFull(s.Cmn.C, tmpBuf[:chdr.Len])
			AtomicAddUint64(&ops.SsdStatReqDropped, 1)
			goto again
		}
		ctx = s.Slots[idx]

		// Retrieve the payload
		n, err = ReadFull(s.Cmn.C, ctx.Cmn.ReqBuf[:chdr.Len])
		if err != nil {
			if n != 0 {
				fmt.Println("Failed to read the request payload")
			}
			ssdPutSlot(ops, s, idx)
			return -1
		}
		ctx.Cmn.ReqLen = chdr.Len
		ctx.Cmn.RespLen = 0
		ctx.Cmn.Id = chdr.Id
		ctx.Ts = chdr.Ts

		// Update stats
		s.Lock.Lock()
		s.NumPending++
		AtomicAddUint64(&ops.SsdNumPending, 1)
		s.Lock.Unlock()

		// Spawn the worker to handle the request
		go ssdWorker(ops, s, ctx)
	case SdOpWinUpdate:
		fmt.Println("Invalid op")
		return -1
	default:
		fmt.Println("Invalid op")
		return -1
	}

	return 0
}

func ssdServer(ops *SsdOps, conn *net.TCPConn) {
	defer conn.Close()

	// Initialize the session state
	s := &SsdSession{}
	s.Cmn = &SrpcSession{}
	s.Cmn.Ext = s
	s.Cmn.C = conn
	s.Id = int(AtomicAddUint64(&ops.SsdNumSess, 1))
	s.NumPending = 0
	s.Closed = false
	s.AvailSlots.Grow(uint32(SsdMaxWindow))
	s.CompletedSlots.Grow(uint32(SsdMaxWindow))
	for i := uint64(0); i < SsdMaxWindow; i++ {
		s.AvailSlots.Set(uint32(i))
		s.CompletedSlots.Remove(uint32(i))
		s.Slots[i] = nil
	}
	s.SendCondVar = sync.NewCond(&s.Lock)
	s.SendWaiter.Add(1)

	// Start the sender
	go ssdSender(ops, s)

	// Receive the requests
	for {
		ret := ssdRecvOne(ops, s)
		if ret != 0 {
			break
		}
	}

	// Update a few stats and signal the sender
	s.Lock.Lock()
	AtomicSubUint64(&ops.SsdNumPending, uint64(s.NumPending))
	s.NumPending = 0
	s.Closed = true
	s.SendCondVar.Signal()
	s.Lock.Unlock()

	// Cleanup
	AtomicSubUint64(&ops.SsdNumSess, 1)
	s.SendWaiter.Wait()

	// Break the SrpcSession <-> SsdSession cycle. Both objects become
	// unreachable when this function returns, but clearing the cross-links
	// ensures the GC can collect them independently and turns any stale
	// access into a nil-panic instead of silent garbage.
	s.Cmn.Ext = nil
	s.Cmn = nil
}

func ssdListener(ops *SsdOps, listenWaiter *sync.WaitGroup) {

	// Initialize the state
	AtomicSetUint64(&ops.SsdNumSess, 0)
	AtomicSetUint64(&ops.SsdNumActive, 0)
	AtomicSetUint64(&ops.SsdNumPending, 0)
	AtomicSetUint64(&ops.SsdStatReqRx, 0)
	AtomicSetUint64(&ops.SsdStatReqDropped, 0)
	AtomicSetUint64(&ops.SsdStatRespTx, 0)

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
		go ssdServer(ops, conn)
	}
}

type SsdOps struct {
	// Request handler
	SsdHandler SrpcFn
	// Number of client sessions
	SsdNumSess uint64
	// Number of clients with non-zero credits
	SsdNumActive uint64
	// Number of pending requests (whose responses are not yet sent)
	SsdNumPending uint64
	// Statistics
	SsdStatReqRx      uint64
	SsdStatReqDropped uint64
	SsdStatRespTx     uint64
	// Lock
	Lock sync.Mutex
	// Memory pool for datapath allocations
	SsdCtxPool  sync.Pool
	SrpcCtxPool sync.Pool
}

func (ops *SsdOps) SrpcEnable(handler SrpcFn) int {

	// Set the request handler
	ops.Lock.Lock()
	if ops.SsdHandler != nil {
		ops.Lock.Unlock()
		return -1
	}
	ops.SsdHandler = handler
	ops.Lock.Unlock()

	// Initialize the memory pools
	ops.SsdCtxPool = sync.Pool{
		New: func() any {
			return new(SsdCtx)
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
	go ssdListener(ops, &listenWaiter)
	listenWaiter.Wait()

	return 0
}

func (ops *SsdOps) SrpcDrop() {}

func (ops *SsdOps) SrpcStatCUpdateRx() uint64 { return 0 }

func (ops *SsdOps) SrpcStatECreditTx() uint64 { return 0 }

func (ops *SsdOps) SrpcStatCreditTx() uint64 { return 0 }

func (ops *SsdOps) SrpcStatReqRx() uint64 {
	return AtomicGetUint64(&ops.SsdStatReqRx)
}

func (ops *SsdOps) SrpcStatReqDropped() uint64 {
	return AtomicGetUint64(&ops.SsdStatReqDropped)
}

func (ops *SsdOps) SrpcStatRespTx() uint64 {
	return AtomicGetUint64(&ops.SsdStatRespTx)
}
