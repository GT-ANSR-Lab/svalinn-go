package main

import (
	"bytes"
	"encoding/gob"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	. "apps/netbench/common"
	. "ovldctlrpc"
	. "utils"
)

// Constants
const (
	StatPort         int = 8002
	WarmUpTimeUs     int = 10000000
	MaxCatchUpTimeUs int = 5000
)

// Client settings type
type Settings struct {
	IsMasterClient    bool
	ServerIP          string
	MasterClientIP    string
	OvldCtlAlgo       RpcOpsType
	NumConns          int
	NumAgents         int
	Slo               int
	OfferedLoad       float64
	DurationS         int
	CpuBoundWorkIters int
	MemBoundWorkIters int
	CpuBoundWorkPerc  int
}

// Global client settings
var gSettings Settings

// TCP port number of NetBarrier client connections
const (
	NetBarrierPort int = 5000
)

// Barrier-like synchronization primitive for clients across different machines
type NetBarrier struct {
	isMaster       bool
	numAgents      int
	masterListener *net.TCPListener
	conns          []*net.TCPConn
}

func MasterNetBarrierInit(nb *NetBarrier, settings *Settings) {

	// Init the barrier
	nb.isMaster = true
	nb.numAgents = settings.NumAgents - 1

	// Init a few settings
	settings.IsMasterClient = true
	settings.NumConns = settings.NumConns / settings.NumAgents
	settings.OfferedLoad = (settings.OfferedLoad / float64(settings.NumAgents)) / float64(settings.NumConns)

	// Serialize the settings
	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(*settings)
	if err != nil {
		panic("Failed to serialize client settings")
	}
	serialSettings := buf.Bytes()
	serialSettingsLen := len(serialSettings)

	// Create the master client listener
	laddr := &net.TCPAddr{
		IP:   net.IPv4zero,
		Port: NetBarrierPort,
	}
	nb.masterListener, err = net.ListenTCP("tcp", laddr)
	if err != nil {
		panic("Failed to open net barrier port")
	}

	// Synchronize the settings with the non-master clients
	for i := 0; i < nb.numAgents; i++ {
		// Accept the connection
		conn, err := nb.masterListener.AcceptTCP()
		if err != nil {
			panic("Failed to accept net barrier connection")
		}

		// Update the client (self) IP
		localAddr, _ := conn.LocalAddr().(*net.TCPAddr)
		settings.MasterClientIP = localAddr.IP.String()

		// Send the settings to the non-master clients
		WriteFull(conn, ToBytes(&serialSettingsLen))
		WriteFull(conn, serialSettings)

		// Save the connection
		nb.conns = append(nb.conns, conn)
	}
}

func NonMasterNetBarrierInit(nb *NetBarrier, settings *Settings) {
	// Init the barrier
	nb.isMaster = false

	// Connect to the master client
	raddr := &net.TCPAddr{
		IP:   net.ParseIP(settings.MasterClientIP),
		Port: NetBarrierPort,
	}
	conn, err := net.DialTCP("tcp", nil, raddr)
	if err != nil {
		panic("Failed to connect with net barrier server")
	}

	// Get the settings from the master client
	var serialSettingsLen int = 0
	ReadFull(conn, ToBytes(&serialSettingsLen))
	serialSettings := make([]byte, serialSettingsLen)
	ReadFull(conn, serialSettings)
	err = gob.NewDecoder(bytes.NewReader(serialSettings)).Decode(settings)
	if err != nil {
		panic("Failed to deserialize the settings")
	}

	// Init a few settings
	settings.IsMasterClient = false

	// Save the connection
	nb.conns = append(nb.conns, conn)
}

func NewNetBarrier(IsMasterClient bool, settings *Settings) *NetBarrier {
	nb := &NetBarrier{}

	if IsMasterClient {
		MasterNetBarrierInit(nb, settings)
	} else {
		NonMasterNetBarrierInit(nb, settings)
	}

	return nb
}

func (nb *NetBarrier) Wait() {
	var dummy byte

	if nb.isMaster {
		for i := 0; i < nb.numAgents; i++ {
			ReadFull(nb.conns[i], ToBytes(&dummy))
		}
		for i := 0; i < nb.numAgents; i++ {
			WriteFull(nb.conns[i], ToBytes(&dummy))
		}
	} else {
		WriteFull(nb.conns[0], ToBytes(&dummy))
		ReadFull(nb.conns[0], ToBytes(&dummy))
	}
}

