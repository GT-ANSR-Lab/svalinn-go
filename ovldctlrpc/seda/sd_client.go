package seda

import (
	"fmt"
	"math/rand"
	"net"
	"sort"
	"sync"
	"time"

	. "ovldctlrpc/common"
	. "utils"
)


const (
	SedaNReq = 100
)

// Specialized client-side state for a single client connection with a server
// replica
type CsdConn struct {
	Cmn     *CrpcConn

	TimerCondVar *sync.Cond
	TimerWaiter  sync.WaitGroup
	Running bool
	SenderCondVar *sync.Cond
	SenderWaiter  sync.WaitGroup
	Lock sync.Mutex
	Session *CsdSession

	// Queue of pending RPC requests
	Head uint32
	Tail uint32
	QReq [CrpcQlen]*CrpcCtx

	// Token bucket for rate limiting
	TbToken float64
	TbRefreshRate float64
	TbLastRefresh uint64

	// Response time statistics
	ResTs [SedaNReq]int
	ResIdx int
	Cur float64
	SedaLastUpdate uint64

	// per connection stats
	RespRx     uint64
	ReqTx      uint64
	ReqDropped uint64
}

// Specialized client-side state for a single RPC client
type CsdSession struct {
	Cmn         *CrpcSession
	ReqId       uint64
	Id          uint64
	NextConnIdx int
	Lock        sync.Mutex
	ReqDropped  uint64
}

// Client operation handlers for "seda" overload control algorithm
type CsdOps struct{}

func tbRefillToken(ops *CsdOps, cc *CsdConn) {
	now := MicroTime()

	if cc.TbLastRefresh == 0 {
		cc.TbLastRefresh = now
		cc.TbToken = 0
		return
	}

	newToken := float64(now-cc.TbLastRefresh) * cc.TbRefreshRate / 1000000.0
	cc.TbToken += newToken
	cc.TbLastRefresh = now

	cc.TbToken = Min(cc.TbToken, float64(CsdTbMaxToken))
}

func tbSetRate(ops *CsdOps, cc *CsdConn, new_rate float64) {
	tbRefillToken(ops, cc)
	cc.TbRefreshRate = Max(new_rate, float64(CsdTbMinRate))
}

func tbSleepUntilNextToken(ops *CsdOps, cc *CsdConn) {

	sleepUntil := cc.TbLastRefresh + uint64((1.0 - cc.TbToken) * 1000000.0 / cc.TbRefreshRate) + 1

	cc.Lock.Unlock()
	now := MicroTime()
	if now < sleepUntil {
		time.Sleep(time.Duration(sleepUntil - now) * time.Microsecond)
	}
	cc.Lock.Lock()
	tbRefillToken(ops, cc)
}

func csdSendRequestVector(ops *CsdOps, cc *CsdConn) int {
	now := MicroTime()

	// Queue is empty or no available credits
	if cc.Head == cc.Tail || cc.TbToken < 1.0 {
		return 0
	}

	// While queue is not empty and there are available credits
	for cc.Head != cc.Tail && cc.TbToken >= 1.0 {
		c := cc.QReq[cc.Tail%CrpcQlen]
		cc.Tail++

		// Prepare the header
		chdr := CsdHdr{
			Magic: SdReqMagic,
			Op:    SdOpCall,
			Id:    c.Id,
			Len:   c.Len,
			Ts:    now,
		}

		// Send the header
		n, err := WriteFull(cc.Cmn.C, ToBytes(&chdr))
		if err != nil {
			if n != 0 {
				fmt.Println("Failed to send request header")
			}
			return -1
		}

		// Send the payload
		n, err = WriteFull(cc.Cmn.C, c.Buf[:c.Len])
		if err != nil {
			if n != 0 {
				fmt.Println("Failed to send request payload")
			}
			return -1
		}

		cc.TbToken -= 1.0
		cc.ReqTx++
	}

	if cc.Head == cc.Tail {
		cc.Head = 0
		cc.Tail = 0
	}

	return 0
}

func csdSendRaw(ops *CsdOps, cc *CsdConn, buf []byte, len uint64, id uint64) int {

	// Prepare the header
	chdr := CsdHdr{
		Magic: SdReqMagic,
		Op:    SdOpCall,
		Id:    id,
		Len:   len,
		Ts:    MicroTime(),
	}

	// Send the header
	n, err := WriteFull(cc.Cmn.C, ToBytes(&chdr))
	if err != nil {
		if n != 0 {
			fmt.Println("Failed to send request header")
		}
		return -1
	}

	// Send the payload
	n, err = WriteFull(cc.Cmn.C, buf[:len])
	if err != nil {
		if n != 0 {
			fmt.Println("Failed to send request payload")
		}
		return -1
	}

	// Update stats
	cc.ReqTx++
	return n
}

