package protego

import (
	"fmt"
	"net"
	"sync"
	"time"

	. "ovldctlrpc/common"
	. "utils"
)

// Specialized client-side state for a single client connection with a server
// replica
type CpgConn struct {
	Cmn     *CrpcConn
	Session *CpgSession

	// Credit-related variables
	WaitingResp bool
	Credit      uint32
	CreditUsed  uint32

	// Per-connection stats
	ECreditRx     uint64
	CUpdateTx     uint64
	RespRx        uint64
	ReqTx         uint64
	CreditExpired uint64
}

// Specialized client-side state for a single RPC client
type CpgSession struct {
	Cmn         *CrpcSession
	Id          uint64
	ReqId       uint64
	NextConnIdx int
	Running     bool
	Init        bool
	Lock        sync.Mutex

	// Timer for request expire in the queue
	TimerWaiter  sync.WaitGroup
	TimerCondVar *sync.Cond

	// Queue of pending RPC requests
	Head uint32
	Tail uint32
	QReq [CrpcQlen]*CrpcCtx

	// Per-client stats
	ReqDropped uint64
}

// Client operation handlers for "protego" overload control algorithm
type CpgOps struct{}

func cpgSendCUpdate(ops *CpgOps, cc *CpgConn) int {
	s := cc.Session

	// Construct the client header
	chdr := CpgHdr{
		Magic:  PgReqMagic,
		Op:     PgOpCredit,
		Id:     0,
		Len:    0,
		Demand: uint64(s.Head - s.Tail),
		Flags:  0,
	}

	// Send the request
	n, err := WriteFull(cc.Cmn.C, ToBytes(&chdr))
	if err != nil {
		if n != 0 {
			fmt.Println("Failed to send the credit update message")
		}
		return -1
	}

	cc.CUpdateTx++
	return 0
}

