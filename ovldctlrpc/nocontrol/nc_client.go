package nocontrol

import (
	"fmt"
	"net"
	"sync"

	. "ovldctlrpc/common"
	. "utils"
)

// Specialized client-side state for a single client connection with a server
// replica
type CncConn struct {
	Cmn     *CrpcConn
	Session *CncSession
	RespRx  uint64
	ReqTx   uint64
}

// Specialized client-side state for a single RPC client
type CncSession struct {
	Cmn         *CrpcSession
	ReqId       uint64
	Id          uint64
	NextConnIdx int
	Lock        sync.Mutex
}

// Client operation handlers for "no control" overload control algorithm
type CncOps struct{}

func (ops *CncOps) CrpcAddConnection(s *CrpcSession, raddr *net.TCPAddr) int {
	ss := s.Ext.(*CncSession)

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

	// Allocate the connection object
	c := &CncConn{}
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

func cncSendRaw(ops *CncOps, cc *CncConn, buf []byte, len uint64, id uint64) int {

	// Prepare the header
	chdr := CncHdr{
		Magic: NcReqMagic,
		Op:    NcOpCall,
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

func (ops *CncOps) CrpcSendOne(s *CrpcSession, buf []byte, len uint64, hash int, arg any) int {

	connIdx := 0
	ss := s.Ext.(*CncSession)

	if len > SrpcBufSize {
		return -1
	}

	ss.Lock.Lock()

	switch CncLbPolicy {
	case CncLbRR:
		connIdx = ss.NextConnIdx
		ss.NextConnIdx = (ss.NextConnIdx + 1) % ss.Cmn.NConns
	case CncLbRand:
		connIdx = hash % ss.Cmn.NConns
	default:
		fmt.Println("Invalid load balancing policy")
		ss.Lock.Unlock()
		return -1
	}

	// Send request
	cc := ss.Cmn.C[connIdx].Ext.(*CncConn)
	ret := cncSendRaw(ops, cc, buf, len, ss.ReqId)
	ss.ReqId++
	ss.Lock.Unlock()

	return ret
}

func (ops *CncOps) CrpcRecvOne(c *CrpcConn, buf []byte, len uint64, arg any) int {

	var shdr SncHdr
	cc := c.Ext.(*CncConn)

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
	if shdr.Magic != NcRespMagic {
		fmt.Println("Got invalid magic")
		return -1
	}
	if shdr.Len > Min(SrpcBufSize, len) {
		fmt.Println("Response too large")
		return -1
	}

	switch shdr.Op {
	case NcOpCall:
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
	default:
		fmt.Println("Invalid op")
		return -1
	}

	return int(shdr.Len)
}

func (ops *CncOps) CrpcOpen(
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

	// Allocate the session object
	s := &CncSession{}
	s.Cmn = &CrpcSession{}
	s.Cmn.Ext = s

	// Allocate the connection object
	c := &CncConn{}
	c.Cmn = &CrpcConn{}
	c.Cmn.Ext = c

	// Initialize the connection
	c.Cmn.C = conn
	c.Session = s

	// Initialize the session
	s.Cmn.NConns = 1
	s.Cmn.C[0] = c.Cmn
	s.Id = uint64(id)
	s.ReqId = 1

	return s.Cmn, 0
}

func (ops *CncOps) CrpcClose(s *CrpcSession) {
	ss := s.Ext.(*CncSession)

	for i := 0; i < ss.Cmn.NConns; i++ {
		ss.Cmn.C[i].C.Close()
		ss.Cmn.C[i] = nil
	}
}

func (ops *CncOps) CrpcCredit(s *CrpcSession) uint64 {
	return 0
}

func (ops *CncOps) CrpcStatClear(s *CrpcSession) {
}

func (ops *CncOps) CrpcStatECreditRx(s *CrpcSession) uint64 {
	return 0
}

func (ops *CncOps) CrpcStatCreditExpired(s *CrpcSession) uint64 {
	return 0
}

func (ops *CncOps) CrpcStatCUpdateTx(s *CrpcSession) uint64 {
	return 0
}

func (ops *CncOps) CrpcStatRespRx(s *CrpcSession) uint64 {
	ss := s.Ext.(*CncSession)
	ret := uint64(0)

	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CncConn)
		ret += c.RespRx
	}

	return ret
}

func (ops *CncOps) CrpcStatReqTx(s *CrpcSession) uint64 {
	ss := s.Ext.(*CncSession)
	ret := uint64(0)

	for i := 0; i < ss.Cmn.NConns; i++ {
		c := ss.Cmn.C[i].Ext.(*CncConn)
		ret += c.ReqTx
	}

	return ret
}

func (ops *CncOps) CrpcStatReqDropped(s *CrpcSession) uint64 {
	return 0
}