func csdEnqueueOne(ops *CsdOps, ss *CsdSession, buf []byte, len uint64) bool {
	var cc *CsdConn
	now := MicroTime()

	for i := 0; i < ss.Cmn.NConns; i++ {
		connIdx := (ss.NextConnIdx + i) % ss.Cmn.NConns
		cc = ss.Cmn.C[connIdx].Ext.(*CsdConn)
		if cc.Head-cc.Tail < CrpcQlen {
			ss.NextConnIdx = (connIdx + 1) % ss.Cmn.NConns
			break
		}
		cc = nil
	}

	if cc == nil {
		ss.ReqDropped++
		return false
	}

	cc.Lock.Lock()

	if cc.Head - cc.Tail >= CrpcQlen {
		ss.ReqDropped++
		cc.Lock.Unlock()
		return false
	}

	pos := cc.Head % CrpcQlen
	cc.Head++
	c := cc.QReq[pos]
	copy(c.Buf[:], buf[:len])
	c.Id = ss.ReqId
	ss.ReqId++
	c.Ts = now
	c.Len = len

	if cc.Head - cc.Tail == 1 {
		cc.TimerCondVar.Signal()
		cc.SenderCondVar.Signal()
	}

	cc.Lock.Unlock()
	return true
}

func csdTimer(ops *CsdOps, cc *CsdConn) {
	cc.Lock.Lock()

	for {
		for cc.Running && cc.Head == cc.Tail {
			cc.TimerCondVar.Wait()
		}
		if !cc.Running {
			goto done
		}

		now := MicroTime()

		// Drop old requests
		for cc.Head != cc.Tail {
			pos := cc.Tail % CrpcQlen
			c := cc.QReq[pos]
			if now - c.Ts <= CsdMaxClientDelayUs {
				break
			}
			cc.Tail++
			cc.ReqDropped++
		}

		if cc.Head == cc.Tail {
			cc.Head = 0
			cc.Tail = 0
			continue
		}

		// Calculate next wake up time
		pos := (cc.Head - 1) % CrpcQlen
		c := cc.QReq[pos]
		cc.Lock.Unlock()
		now = MicroTime()
		if now < c.Ts + CsdMaxClientDelayUs {
			sleepDuration := c.Ts + CsdMaxClientDelayUs - now
			time.Sleep(time.Duration(sleepDuration) * time.Microsecond)
		}
		cc.Lock.Lock()
	}

done:
	cc.Lock.Unlock()
	cc.TimerWaiter.Done()
}

func csdSender(ops *CsdOps, cc *CsdConn) {

	cc.Lock.Lock()

	for {
		for cc.Running && cc.Head == cc.Tail {
			cc.SenderCondVar.Wait()
		}

		if !cc.Running {
			goto done
		}

		for cc.TbToken < 1.0 {
			tbSleepUntilNextToken(ops, cc)
		}

		now := MicroTime()
		for cc.Head != cc.Tail {
			pos := cc.Tail % CrpcQlen
			c := cc.QReq[pos]
			if now - c.Ts <= CsdMaxClientDelayUs {
				break
			}
			cc.Tail++
			cc.ReqDropped++
		}

		csdSendRequestVector(ops, cc)
	}

done:
	cc.Lock.Unlock()
	cc.SenderWaiter.Done()
}

func csdUpdateTbRate(ops *CsdOps, cc *CsdConn, us uint64) {
	
	rate := cc.TbRefreshRate
	now := MicroTime()

	cc.ResTs[cc.ResIdx%SedaNReq] = int(us)
	cc.ResIdx++

	if now - cc.SedaLastUpdate > SedaTimeout {
		len := cc.ResIdx % SedaNReq
		sort.Slice(cc.ResTs[:len], func(i, j int) bool {
			return cc.ResTs[i] < cc.ResTs[j]
		})
		idx := int(float64(len - 1) * 0.99)
		samp := cc.ResTs[idx]
		cc.Cur = SedaAlpha * cc.Cur + (1 - SedaAlpha) * float64(samp)

		err := (cc.Cur - float64(SedaTarget)) / float64(SedaTarget)

		if err > SedaErrD {
			rate = rate / SedaAdjD
		} else if err < SedaErrI {
			rate += -(err - SedaCi) * SedaAdjI
		}

		tbSetRate(ops, cc, rate)
		cc.SedaLastUpdate = MicroTime()
		cc.ResIdx = 0
		return
	}

	if cc.ResIdx % SedaNReq > 0 {
		return
	}

	sort.Slice(cc.ResTs[:SedaNReq], func(i, j int) bool {
		return cc.ResTs[i] < cc.ResTs[j]
	})
	idx := int(float64(SedaNReq) * 0.99)
	samp := cc.ResTs[idx]
	cc.Cur = SedaAlpha * cc.Cur + (1 - SedaAlpha) * float64(samp)

	err := (cc.Cur - float64(SedaTarget)) / float64(SedaTarget)

	if err > SedaErrD {
		rate = rate / SedaAdjD
	} else if err < SedaErrI {
		rate += -(err - SedaCi) * SedaAdjI
	}
	
	tbSetRate(ops, cc, rate)
	cc.SedaLastUpdate = MicroTime()
}