func cpgSendRaw(ops *CpgOps, cc *CpgConn, buf []byte, len uint64, id uint64) int {
	s := cc.Session

	// Prepare the header
	chdr := CpgHdr{
		Magic:  PgReqMagic,
		Op:     PgOpCall,
		Id:     id,
		Len:    len,
		Demand: uint64(s.Head - s.Tail),
		TsSent: MicroTime(),
		Flags:  0,
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

func cpgSendRequestVector(ops *CpgOps, cc *CpgConn) int {
	s := cc.Session
	now := MicroTime()

	// Queue is empty or no available credits
	if s.Head == s.Tail || cc.CreditUsed >= cc.Credit {
		return 0
	}

	// While queue is not empty and there are available credits
	for s.Head != s.Tail && cc.CreditUsed < cc.Credit {
		c := s.QReq[s.Tail%CrpcQlen]
		s.Tail++

		// Prepare the header
		chdr := CpgHdr{
			Magic:  PgReqMagic,
			Op:     PgOpCall,
			Id:     c.Id,
			Len:    c.Len,
			Demand: uint64(s.Head - s.Tail),
			TsSent: now,
			Flags:  0,
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

		cc.CreditUsed++
		cc.ReqTx++
	}

	if s.Head == s.Tail {
		s.Head = 0
		s.Tail = 0
	}

	return 0
}

func cpgDrainQueue(ops *CpgOps, s *CpgSession) {
	now := MicroTime()

	// If the queue is empty
	if s.Head == s.Tail {
		return
	}

	// Choose request to send from request queue
	for s.Head != s.Tail {
		pos := s.Tail % CrpcQlen
		c := s.QReq[pos]
		if CpgMaxClientDelayUs == 0 || now-c.Ts <= CpgMaxClientDelayUs {
			break
		}

		// Handle drop
		if s.Cmn.LDropHandler != nil {
			s.Cmn.LDropHandler(c)
		}
		s.Tail++
		s.ReqDropped++
	}

	// Find the connection to send
	for i := 0; i < s.Cmn.NConns; i++ {
		connIdx := (s.NextConnIdx + i) % s.Cmn.NConns
		cc := s.Cmn.C[connIdx].Ext.(*CpgConn)

		// (1) not waitin for the first response
		// (2) have available credit
		if !cc.WaitingResp && cc.CreditUsed < cc.Credit {
			cpgSendRequestVector(ops, cc)
			s.NextConnIdx = (connIdx + 1) % s.Cmn.NConns
			break
		}
	}
}

func cpgEnqueueOne(ops *CpgOps, s *CpgSession, buf []byte, len uint64, arg any) bool {
	now := MicroTime()

	// If the queue is full, drop tail
	if s.Head-s.Tail >= CrpcQlen {
		pos := s.Tail % CrpcQlen
		c := s.QReq[pos]

		// Handle the drop
		if s.Cmn.LDropHandler != nil {
			s.Cmn.LDropHandler(c)
		}

		s.Tail++
		s.ReqDropped++
	}

	pos := s.Head % CrpcQlen
	s.Head++
	c := s.QReq[pos]
	copy(c.Buf[:], buf[:len])
	c.Id = s.ReqId
	s.ReqId++
	c.Ts = now
	c.Len = len
	c.Arg = arg

	// Very first message
	if !s.Init {
		for i := 0; i < s.Cmn.NConns; i++ {
			cc := s.Cmn.C[i].Ext.(*CpgConn)
			cpgSendCUpdate(ops, cc)
			cc.WaitingResp = true
		}
		s.Init = true
	}

	// If queue becomes non-empty, start expiration loop
	if s.Head-s.Tail == 1 {
		// Mutex already held
		s.TimerCondVar.Signal()
	}

	return true
}

func (ops *CpgOps) CrpcAddConnection(s *CrpcSession, raddr *net.TCPAddr) int {
	ss := s.Ext.(*CpgSession)

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
	c := &CpgConn{}
	c.Cmn = &CrpcConn{}
	c.Cmn.Ext = c

	// Initialize the connection
	c.Cmn.C = conn
	c.Session = ss

	// Update session
	ss.Lock.Lock()
	ss.Cmn.C[ss.Cmn.NConns] = c.Cmn
	ss.Cmn.NConns++
	ss.Lock.Unlock()

	return 0
}

func (ops *CpgOps) CrpcSendOne(s *CrpcSession, buf []byte, len uint64, hash int, arg any) int {
	ss := s.Ext.(*CpgSession)

	if len > SrpcBufSize {
		return -1
	}

	ss.Lock.Lock()

	// Hot path, just send
	cc := ss.Cmn.C[ss.NextConnIdx].Ext.(*CpgConn)
	if cc.CreditUsed < cc.Credit && ss.Head == ss.Tail {
		cc.CreditUsed++
		ret := cpgSendRaw(ops, cc, buf, len, ss.ReqId)
		ss.ReqId++
		ss.NextConnIdx = (ss.NextConnIdx + 1) % ss.Cmn.NConns
		ss.Lock.Unlock()
		return ret
	}

	// Cold path, enqueue request and drain the queue
	if !cpgEnqueueOne(ops, ss, buf, len, arg) {
		cpgDrainQueue(ops, ss)
		ss.Lock.Unlock()
		return -1
	}
	cpgDrainQueue(ops, ss)
	ss.Lock.Unlock()

	return int(len)
}

func (ops *CpgOps) CrpcRecvOne(c *CrpcConn, buf []byte, len uint64, arg any) int {

	var shdr SpgHdr
	cc := c.Ext.(*CpgConn)
	s := cc.Session

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
	if shdr.Magic != PgRespMagic {
		fmt.Println("Got invalid magic")
		return -1
	}
	if shdr.Len > Min(SrpcBufSize, len) {
		fmt.Println("Response too large")
		return -1
	}

	switch shdr.Op {
	case PgOpCall:
		// Read the payload
		if shdr.Len > 0 {
			n, err = ReadFull(cc.Cmn.C, buf[:shdr.Len])
			if err != nil {
				if n != 0 {
					fmt.Println("Failed to read response payload")
				}
				return -1
			}
			if shdr.Flags&uint8(PgSFlagDrop) == 0 {
				cc.RespRx++
			}
		}

		// Update the credit
		s.Lock.Lock()
		cc.CreditUsed--
		cc.Credit = uint32(shdr.Credit)
		cc.WaitingResp = false
		if cc.Credit > 0 {
			cpgDrainQueue(ops, s)
		}
		s.Lock.Unlock()

		// Handle the drop
		if shdr.Flags&uint8(PgSFlagDrop) != 0 {
			if s.Cmn.RDropHandler != nil {
				s.Cmn.RDropHandler(buf, uint64(n), arg)
			}
			goto again
		}
	case PgOpCredit:
		if shdr.Len != 0 {
			fmt.Println("Window update has non-zero length")
			return -1
		}

		// Update the credit
		s.Lock.Lock()
		cc.Credit = uint32(shdr.Credit)
		cc.WaitingResp = false
		if cc.Credit > 0 {
			cpgDrainQueue(ops, s)
		}
		s.Lock.Unlock()
		cc.ECreditRx++

		goto again
	default:
		fmt.Println("Invalid op")
		return -1
	}

	return int(shdr.Len)
}

func cpgTimer(ops *CpgOps, s *CpgSession) {

	s.Lock.Lock()
	for {
		for s.Running && s.Head == s.Tail {
			s.TimerCondVar.Wait()
		}

		if !s.Running {
			goto done
		}

		numDrops := 0
		now := MicroTime()

		// Drop requests if expired
		for s.Head != s.Tail {
			pos := s.Tail % CrpcQlen
			c := s.QReq[pos]
			if now-c.Ts <= CpgMaxClientDelayUs {
				break
			}

			// Handle drop
			if s.Cmn.LDropHandler != nil {
				s.Cmn.LDropHandler(c)
			}

			// Update stats
			s.Tail++
			s.ReqDropped++
			numDrops++
		}

		// If queue becomes empty
		if s.Head == s.Tail {
			continue
		}

		// Calculate next wake up time
		pos := (s.Head - 1) % CrpcQlen
		c := s.QReq[pos]
		s.Lock.Unlock()
		now = MicroTime()
		if now < c.Ts+CpgMaxClientDelayUs {
			sleepDuration := c.Ts + CpgMaxClientDelayUs - now
			time.Sleep(time.Duration(sleepDuration) * time.Microsecond)
		}
		s.Lock.Lock()
	}
done:
	s.Lock.Unlock()
	s.TimerWaiter.Done()
}

func (ops *CpgOps) CrpcOpen(
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
	s := &CpgSession{}
	s.Cmn = &CrpcSession{}
	s.Cmn.Ext = s

	// Allocate the request queue
	for i := 0; i < CrpcQlen; i++ {
		s.QReq[i] = &CrpcCtx{}
	}
	s.Head = 0
	s.Tail = 0

	// Allocate the connection object
	c := &CpgConn{}
	c.Cmn = &CrpcConn{}
	c.Cmn.Ext = c

	// Initialize the connection
	c.Cmn.C = conn
	c.Session = s

	// Initialize the session
	s.Cmn.NConns = 1
	s.Cmn.C[0] = c.Cmn
	s.Cmn.LDropHandler = ldropHandler
	s.Cmn.RDropHandler = rdropHandler
	s.Running = true
	s.Id = uint64(id)
	s.ReqId = 1

	s.TimerWaiter.Add(1)
	s.TimerCondVar = sync.NewCond(&s.Lock)

	// Spawn timer thread
	if CpgMaxClientDelayUs > 0 {
		go cpgTimer(ops, s)
	} else {
		s.TimerWaiter.Done()
	}

	return s.Cmn, 0
}

func (ops *CpgOps) CrpcClose(s *CrpcSession) {
	ss := s.Ext.(*CpgSession)

	// Terminate the client and wait for timer thread
	ss.Lock.Lock()
	ss.Running = false
	ss.TimerCondVar.Signal()
	ss.Lock.Unlock()

	// Wait for the timer to exit
	ss.TimerWaiter.Wait()

	// Free the connections. Break the CrpcConn <-> CpgConn cycle (and
	// CpgConn -> CpgSession back-ref) before dropping references. Cycles
	// aren't a leak in Go, but clearing them ensures a goroutine still
	// holding a *CrpcConn (e.g. blocked in CrpcRecvOne) can't resurrect
	// stale specialized/session state via .Ext.
	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i]
		cc := c.Ext.(*CpgConn)
		c.C.Close()
		cc.Cmn = nil
		cc.Session = nil
		c.Ext = nil
		ss.Cmn.C[i] = nil
	}

	// Remove the references for the request queue
	for i := 0; i < CrpcQlen; i++ {
		ss.QReq[i] = nil
	}

	// Break the CrpcSession <-> CpgSession cycle. Safe to do here because
	// the timer goroutine has already exited (TimerWaiter.Wait above) and
	// no further user-visible access should occur via either back-pointer.
	s.Ext = nil
	ss.Cmn = nil
}

func (ops *CpgOps) CrpcCredit(s *CrpcSession) uint64 {
	ss := s.Ext.(*CpgSession)
	ret := uint64(0)

	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CpgConn)
		ret += uint64(c.Credit)
	}

	return ret
}

