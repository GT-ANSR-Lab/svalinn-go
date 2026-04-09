package ovldctlrpc

import (
	"fmt"
	"net"

	"ovldctlrpc/breakwater"
	"ovldctlrpc/common"
	"ovldctlrpc/nocontrol"
	"ovldctlrpc/protego"

	"perf"
)

/**
 * Common API
 */

// Type of overload control algorithm to use
type RpcOpsType uint8

const (
	// No overload control
	RpcNoControlOps RpcOpsType = iota
	// Breakwater
	RpcBreakwaterOps
	// Protego
	RpcProtegoOps
)

// The maximum request and response buffer size
const (
	RpcMaxBufferSize = common.SrpcBufSize
)

// We want all the users of the API, within the same runtime instance, to share
// the server and client ops. Hence, we create ops objects for each overload
// control algorithm here and each consumer will point to only one of the
// server and client ops, as per the given configuration.
var sncOps nocontrol.SncOps
var cncOps nocontrol.CncOps
var sbwOps breakwater.SbwOps
var cbwOps breakwater.CbwOps
var spgOps protego.SpgOps
var cpgOps protego.CpgOps

/**
 * Server API
 */

// A single RPC request context passed to the server-side request handler
// function. The user will get the request information through this object and
// needs to set the output information in this context structure. The fields are:
//
//	ReqLen    uint64
//	RespLen   uint64
//	ReqBuf    [RpcMaxBufferSize]byte
//	RespBuf   [RpcMaxBufferSize]byte
//	Drop      bool
type RpcServerCtx = common.SrpcCtx

// The RPC request handler function. The user can define a function of type
//
// func RequestHandler(ctx *RpcServerCtx) {}
type RpcServerFn = common.SrpcFn

// RPC server object
type RpcServer struct {
	ops common.SrpcOps
}

// Constructor for RPC server
func NewRpcServer(opsType RpcOpsType) *RpcServer {

	// Start the perf subsystem so that queueing-delay (and any other perf
	// counters the server algorithms rely on) are refreshed in the
	// background. This is a no-op if already initialized, and replaces
	// any direct use of runtime.QueueDelay on the hot path.
	perf.PerfInit()

	// Create the RPC server object
	s := &RpcServer{}

	// Initialize the ops
	switch opsType {
	case RpcNoControlOps:
		s.ops = &sncOps
	case RpcBreakwaterOps:
		s.ops = &sbwOps
	case RpcProtegoOps:
		s.ops = &spgOps
	default:
		fmt.Println("Invalid RPC ops")
		return nil
	}

	return s
}

// Register the request handler with the RPC server
func (s *RpcServer) Enable(handler RpcServerFn) int {
	return s.ops.SrpcEnable(handler)
}

// Drop a request at the server during request handling
func (s *RpcServer) Drop() {
	s.ops.SrpcDrop()
}

// Server-side stat functions
func (s *RpcServer) StatCUpdateRx() uint64 {
	return s.ops.SrpcStatCUpdateRx()
}

func (s *RpcServer) StatECreditTx() uint64 {
	return s.ops.SrpcStatECreditTx()
}

func (s *RpcServer) StatCreditTx() uint64 {
	return s.ops.SrpcStatCreditTx()
}

func (s *RpcServer) StatReqRx() uint64 {
	return s.ops.SrpcStatReqRx()
}

func (s *RpcServer) StatReqDropped() uint64 {
	return s.ops.SrpcStatReqDropped()
}

func (s *RpcServer) StatRespTx() uint64 {
	return s.ops.SrpcStatRespTx()
}

/**
 * Client API
 */

// Client-side context for a RPC request. This is used to provide information
// about a request that was dropped to the drop handlers. The fields are
//
//	Len uint64
//	Id  uint64
//	Ts  uint64
//	Buf [SrpcBufSize]byte
//	Arg any
type RpcClientCtx = common.CrpcCtx