func (ops *CsdOps) CrpcAddConnection(s *CrpcSession, raddr *net.TCPAddr) int {
	ss := s.Ext.(*CsdSession)

	// Perform error checks
	if ss.Cmn.NConns >= CrpcMaxReplica {
		return -1
	}
	if raddr.Port != SrpcPort {
		return -1
	}

	// Connect with the RPC server
	conn, err := net.DialTCP("tcp", nil, raddr)
	if err != nil {
		fmt.Println("Failed to connect with the server")
		return -1
	}
	conn.SetNoDelay(true)

	// Allocate the connection object
	c := &CsdConn{}
	c.Cmn = &CrpcConn{}
	c.Cmn.Ext = c

	// Allocate the request queue
	for i := 0; i < CrpcQlen; i++ {
		c.QReq[i] = &CrpcCtx{}
	}
	c.Head = 0
	c.Tail = 0

	// Initialize the connection
	c.Cmn.C = conn
	c.Running = true
	c.TbToken = 0.0
	c.TbRefreshRate = rand.Float64()*float64(CsdTbInitRate-CsdTbMinRate) + float64(CsdTbMinRate)
	c.TbLastRefresh = 0
	c.ResIdx = 0
	c.Session = ss

	c.TimerCondVar = sync.NewCond(&c.Lock)
	c.SenderCondVar = sync.NewCond(&c.Lock)
	c.TimerWaiter.Add(1)
	c.SenderWaiter.Add(1)

	ss.Lock.Lock()
	ss.Cmn.C[ss.Cmn.NConns] = c.Cmn
	ss.Cmn.NConns++
	ss.Lock.Unlock()

	// Start the timer thread
	go csdTimer(ops, c)

	// Start the sender thread
	go csdSender(ops, c)

	return 0
}

func (ops *CsdOps) CrpcSendOne(s *CrpcSession, buf []byte, len uint64, hash int, arg any) int {

	connIdx := 0
	ss := s.Ext.(*CsdSession)

	if len > SrpcBufSize {
		return -1
	}

	for i := 0; i < ss.Cmn.NConns; i++ {
		cc := ss.Cmn.C[i].Ext.(*CsdConn)
		cc.Lock.Lock()
		tbRefillToken(ops, cc)
		cc.Lock.Unlock()
	}

	ss.Lock.Lock()

	for i := 0; i < ss.Cmn.NConns; i++ {
		connIdx = (ss.NextConnIdx + i) % ss.Cmn.NConns
		cc := ss.Cmn.C[connIdx].Ext.(*CsdConn)

		// Hot path
		cc.Lock.Lock()
		if cc.Head == cc.Tail && cc.TbToken >= 1.0 {
			cc.TbToken -= 1.0
			ret := csdSendRaw(ops, cc, buf, len, ss.ReqId)
			ss.ReqId++
			ss.NextConnIdx = (ss.NextConnIdx + 1) % ss.Cmn.NConns
			cc.Lock.Unlock()
			ss.Lock.Unlock()
			return ret
		}
		cc.Lock.Unlock()
	}

	// Cold path
	csdEnqueueOne(ops, ss, buf, len)
	ss.Lock.Unlock()

	return int(len)
}

func (ops *CsdOps) CrpcRecvOne(c *CrpcConn, buf []byte, len uint64, arg any) int {

	var shdr SsdHdr
	cc := c.Ext.(*CsdConn)

again:
	// Read the server header
	n, err := ReadFull(cc.Cmn.C, ToBytes(&shdr))
	if err != nil {
		if n != 0 {
			fmt.Println("Failed to read response header")
		}
		return -1
	}

	// Parse the header
	if shdr.Magic != SdRespMagic {
		fmt.Println("Got invalid magic")
		return -1
	}
	if shdr.Len > Min(SrpcBufSize, len) {
		fmt.Println("Response too large")
		return -1
	}

	switch shdr.Op {
	case SdOpCall:
		if shdr.Len == 0 {
			goto again
		}
		// Read the payload
		n, err = ReadFull(cc.Cmn.C, buf[:shdr.Len])
		if err != nil {
			if n != 0 {
				fmt.Println("Failed to read response payload")
			}
			return -1
		}
		cc.RespRx++

		now := MicroTime()
		us := now - shdr.Ts

		cc.Lock.Lock()
		csdUpdateTbRate(ops, cc, us)
		cc.Lock.Unlock()
	default:
		fmt.Println("Invalid op")
		return -1
	}

	return int(shdr.Len)
}