func (nb *NetBarrier) StartExperiment() {
	nb.Wait()
}

func (nb *NetBarrier) EndExperiment(cstat *CStat, units *[]WorkUnit) {
	if nb.isMaster {
		for i := 0; i < nb.numAgents; i++ {
			var peerCstat CStat
			ReadFull(nb.conns[i], ToBytes(&peerCstat))
			cstat.OfferedRps += peerCstat.OfferedRps
			cstat.Rps += peerCstat.Rps
			cstat.CpuBoundWorkRps += peerCstat.CpuBoundWorkRps
			cstat.MemBoundWorkRps += peerCstat.MemBoundWorkRps
			cstat.Goodput += peerCstat.Goodput
			cstat.ECreditRxPps += peerCstat.ECreditRxPps
			cstat.CUpdateTxPps += peerCstat.CUpdateTxPps
			cstat.CreditExpiredCps += peerCstat.CreditExpiredCps
			cstat.RespRxPps += peerCstat.RespRxPps
			cstat.ReqTxPps += peerCstat.ReqTxPps
			cstat.ReqDroppedRps += peerCstat.ReqDroppedRps
		}

		for i := 0; i < nb.numAgents; i++ {
			serialUnitsLen := 0
			ReadFull(nb.conns[i], ToBytes(&serialUnitsLen))
			serialUnits := make([]byte, serialUnitsLen)
			ReadFull(nb.conns[i], serialUnits)
			var peerUnits []WorkUnit
			err := gob.NewDecoder(bytes.NewReader(serialUnits)).Decode(&peerUnits)
			if err != nil {
				panic("Failed to deserialize work units")
			}
			*units = append(*units, peerUnits...)
		}
	} else {
		// Send the client stats
		WriteFull(nb.conns[0], ToBytes(cstat))

		// Send the work units
		var buf bytes.Buffer
		err := gob.NewEncoder(&buf).Encode(*units)
		if err != nil {
			panic("Failed to serialize work units")
		}
		serialUnits := buf.Bytes()
		serialUnitsLen := len(serialUnits)
		WriteFull(nb.conns[0], ToBytes(&serialUnitsLen))
		WriteFull(nb.conns[0], serialUnits)
	}
}

func (nb *NetBarrier) Close() {
	if nb.isMaster {
		for i := 0; i < nb.numAgents; i++ {
			nb.conns[i].Close()
		}
		nb.masterListener.Close()
	} else {
		nb.conns[0].Close()
	}
}

// A single request-response state maintained by the client.
//
// NOTE: Should be serializable by default
type WorkUnit struct {
	IsSuccess     bool
	IsCpuBoundReq bool
	SchedStartUs  float64
	StartUs       float64
	DurationUs    float64
	Hash          int
	Req           NetbenchReq
	WorkUs        uint64
}

// Server-side statistics
type SStat struct {
	CpuUsage     float64
	MembwUsage   float64
	PowerUsage   float64
	CUpdateRxPps float64
	ECreditTxPps float64
	CreditTxCps  float64
	ReqRxPps     float64
	RespTxPps    float64
	ReqDropRate  float64
}

// Client-side statistics
type CStat struct {
	OfferedRps       float64
	Rps              float64
	CpuBoundWorkRps  float64
	MemBoundWorkRps  float64
	Goodput          float64
	ECreditRxPps     float64
	CUpdateTxPps     float64
	CreditExpiredCps float64
	RespRxPps        float64
	ReqTxPps         float64
	ReqDroppedRps    float64

	MinDurationUs             float64
	MeanDurationUs            float64
	P50DurationUs             float64
	P90DurationUs             float64
	P99DurationUs             float64
	MaxDurationUs             float64
	P50CpuBoundWorkDurationUs float64
	P90CpuBoundWorkDurationUs float64
	P99CpuBoundWorkDurationUs float64
	P50MemBoundWorkDurationUs float64
	P90MemBoundWorkDurationUs float64
	P99MemBoundWorkDurationUs float64

	P50CpuBoundWorkStUs float64
	P90CpuBoundWorkStUs float64
	P99CpuBoundWorkStUs float64
	AvgCpuBoundWorkStUs float64
	P50MemBoundWorkStUs float64
	P90MemBoundWorkStUs float64
	P99MemBoundWorkStUs float64
	AvgMemBoundWorkStUs float64
}

