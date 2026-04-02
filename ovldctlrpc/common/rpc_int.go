package common

import (
	"net"
)

/**
 * Server API
 */

// Constants
const (
	SrpcPort    = 8123
	SrpcBufSize = 2048
)

// Server-side state for a single RPC client
type SrpcSession struct {
	C   *net.TCPConn
	Ext any
}

// Server-side state for a single RPC request
type SrpcCtx struct {
	S         *SrpcSession
	Idx       int
	Id        uint64
	ReqLen    uint64
	RespLen   uint64
	ReqBuf    [SrpcBufSize]byte
	RespBuf   [SrpcBufSize]byte
	Drop      bool
	Track     bool
	DsCredit  uint64
	ShouldPut bool
	Ext       any
}

// Signature for the user-defined RPC request handler
type SrpcFn func(ctx *SrpcCtx)

// Abstract interface for any RPC server
type SrpcOps interface {
	SrpcEnable(handler SrpcFn) int
	SrpcDrop()
	SrpcStatCUpdateRx() uint64
	SrpcStatECreditTx() uint64
	SrpcStatCreditTx() uint64
	SrpcStatReqRx() uint64
	SrpcStatReqDropped() uint64
	SrpcStatRespTx() uint64
}

/**
 * Client API
 */

// Constants
const (
	CrpcQlen       = 16
	CrpcMaxReplica = 256
)

// Client-side state for a single RPC client
type CrpcCtx struct {
	Len uint64
	Id  uint64
	Ts  uint64
	Buf [SrpcBufSize]byte
	Arg any
}

// User-specified drop handlers for server-side (remote) or client-side
// (local) drops
type CrpcLDropFn func(ctx *CrpcCtx)
type CrpcRDropFn func(buf []byte, len uint64, arg any)

// Client-side state for a single client connection with a server replica
type CrpcConn struct {
	C   *net.TCPConn
	Ext any
}

// Client-side state for a single RPC client
type CrpcSession struct {
	C            [CrpcMaxReplica]*CrpcConn
	NConns       int
	LDropHandler CrpcLDropFn
	RDropHandler CrpcRDropFn
	Ext          any
}

// Abstract interface for any RPC server
type CrpcOps interface {
	CrpcAddConnection(s *CrpcSession, raddr *net.TCPAddr) int
	CrpcSendOne(s *CrpcSession, buf []byte, len uint64, hash int, arg any) int
	CrpcRecvOne(c *CrpcConn, buf []byte, len uint64, arg any) int
	CrpcOpen(
		raddr *net.TCPAddr,
		id int,
		ldropHandler CrpcLDropFn,
		rdropHandler CrpcRDropFn,
	) (*CrpcSession, int)
	CrpcClose(s *CrpcSession)
	CrpcCredit(s *CrpcSession) uint64
	CrpcStatClear(s *CrpcSession)
	CrpcStatECreditRx(s *CrpcSession) uint64
	CrpcStatCreditExpired(s *CrpcSession) uint64
	CrpcStatCUpdateTx(s *CrpcSession) uint64
	CrpcStatRespRx(s *CrpcSession) uint64
	CrpcStatReqTx(s *CrpcSession) uint64
	CrpcStatReqDropped(s *CrpcSession) uint64
}