func (ops *CpgOps) CrpcStatClear(s *CrpcSession) {
	ss := s.Ext.(*CpgSession)

	ss.ReqDropped = 0
	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CpgConn)
		c.CreditExpired = 0
		c.ECreditRx = 0
		c.CUpdateTx = 0
		c.RespRx = 0
		c.ReqTx = 0
	}
}

func (ops *CpgOps) CrpcStatECreditRx(s *CrpcSession) uint64 {
	ss := s.Ext.(*CpgSession)
	ret := uint64(0)

	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CpgConn)
		ret += c.ECreditRx
	}

	return ret
}

func (ops *CpgOps) CrpcStatCreditExpired(s *CrpcSession) uint64 {
	ss := s.Ext.(*CpgSession)
	ret := uint64(0)

	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CpgConn)
		ret += c.CreditExpired
	}

	return ret
}

func (ops *CpgOps) CrpcStatCUpdateTx(s *CrpcSession) uint64 {
	ss := s.Ext.(*CpgSession)
	ret := uint64(0)

	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CpgConn)
		ret += c.CUpdateTx
	}

	return ret
}

func (ops *CpgOps) CrpcStatRespRx(s *CrpcSession) uint64 {
	ss := s.Ext.(*CpgSession)
	ret := uint64(0)

	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CpgConn)
		ret += c.RespRx
	}

	return ret
}

func (ops *CpgOps) CrpcStatReqTx(s *CrpcSession) uint64 {
	ss := s.Ext.(*CpgSession)
	ret := uint64(0)

	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CpgConn)
		ret += c.ReqTx
	}

	return ret
}

func (ops *CpgOps) CrpcStatReqDropped(s *CrpcSession) uint64 {
	ss := s.Ext.(*CpgSession)
	return ss.ReqDropped
}