func GenerateWork(units *[]WorkUnit) {

	// Get the average inter request arrival time
	avgInterArrivalUs := 1e6 / gSettings.OfferedLoad

	// Get the experiment time in microseconds
	durationUs := float64(gSettings.DurationS) * 1e6

	var currUs float64 = 0.0
	var id uint64 = 0

	for currUs < durationUs {
		// Generate a few values for the work unit
		hash := rand.Int()
		isCpuBoundReq := hash%100 < gSettings.CpuBoundWorkPerc
		workItr := uint64(0)
		if isCpuBoundReq {
			workItr = uint64(gSettings.CpuBoundWorkIters)
		} else {
			workItr = uint64(gSettings.MemBoundWorkIters)
		}
		currUs += rand.ExpFloat64() * avgInterArrivalUs

		// Prepare the work unit
		wu := WorkUnit{
			IsSuccess:     false,
			IsCpuBoundReq: isCpuBoundReq,
			SchedStartUs:  currUs,
			StartUs:       0.0,
			DurationUs:    0.0,
			Hash:          hash,
			Req: NetbenchReq{
				Magic:         NetbenchReqMagic,
				Opaque:        id,
				IsCpuBoundReq: isCpuBoundReq,
				WorkItr:       workItr,
			},
		}
		*units = append(*units, wu)

		id++
	}
}

func ClientWorker(
	conn *RpcClient,
	units *[]WorkUnit,
	starterWg *sync.WaitGroup,
	starter2Wg *sync.WaitGroup,
	enderWg *sync.WaitGroup) {

	// Generate the workload trace
	GenerateWork(units)

	// Waitgroup to synchronize the sender and the receiver threads
	var senderReceiverWg sync.WaitGroup
	senderReceiverWg.Add(1)

	// Start the receiver thread
	go func() {
		var resp_buf [RpcMaxBufferSize]byte

		for {
			// Receive the response
			ret := conn.Recv(resp_buf[:], RpcMaxBufferSize, 0, units)
			if ret != int(unsafe.Sizeof(NetbenchResp{})) {
				if ret <= 0 {
					// The connection was closed
					break
				}
				panic("Received incorrect response")
			}

			// Validate the response
			resp := (*NetbenchResp)(unsafe.Pointer(&resp_buf[0]))
			if resp.Magic != NetbenchRespMagic {
				panic("Received invalid magic")
			}

			// Update the work unit
			now := MicroTime()
			idx := resp.Opaque
			wu := &((*units)[idx])
			wu.DurationUs = float64(now) - wu.StartUs
			wu.IsSuccess = true
			wu.WorkUs = resp.WorkUs
		}

		// Signal the sender to exit
		senderReceiverWg.Done()
	}()

	// Synchronize the start of the load generation
	starterWg.Done()
	starter2Wg.Wait()

	expStartUs := MicroTime()

	for i := 0; i < len(*units); i++ {
		// Get the work unit
		wu := &((*units)[i])

		// Check if we need to sleep
		nowUs := MicroTime()
		if nowUs-expStartUs < uint64(wu.SchedStartUs) {
			time.Sleep(time.Duration(uint64(wu.SchedStartUs)-(nowUs-expStartUs)) * time.Microsecond)
		}

		// Check if we should drop this sample, as it is too late to send now
		nowUs = MicroTime()
		if nowUs-expStartUs-uint64(wu.SchedStartUs) > uint64(MaxCatchUpTimeUs) {
			continue
		}

		// Record the start time for this request
		wu.StartUs = float64(nowUs)

		// Send the request
		req_buf := ToBytes(&wu.Req)
		ret := conn.Send(req_buf, uint64(len(req_buf)), wu.Hash, units)
		if ret != int(unsafe.Sizeof(NetbenchReq{})) {
			panic("Failed to send")
		}
	}

	// Cleanup
	conn.Shutdown()
	senderReceiverWg.Wait()
	enderWg.Done()
}

