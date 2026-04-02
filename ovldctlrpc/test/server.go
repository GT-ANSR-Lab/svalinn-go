package main

import (
	"fmt"
	"unsafe"

	. "ovldctlrpc"
)

type RequestMsg struct {
	num1 uint64
	num2 float64
}

type ResponseMsg struct {
	num1Sq uint64
	num2Sq float64
}

func RequestHandler(ctx *RpcServerCtx) {
	req := (*RequestMsg)(unsafe.Pointer(&ctx.ReqBuf[0]))
	fmt.Printf("Received Request: num1: %d, num2: %.3f\n", req.num1, req.num2)

	// Prepare the response
	resp := (*ResponseMsg)(unsafe.Pointer(&ctx.RespBuf[0]))
	resp.num1Sq = req.num1 * req.num1
	resp.num2Sq = req.num2 * req.num2
	ctx.RespLen = uint64(unsafe.Sizeof(ResponseMsg{}))
	fmt.Printf("Sending Response: num1: %d, num2: %.3f\n", resp.num1Sq, resp.num2Sq)
}

func main() {

	// Create the RPC server object
	fmt.Println("Creating the RPC server object")
	server := NewRpcServer(RpcNoControlOps)
	if server == nil {
		fmt.Println("Failed to create the RPC server object")
	}

	// Register the handler
	fmt.Println("Registering the RPC request handler")
	server.Enable(RequestHandler)

	// Wait infinitely
	fmt.Println("Waiting for the requests, indefinitely")
	for {
	}
}
