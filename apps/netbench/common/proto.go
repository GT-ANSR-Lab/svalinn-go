package common

const (
	NetbenchStatReqMagic uint64 = 0xDEADBEEF
)

type NetbenchStatReq struct {
	Magic uint64
}

type NetbenchStatResp struct {
	Total          uint64
	Busy           uint64
	MemAccesses    uint64
	EnergyConsumed float64
	CUpdateRx      uint64
	ECreditTx      uint64
	CreditTx       uint64
	ReqRx          uint64
	ReqDropped     uint64
	RespTx         uint64
}

const (
	NetbenchReqMagic  uint64 = 0xFEED
	NetbenchRespMagic uint64 = 0xF00D
)

type NetbenchReq struct {
	Magic         uint64
	Opaque        uint64
	IsCpuBoundReq bool
	Hash          uint64
}

type NetbenchResp struct {
	Magic  uint64
	Opaque uint64
	WorkUs uint64
}