func LocalDropHandler(ctx *RpcClientCtx) {
	req := (*NetbenchReq)(unsafe.Pointer(&ctx.Buf[0]))
	idx := req.Opaque
	units := ctx.Arg.(*[]WorkUnit)
	(*units)[idx].DurationUs = 0.0
	(*units)[idx].IsSuccess = false
}

func RemoteDropHandler(buf []byte, len uint64, arg any) {
	req := (*NetbenchReq)(unsafe.Pointer(&buf[0]))
	idx := req.Opaque
	units := arg.(*[]WorkUnit)
	(*units)[idx].DurationUs = 0.0
	(*units)[idx].IsSuccess = false
}

func ReadRpcServerStat(sstat *NetbenchStatResp) {
	// Connect with the RPC stat server
	raddr := &net.TCPAddr{
		IP:   net.ParseIP(gSettings.ServerIP),
		Port: StatPort,
	}
	conn, err := net.DialTCP("tcp", nil, raddr)
	if err != nil {
		panic("Failed to connect with netbench stat server")
	}
	defer conn.Close()

	// Send the request
	req := NetbenchStatReq{
		Magic: NetbenchStatReqMagic,
	}
	_, err = WriteFull(conn, ToBytes(&req))
	if err != nil {
		panic("Failed to send netbench stat request")
	}

	// Recieve the response
	_, err = ReadFull(conn, ToBytes(sstat))
	if err != nil {
		panic("Failed to receive netbench stat response")
	}
}

