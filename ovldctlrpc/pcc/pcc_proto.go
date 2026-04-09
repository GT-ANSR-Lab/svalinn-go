package pcc

// Magic numbers used in request-response messages
const (
	PccReqMagic  uint32 = 0x63727063
	PccRespMagic uint32 = 0x73727063
)

// Operation types
type PccOp uint32

const (
	PccOpCall   PccOp = iota // performs a procedure call
	PccOpCredit             // just updates the credit (no call)
	PccOpMax                // maximum number of opcodes
)

// Client flags
type PccCFlag uint8

const (
	PccCFlagDsync PccCFlag = 0x1
)

// Server flags
type PccSFlag uint8

const (
	PccSFlagDrop PccSFlag = 0x1
)

// Header used for CLIENT -> SERVER
type CpccHdr struct {
	Magic  uint32
	Op     PccOp
	Len    uint64
	Id     uint64
	Demand uint64
	TsSent uint64
	Flags  uint8
}

// Header used for SERVER -> CLIENT
type SpccHdr struct {
	Magic  uint32
	Op     PccOp
	Len    uint64
	Id     uint64
	Credit uint64
	TsSent uint64
	Flags  uint8
}