// Local drop handler. If the request was dropped even before being set to the
// server. The signature should be as follows:
//
// func LocalDropHandler(ctx *RpcClientCtx)
type RpcClientLDropFn = common.CrpcLDropFn

// Remote drop handler. If the request was dropped at the server. The signature
// should be as follows:
//
// func RemoteDropHandler(buf []byte, len uint64, arg any)
type RpcClientRDropFn = common.CrpcRDropFn

// RPC client object
type RpcClient struct {
	sess *common.CrpcSession
	ops  common.CrpcOps
}

// Constructor for the RPC client object
//
// Connects with an RPC server at the provided remote address. Registers the
// drop handlers.
func NewRpcClient(
	opsType RpcOpsType,
	raddr *net.TCPAddr,
	id int,
	ldropHandler RpcClientLDropFn,
	rdropHandler RpcClientRDropFn) (*RpcClient, int) {

	// Create the RPC client object
	c := &RpcClient{}

	// Initialize the ops
	switch opsType {
	case RpcNoControlOps:
		c.ops = &cncOps
	case RpcBreakwaterOps:
		c.ops = &cbwOps
	case RpcProtegoOps:
		c.ops = &cpgOps
	default:
		fmt.Println("Invalid RPC ops")
		return nil, -1
	}

	// Set the TCP port
	raddr.Port = common.SrpcPort

	// Connect with the server
	sess, ret := c.ops.CrpcOpen(raddr, id, ldropHandler, rdropHandler)
	c.sess = sess

	return c, ret

}

// Register an additional server replica with the client
func (c *RpcClient) AddConnection(raddr *net.TCPAddr) int {
	raddr.Port = common.SrpcPort
	return c.ops.CrpcAddConnection(c.sess, raddr)
}

// Send a request to the server. The request is load-balanced based on the
// configured load-balancing policy for the underlying ops
func (c *RpcClient) Send(buf []byte, len uint64, hash int, arg any) int {
	return c.ops.CrpcSendOne(c.sess, buf, len, hash, arg)
}

// Receive a response from the specified connection (i.e., server replica)
func (c *RpcClient) Recv(buf []byte, len uint64, cidx int, arg any) int {
	return c.ops.CrpcRecvOne(c.sess.C[cidx], buf, len, arg)
}

// Number of server replicas registered
func (c *RpcClient) NConns() int {
	return c.sess.NConns
}

// Credits assigned to this client by the server
func (c *RpcClient) Credit() uint64 {
	return c.ops.CrpcCredit(c.sess)
}

// Client-side stats
func (c *RpcClient) StatClear() {
	c.ops.CrpcStatClear(c.sess)
}

func (c *RpcClient) StatECreditRx() uint64 {
	return c.ops.CrpcStatECreditRx(c.sess)
}

func (c *RpcClient) StatCreditExpired() uint64 {
	return c.ops.CrpcStatCreditExpired(c.sess)
}

func (c *RpcClient) StatCUpdateTx() uint64 {
	return c.ops.CrpcStatCUpdateTx(c.sess)
}

func (c *RpcClient) StatRespRx() uint64 {
	return c.ops.CrpcStatRespRx(c.sess)
}

func (c *RpcClient) StatReqTx() uint64 {
	return c.ops.CrpcStatReqTx(c.sess)
}

func (c *RpcClient) StatReqDropped() uint64 {
	return c.ops.CrpcStatReqDropped(c.sess)
}

// Close the client connection with the server
func (c *RpcClient) Close() {
	c.ops.CrpcClose(c.sess)
}

// Shutdown the connection with the server
// Shuts down both the read and write end of the connection
func (c *RpcClient) Shutdown() {
	for i := 0; i < c.sess.NConns; i++ {
		if c.sess.C[i] == nil {
			continue
		}
		c.sess.C[i].C.CloseRead()
		c.sess.C[i].C.CloseWrite()
	}
}
