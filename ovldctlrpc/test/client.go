package main

import (
	"fmt"
	"net"
	"os"
	"time"
	"unsafe"

	. "ovldctlrpc"
	. "utils"
)

type RequestMsg struct {
	num1 uint64
	num2 float64
}

type ResponseMsg struct {
	num1Sq uint64
	num2Sq float64
}

func main() {

	// Get the RPC server address
	raddr := &net.TCPAddr{
		IP: net.ParseIP("192.168.11.129"),
	}

	// Create the RPC client
	fmt.Println("Creating a RPC client connection with the server")
	client, ret := NewRpcClient(RpcNoControlOps, raddr, 0, nil, nil)
	if ret < 0 {
		fmt.Println("Failed to create the RPC client object")
		os.Exit(1)
	}

	time.Sleep(2 * time.Second)

	// Send request
	fmt.Println("Sending a request...")
	var req RequestMsg
	req.num1 = 101
	req.num2 = 55.123
	fmt.Printf("Sending Request: num1: %d, num2: %f\n", req.num1, req.num2)
	ret = client.Send(ToBytes(&req), uint64(unsafe.Sizeof(req)), 0, nil)
	if ret <= 0 {
		fmt.Println("Failed to send request")
		os.Exit(1)
	}

	time.Sleep(2 * time.Second)

	// Receive response
	fmt.Println("Receiving a response...")
	var resp ResponseMsg
	ret = client.Recv(ToBytes(&resp), uint64(unsafe.Sizeof(resp)), 0, nil)
	if ret <= 0 {
		fmt.Println("Failed to receive response")
		os.Exit(1)
	}
	fmt.Printf("Received Response: num1: %d, num2: %.3f\n", resp.num1Sq, resp.num2Sq)

	time.Sleep(2 * time.Second)

	fmt.Println("Closing the connection...")
	client.Close()
}
