package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	. "apps/netbench/common"
	. "ovldctlrpc"
	. "utils"

	"msemaphore"
	"perf"
)

// Constants
const (
	StatPort                int = 8002
	MaxProcs                int = 256
	MemBoundWorkerBufSize   int = 32768
	MemBoundWorkerNopPeriod int = 0
	MemBoundWorkerNopNum    int = 0
)

// Global program settings
var gSettings struct {
	ovldCtlAlgo RpcOpsType
	useMsem     bool
}

// RPC server object
var gServer *RpcServer

// Application-specific state
var gCpuBoundWorkers [MaxProcs]*SqrtWorker
var gMemBoundWorkers [MaxProcs]*MemBWAntagonistWorker

// Memory semaphore
var gMemSem *msemaphore.MemSemaphore

// Request handler
func NetbenchReqHandler(ctx *RpcServerCtx) {
	// Validate and parse the request
	req := (*NetbenchReq)(unsafe.Pointer(&ctx.ReqBuf[0]))
	if ctx.ReqLen != uint64(unsafe.Sizeof(NetbenchReq{})) {
		return
	}
	if req.Magic != NetbenchReqMagic {
		return
	}

	// Perform the synthetic work
	start := MicroTime()
	procID := runtime.GetProcId()
	if req.IsCpuBoundReq {
		gCpuBoundWorkers[procID].Work(req.WorkItr)
	} else {
		if gSettings.useMsem {
			if gMemSem.TryWait() {
				gMemBoundWorkers[procID].Work(req.WorkItr)
				gMemSem.Post()
			} else {
				// Skip work if semaphore not acquired
				ctx.Drop = true
				return
			}
		} else {
			gMemBoundWorkers[procID].Work(req.WorkItr)
		}
	}
	end := MicroTime()

	// Prepare the response
	resp := (*NetbenchResp)(unsafe.Pointer(&ctx.RespBuf[0]))
	resp.Magic = NetbenchRespMagic
	resp.Opaque = req.Opaque
	resp.WorkUs = uint64(end - start)
	ctx.RespLen = uint64(unsafe.Sizeof(NetbenchResp{}))
}

func NetbenchStatWorker(conn *net.TCPConn) {
	defer conn.Close()

	for {
		// Read stat request
		var req NetbenchStatReq
		n, err := ReadFull(conn, ToBytes(&req))
		if err != nil {
			if n == 0 {
				return
			}
			panic("Failed to read stat request")
		}

		// Check the magic value
		if req.Magic != NetbenchStatReqMagic {
			panic("Received invalid stat request magic")
		}

		// Get the CPU cycles used by this process
		data, err := os.ReadFile("/proc/self/stat")
		if err != nil {
			panic("Failed to read /proc/self/stat")
		}
		fields := strings.Fields(string(data))
		if len(fields) < 15 {
			panic("Failed to tokenize /proc/self/stat results")
		}
		utime, _ := strconv.ParseUint(fields[13], 10, 64)
		stime, _ := strconv.ParseUint(fields[14], 10, 64)
		busy := utime + stime

		// Get the total CPU cycles spent by the system
		data, err = os.ReadFile("/proc/stat")
		if err != nil {
			panic("Failed to read /proc/stat")
		}
		fields = strings.Fields(string(data))
		if len(fields) < 8 {
			panic("Failed to tokenize /proc/stat results")
		}
		total := uint64(0)
		for i := 0; i < 8; i++ {
			val, _ := strconv.ParseUint(fields[i], 10, 64)
			total += val
		}

		// Prepare the response
		resp := NetbenchStatResp{
			Total:          total,
			Busy:           busy,
			MemAccesses:    perf.MemPmcGetMemAccesses(),
			EnergyConsumed: perf.PowPmcGetEnergyConsumed(),
			CUpdateRx:      gServer.StatCUpdateRx(),
			ECreditTx:      gServer.StatECreditTx(),
			CreditTx:       gServer.StatCreditTx(),
			ReqRx:          gServer.StatReqRx(),
			ReqDropped:     gServer.StatReqDropped(),
			RespTx:         gServer.StatRespTx(),
		}

		// Send the stat response
		_, err = WriteFull(conn, ToBytes(&resp))
		if err != nil {
			return
		}
	}
}

func NetbenchStatServer() {

	// Create the stat listener
	laddr := &net.TCPAddr{
		IP:   net.IPv4zero,
		Port: StatPort,
	}
	l, err := net.ListenTCP("tcp", laddr)
	if err != nil {
		panic("Failed to open stat port")
	}
	defer l.Close()

	for {
		// Accept new connection
		conn, err := l.AcceptTCP()
		if err != nil {
			panic("Failed to accept stat connection")
		}

		// Handle the new connection
		go NetbenchStatWorker(conn)
	}
}

func main() {
	// Parse the command line arguments
	ovldCtlAlgo := flag.String("ovldctlalgo", "nocontrol",
		"Overload Controller Algorithm")
	useMsem := flag.Bool("usemsem", false,
		"Enable memory semaphore for memory-bound requests")
	flag.Parse()
	gSettings.useMsem = *useMsem

	// Interpret and Validate the arguments
	if *ovldCtlAlgo == "nocontrol" {
		gSettings.ovldCtlAlgo = RpcNoControlOps
	} else if *ovldCtlAlgo == "seda" {
		gSettings.ovldCtlAlgo = RpcSedaOps
	} else if *ovldCtlAlgo == "breakwater" {
		gSettings.ovldCtlAlgo = RpcBreakwaterOps
	} else if *ovldCtlAlgo == "protego" {
		gSettings.ovldCtlAlgo = RpcProtegoOps
	} else if *ovldCtlAlgo == "pcc" {
		gSettings.ovldCtlAlgo = RpcPccOps
	} else {
		panic("Invalid overload controller algorithm")
	}
	fmt.Printf("Selected \"%s\" overload control algorithm\n", *ovldCtlAlgo)

	// Initialize application-specific state
	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
		gCpuBoundWorkers[i], _ = NewSqrtWorker()
		gMemBoundWorkers[i], _ = NewMemBWAntagonistWorker(
			MemBoundWorkerBufSize,
			MemBoundWorkerNopPeriod,
			MemBoundWorkerNopNum)
	}

	// Create the memory semaphore
	if gSettings.useMsem {
		gMemSem = msemaphore.GetInstance()
	}

	// Start the stats server
	go NetbenchStatServer()

	// Create the RPC server
	gServer = NewRpcServer(gSettings.ovldCtlAlgo)
	if gServer == nil {
		panic("Failed to start the server")
	}
	ret := gServer.Enable(NetbenchReqHandler)
	if ret != 0 {
		panic("Failed to register the request handler")
	}

	// Wait forever
	fmt.Println("Server initialized and now waiting for connections...")
	select {}

	// Clean up the workers
	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
		gCpuBoundWorkers[i].Close()
		gMemBoundWorkers[i].Close()
	}
}