func RunExperiment() {

	// Create the network barriers and synchronize all the clients
	netBarrier := NewNetBarrier(gSettings.IsMasterClient, &gSettings)

	// Create RPC client connections
	var conns []*RpcClient
	raddr := &net.TCPAddr{
		IP: net.ParseIP(gSettings.ServerIP),
	}
	for i := 0; i < gSettings.NumConns; i++ {
		conn, err := NewRpcClient(gSettings.OvldCtlAlgo, raddr, i+1,
			LocalDropHandler, RemoteDropHandler)
		if err != 0 {
			panic("Failed to connect with the RPC server")
		}
		conns = append(conns, conn)
	}

	// Waitgroups to synchronize the start and end of the experiment
	var starterWg sync.WaitGroup
	var starter2Wg sync.WaitGroup
	var enderWg sync.WaitGroup
	starterWg.Add(gSettings.NumConns)
	starter2Wg.Add(1)
	enderWg.Add(gSettings.NumConns)

	// Create the work unit arrays to collect the results from the workers
	workUnits := make([][]WorkUnit, gSettings.NumConns)
	for i := 0; i < gSettings.NumConns; i++ {
		workUnits[i] = make([]WorkUnit, 0)
	}

	// Spawn the worker threads
	for i := 0; i < gSettings.NumConns; i++ {
		go ClientWorker(conns[i], &workUnits[i], &starterWg, &starter2Wg, &enderWg)
	}

	// Synchronize the start of the experiment across all clients
	starterWg.Wait()
	netBarrier.Wait()
	starter2Wg.Done()

	var sstatStart NetbenchStatResp
	var sstatFinish NetbenchStatResp

	// Experiment started started
	startUs := MicroTime()

	// Wait for warmup amount of time
	time.Sleep(time.Duration(WarmUpTimeUs) * time.Microsecond)
	for i := 0; i < gSettings.NumConns; i++ {
		conns[i].StatClear()
	}

	// Read the snapshot of the stats after the warmup period
	if gSettings.IsMasterClient {
		ReadRpcServerStat(&sstatStart)
	}

	// Wait for the workers to finish
	enderWg.Wait()

	// Experiment started finished
	finishUs := MicroTime()

	// Read the snapshot of the stats at the end
	if gSettings.IsMasterClient {
		ReadRpcServerStat(&sstatFinish)
	}

	// Get the elapsed time in seconds
	elapsedS := float64(finishUs-startUs-uint64(WarmUpTimeUs)) / 1e6

	var cstat CStat

	// Calculate the client throughput stats
	var offered uint64 = 0
	var resps uint64 = 0
	var goodResps uint64 = 0
	var cpuBoundWorkResps uint64 = 0
	var memBoundWorkResps uint64 = 0
	var clientDropped uint64 = 0
	for i := 0; i < gSettings.NumConns; i++ {
		// Remove the requests before the warmup time
		RemoveIf(&workUnits[i], func(p *WorkUnit) bool {
			return p.SchedStartUs+p.DurationUs < float64(WarmUpTimeUs)
		})

		offered += uint64(len(workUnits[i]))

		// Remove local drops
		clientDropped += RemoveIf(&workUnits[i], func(p *WorkUnit) bool {
			return p.DurationUs == 0.0
		})

		resps += CountIf(&workUnits[i], func(p *WorkUnit) bool {
			return p.IsSuccess
		})
		goodResps += CountIf(&workUnits[i], func(p *WorkUnit) bool {
			return p.IsSuccess && p.DurationUs < float64(gSettings.Slo)
		})
		cpuBoundWorkResps += CountIf(&workUnits[i], func(p *WorkUnit) bool {
			return p.IsSuccess && p.IsCpuBoundReq
		})
		memBoundWorkResps += CountIf(&workUnits[i], func(p *WorkUnit) bool {
			return p.IsSuccess && !p.IsCpuBoundReq
		})
	}
	cstat.OfferedRps = float64(offered) / elapsedS
	cstat.Rps = float64(resps) / elapsedS
	cstat.Goodput = float64(goodResps) / elapsedS
	cstat.CpuBoundWorkRps = float64(cpuBoundWorkResps) / elapsedS
	cstat.MemBoundWorkRps = float64(memBoundWorkResps) / elapsedS
	cstat.ReqDroppedRps = float64(clientDropped) / elapsedS

	// Calculate the client RPC-layer throughput stats
	var eCreditRx uint64 = 0
	var cUpdateTx uint64 = 0
	var creditExpired uint64 = 0
	var respRx uint64 = 0
	var reqTx uint64 = 0
	for i := 0; i < gSettings.NumConns; i++ {
		eCreditRx += conns[i].StatECreditRx()
		cUpdateTx += conns[i].StatCUpdateTx()
		creditExpired += conns[i].StatCreditExpired()
		respRx += conns[i].StatRespRx()
		reqTx += conns[i].StatReqTx()
		conns[i].Close()
	}
	cstat.ECreditRxPps = float64(eCreditRx) / elapsedS
	cstat.CUpdateTxPps = float64(cUpdateTx) / elapsedS
	cstat.CreditExpiredCps = float64(creditExpired) / elapsedS
	cstat.RespRxPps = float64(respRx) / elapsedS
	cstat.ReqTxPps = cstat.ReqTxPps / elapsedS

	// Flatten the work units across all connections
	aggWorkUnitsLen := 0
	for i := 0; i < gSettings.NumConns; i++ {
		aggWorkUnitsLen += len(workUnits[i])
	}
	aggWorkUnits := make([]WorkUnit, 0, aggWorkUnitsLen)
	for i := 0; i < gSettings.NumConns; i++ {
		aggWorkUnits = append(aggWorkUnits, workUnits[i]...)
	}

	// Synchronize the stats from all the clients on different machines
	netBarrier.EndExperiment(&cstat, &aggWorkUnits)

	// Non-master clients can exit now
	if !gSettings.IsMasterClient {
		return
	}

	// Calculate the client side latency stats after aggregation
	RemoveIf(&aggWorkUnits, func(p *WorkUnit) bool {
		return !p.IsSuccess
	})
	sort.Slice(aggWorkUnits, func(i, j int) bool {
		return aggWorkUnits[i].DurationUs < aggWorkUnits[j].DurationUs
	})

	// Calculate the CPU-bound work latencies
	cpuBoundWorkUnitsLen := CountIf(&aggWorkUnits, func(p *WorkUnit) bool {
		return p.IsCpuBoundReq
	})
	cpuBoundWorkUnits := make([]WorkUnit, 0, cpuBoundWorkUnitsLen)
	CopyIf(&cpuBoundWorkUnits, &aggWorkUnits, func(p *WorkUnit) bool {
		return p.IsCpuBoundReq
	})
	if cpuBoundWorkUnitsLen > 0 {
		cstat.P50CpuBoundWorkDurationUs = cpuBoundWorkUnits[(cpuBoundWorkUnitsLen*50)/100].DurationUs
		cstat.P90CpuBoundWorkDurationUs = cpuBoundWorkUnits[(cpuBoundWorkUnitsLen*90)/100].DurationUs
		cstat.P99CpuBoundWorkDurationUs = cpuBoundWorkUnits[(cpuBoundWorkUnitsLen*99)/100].DurationUs
	}

	// Calculate the Mem-bound work latencies
	memBoundWorkUnitsLen := CountIf(&aggWorkUnits, func(p *WorkUnit) bool {
		return !p.IsCpuBoundReq
	})
	memBoundWorkUnits := make([]WorkUnit, 0, memBoundWorkUnitsLen)
	CopyIf(&memBoundWorkUnits, &aggWorkUnits, func(p *WorkUnit) bool {
		return !p.IsCpuBoundReq
	})
	if memBoundWorkUnitsLen > 0 {
		cstat.P50MemBoundWorkDurationUs = memBoundWorkUnits[(memBoundWorkUnitsLen*50)/100].DurationUs
		cstat.P90MemBoundWorkDurationUs = memBoundWorkUnits[(memBoundWorkUnitsLen*90)/100].DurationUs
		cstat.P99MemBoundWorkDurationUs = memBoundWorkUnits[(memBoundWorkUnitsLen*99)/100].DurationUs
	}

	// Calculate the overall work latencies
	aggWorkUnitsLen = len(aggWorkUnits)
	if aggWorkUnitsLen > 0 {
		cstat.MinDurationUs = aggWorkUnits[0].DurationUs
		cstat.MeanDurationUs = Accumulate(&aggWorkUnits, 0.0, func(sum float64, p *WorkUnit) float64 {
			return sum + p.DurationUs
		}) / float64(aggWorkUnitsLen)
		cstat.P50DurationUs = aggWorkUnits[(aggWorkUnitsLen*50)/100].DurationUs
		cstat.P90DurationUs = aggWorkUnits[(aggWorkUnitsLen*90)/100].DurationUs
		cstat.P99DurationUs = aggWorkUnits[(aggWorkUnitsLen*99)/100].DurationUs
		cstat.MaxDurationUs = aggWorkUnits[aggWorkUnitsLen-1].DurationUs
	}

	// Calculate the CPU-bound service time
	sort.Slice(aggWorkUnits, func(i, j int) bool {
		return aggWorkUnits[i].WorkUs < aggWorkUnits[j].WorkUs
	})

	// Calculate the CPU-bound work latencies
	cpuBoundWorkUnitsStLen := CountIf(&aggWorkUnits, func(p *WorkUnit) bool {
		return p.IsCpuBoundReq
	})
	cpuBoundWorkUnitsSt := make([]WorkUnit, 0, cpuBoundWorkUnitsStLen)
	CopyIf(&cpuBoundWorkUnitsSt, &aggWorkUnits, func(p *WorkUnit) bool {
		return p.IsCpuBoundReq
	})
	if cpuBoundWorkUnitsStLen > 0 {
		cstat.P50CpuBoundWorkStUs = float64(cpuBoundWorkUnitsSt[(cpuBoundWorkUnitsStLen*50)/100].WorkUs)
		cstat.P90CpuBoundWorkStUs = float64(cpuBoundWorkUnitsSt[(cpuBoundWorkUnitsStLen*90)/100].WorkUs)
		cstat.P99CpuBoundWorkStUs = float64(cpuBoundWorkUnitsSt[(cpuBoundWorkUnitsStLen*99)/100].WorkUs)
		cstat.AvgCpuBoundWorkStUs = Accumulate(&cpuBoundWorkUnitsSt, 0.0, func(sum float64, p *WorkUnit) float64 {
			return sum + float64(p.WorkUs)
		}) / float64(cpuBoundWorkUnitsStLen)
	}

	// Calculate the Mem-bound work latencies
	memBoundWorkUnitsStLen := CountIf(&aggWorkUnits, func(p *WorkUnit) bool {
		return !p.IsCpuBoundReq
	})
	memBoundWorkUnitsSt := make([]WorkUnit, 0, memBoundWorkUnitsStLen)
	CopyIf(&memBoundWorkUnitsSt, &aggWorkUnits, func(p *WorkUnit) bool {
		return !p.IsCpuBoundReq
	})
	if memBoundWorkUnitsStLen > 0 {
		cstat.P50MemBoundWorkStUs = float64(memBoundWorkUnitsSt[(memBoundWorkUnitsStLen*50)/100].WorkUs)
		cstat.P90MemBoundWorkStUs = float64(memBoundWorkUnitsSt[(memBoundWorkUnitsStLen*90)/100].WorkUs)
		cstat.P99MemBoundWorkStUs = float64(memBoundWorkUnitsSt[(memBoundWorkUnitsStLen*99)/100].WorkUs)
		cstat.AvgMemBoundWorkStUs = Accumulate(&memBoundWorkUnitsSt, 0.0, func(sum float64, p *WorkUnit) float64 {
			return sum + float64(p.WorkUs)
		}) / float64(memBoundWorkUnitsStLen)
	}

	// Calculate the server side stats
	var sstat SStat
	total := sstatFinish.Total - sstatStart.Total
	busy := sstatFinish.Busy - sstatStart.Busy
	memAccesses := sstatFinish.MemAccesses - sstatStart.MemAccesses
	energyConsumed := sstatFinish.EnergyConsumed - sstatStart.EnergyConsumed
	cUpdateRx := sstatFinish.CUpdateRx - sstatStart.CUpdateRx
	eCreditTx := sstatFinish.ECreditTx - sstatStart.ECreditTx
	creditTx := sstatFinish.CreditTx - sstatStart.CreditTx
	reqRx := sstatFinish.ReqRx - sstatStart.ReqRx
	reqDropped := sstatFinish.ReqDropped - sstatStart.ReqDropped
	respTx := sstatFinish.RespTx - sstatStart.RespTx
	sstat.CpuUsage = float64(busy) / float64(total)
	sstat.MembwUsage = float64(memAccesses) / elapsedS
	sstat.PowerUsage = energyConsumed / elapsedS
	sstat.CUpdateRxPps = float64(cUpdateRx) / elapsedS
	sstat.ECreditTxPps = float64(eCreditTx) / elapsedS
	sstat.CreditTxCps = float64(creditTx) / elapsedS
	sstat.ReqRxPps = float64(reqRx) / elapsedS
	sstat.ReqDropRate = float64(reqDropped) / float64(reqRx)
	sstat.RespTxPps = float64(respTx) / elapsedS

	// Print the stats
	PrintStats(&cstat, &sstat)

	// Close the network barrier
	netBarrier.Close()
}