func (ops *CsdOps) CrpcOpen(
	raddr *net.TCPAddr,
	id int,
	ldropHandler CrpcLDropFn,
	rdropHandler CrpcRDropFn,
) (*CrpcSession, int) {

	if raddr.Port != SrpcPort {
		return nil, -1
	}

	// Connect with the RPC server
	conn, err := net.DialTCP("tcp", nil, raddr)
	if err != nil {
		fmt.Println("Failed to connect with the server")
		return nil, -1
	}
	conn.SetNoDelay(true)

	// Allocate the session object
	s := &CsdSession{}
	s.Cmn = &CrpcSession{}
	s.Cmn.Ext = s

	// Allocate the connection object
	c := &CsdConn{}
	c.Cmn = &CrpcConn{}
	c.Cmn.Ext = c

	// Allocate the request queue
	for i := 0; i < CrpcQlen; i++ {
		c.QReq[i] = &CrpcCtx{}
	}
	c.Head = 0
	c.Tail = 0

	// Initialize the connection
	c.Cmn.C = conn
	c.Running = true
	c.TbToken = 0.0
	c.TbRefreshRate = rand.Float64()*float64(CsdTbInitRate-CsdTbMinRate) + float64(CsdTbMinRate)
	c.TbLastRefresh = 0
	c.ResIdx = 0
	c.Session = s

	c.TimerCondVar = sync.NewCond(&c.Lock)
	c.SenderCondVar = sync.NewCond(&c.Lock)
	c.TimerWaiter.Add(1)
	c.SenderWaiter.Add(1)

	// Initialize the session
	s.Cmn.NConns = 1
	s.Cmn.C[0] = c.Cmn
	s.Id = uint64(id)
	s.ReqId = 1

	// Start the timer thread
	go csdTimer(ops, c)

	// Start the sender thread
	go csdSender(ops, c)

	return s.Cmn, 0
}

func (ops *CsdOps) CrpcClose(s *CrpcSession) { // XXX: Needs an update
	ss := s.Ext.(*CsdSession)

	// Terminate the client
	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CsdConn)
		c.Lock.Lock()
		c.Running = false
		c.TimerCondVar.Signal()
		c.SenderCondVar.Signal()
		c.Lock.Unlock()
	}

	// Wait for the timer and sender threads
	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CsdConn)
		c.TimerWaiter.Wait()
		c.SenderWaiter.Wait()		
	}

	// Free resources. Break the CrpcConn <-> CsdConn cycle (and
	// CsdConn -> CsdSession back-ref) before dropping references. Cycles
	// aren't a leak in Go, but clearing them ensures a goroutine still
	// holding a *CrpcConn (e.g. blocked in CrpcRecvOne) can't resurrect
	// stale specialized/session state via .Ext. Safe to do here because
	// the timer and sender goroutines have already exited above.
	for i := 0; i < ss.Cmn.NConns; i++ {
		rc := ss.Cmn.C[i]
		c := rc.Ext.(*CsdConn)
		c.Cmn.C.Close()
		for j := 0; j < CrpcQlen; j++ {
			c.QReq[j] = nil
		}
		c.Cmn = nil
		c.Session = nil
		rc.Ext = nil
		ss.Cmn.C[i] = nil
	}

	// Break the CrpcSession <-> CsdSession cycle. After this point the
	// caller's *CrpcSession handle is dead; any further access via either
	// side's back-pointer will nil-panic instead of silently touching
	// freed state.
	s.Ext = nil
	ss.Cmn = nil
}

func (ops *CsdOps) CrpcCredit(s *CrpcSession) uint64 {
	return 0
}

func (ops *CsdOps) CrpcStatClear(s *CrpcSession) {
}

func (ops *CsdOps) CrpcStatECreditRx(s *CrpcSession) uint64 {
	return 0
}

func (ops *CsdOps) CrpcStatCreditExpired(s *CrpcSession) uint64 {
	return 0
}

func (ops *CsdOps) CrpcStatCUpdateTx(s *CrpcSession) uint64 {
	return 0
}

func (ops *CsdOps) CrpcStatRespRx(s *CrpcSession) uint64 {
	ss := s.Ext.(*CsdSession)
	ret := uint64(0)

	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CsdConn)
		ret += c.RespRx
	}

	return ret
}

func (ops *CsdOps) CrpcStatReqTx(s *CrpcSession) uint64 {
	ss := s.Ext.(*CsdSession)
	ret := uint64(0)

	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CsdConn)
		ret += c.ReqTx
	}

	return ret
}

func (ops *CsdOps) CrpcStatReqDropped(s *CrpcSession) uint64 {
	ss := s.Ext.(*CsdSession)
	ret := ss.ReqDropped

	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CsdConn)
		ret += c.ReqDropped
	}

	return ret
}