func PrintStats(cstat *CStat, sstat *SStat) {

	var sb strings.Builder

	fmt.Fprintf(&sb, "%d", gSettings.NumConns*gSettings.NumAgents)
	fmt.Fprintf(&sb, ",%.3f", cstat.OfferedRps)
	fmt.Fprintf(&sb, ",%.3f", cstat.Rps)
	fmt.Fprintf(&sb, ",%.3f", cstat.CpuBoundWorkRps)
	fmt.Fprintf(&sb, ",%.3f", cstat.MemBoundWorkRps)
	fmt.Fprintf(&sb, ",%.3f", cstat.Goodput)
	fmt.Fprintf(&sb, ",%.3f", sstat.CpuUsage)
	fmt.Fprintf(&sb, ",%.3f", sstat.MembwUsage)
	fmt.Fprintf(&sb, ",%.3f", sstat.PowerUsage)
	fmt.Fprintf(&sb, ",%.3f", cstat.MinDurationUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.MeanDurationUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P50DurationUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P50CpuBoundWorkDurationUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P50MemBoundWorkDurationUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P90DurationUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P90CpuBoundWorkDurationUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P90MemBoundWorkDurationUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P99DurationUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P99CpuBoundWorkDurationUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P99MemBoundWorkDurationUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.MaxDurationUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P50CpuBoundWorkStUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P90CpuBoundWorkStUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P99CpuBoundWorkStUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.AvgCpuBoundWorkStUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P50MemBoundWorkStUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P90MemBoundWorkStUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.P99MemBoundWorkStUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.AvgMemBoundWorkStUs)
	fmt.Fprintf(&sb, ",%.3f", cstat.ECreditRxPps)
	fmt.Fprintf(&sb, ",%.3f", cstat.CUpdateTxPps)
	fmt.Fprintf(&sb, ",%.3f", cstat.CreditExpiredCps)
	fmt.Fprintf(&sb, ",%.3f", cstat.RespRxPps)
	fmt.Fprintf(&sb, ",%.3f", cstat.ReqTxPps)
	fmt.Fprintf(&sb, ",%.3f", cstat.ReqDroppedRps)
	fmt.Fprintf(&sb, ",%.3f", sstat.CUpdateRxPps)
	fmt.Fprintf(&sb, ",%.3f", sstat.ECreditTxPps)
	fmt.Fprintf(&sb, ",%.3f", sstat.CreditTxCps)
	fmt.Fprintf(&sb, ",%.3f", sstat.ReqRxPps)
	fmt.Fprintf(&sb, ",%.3f", sstat.RespTxPps)
	fmt.Fprintf(&sb, ",%.3f", sstat.ReqDropRate)
	fmt.Fprintf(&sb, "\n")

	// Save the stats to a file (in append-only mode)
	file, err := os.OpenFile("output.csv",
		os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	if _, err := file.WriteString(sb.String()); err != nil {
		panic(err)
	}

	// Print the stats to stdout
	fmt.Print(sb.String())
}

func main() {
	// Common argument indicating whether the client is master or non-master
	clientType := flag.String("clienttype", "", "Type of client (client, i.e., a master or agent, i.e., a non-master)")

	// Master client arguments
	var (
		// Settings common to any load generator client
		serverIP    = flag.String("server", "127.0.0.1", "Server IP address")
		ovldCtlAlgo = flag.String("ovldctlalgo", "nocontrol", "Overload control algorithm (e.g., nocontrol, seda, breakwater, protego, pcc)")
		numConns    = flag.Int("connections", 100, "Number of connections")
		numAgents   = flag.Int("agents", 1, "Number of clients/agents")
		slo         = flag.Int("slo", 1000, "SLO (Service Level Objective) in us")
		offeredLoad = flag.Float64("load", 100000.0, "Offered load")
		durationS   = flag.Int("duration", 10, "Test duration in seconds")
		// Workload specific settings
		cpuIters = flag.Int("cpuiters", 290, "CPU-bound work iterations")
		memIters = flag.Int("memiters", 500, "Memory-bound work iterations")
		cpuPerc  = flag.Int("cpuperc", 80, "CPU-bound work percentage")
	)

	// Non-master client arguments
	var (
		masterClientIP = flag.String("master", "127.0.0.1", "Master Client IP address")
	)

	// Parse the command line arguments
	flag.Parse()

	// Validate the arguments
	if *clientType == "" {
		panic("Client type must be provided [should be client/agent]")
	}

	// Initialize the global program settings
	if *clientType == "client" {
		gSettings.IsMasterClient = true
		gSettings.ServerIP = *serverIP
		if *ovldCtlAlgo == "nocontrol" {
			gSettings.OvldCtlAlgo = RpcNoControlOps
		} else if *ovldCtlAlgo == "seda" {
			gSettings.OvldCtlAlgo = RpcSedaOps
		} else if *ovldCtlAlgo == "breakwater" {
			gSettings.OvldCtlAlgo = RpcBreakwaterOps
		} else if *ovldCtlAlgo == "protego" {
			gSettings.OvldCtlAlgo = RpcProtegoOps
		} else if *ovldCtlAlgo == "pcc" {
			gSettings.OvldCtlAlgo = RpcPccOps
		} else {
			panic("Invalid overload controller algorithm")
		}
		gSettings.NumConns = *numConns
		gSettings.NumAgents = *numAgents
		gSettings.Slo = *slo
		gSettings.OfferedLoad = *offeredLoad
		gSettings.DurationS = *durationS
		gSettings.CpuBoundWorkIters = *cpuIters
		gSettings.MemBoundWorkIters = *memIters
		gSettings.CpuBoundWorkPerc = *cpuPerc
	} else if *clientType == "agent" {
		gSettings.IsMasterClient = false
		gSettings.MasterClientIP = *masterClientIP
	} else {
		panic("Invalid client type provided [should be client/agent]")
	}

	// Run the experiment
	RunExperiment()
}
