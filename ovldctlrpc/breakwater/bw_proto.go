package breakwater

// Magic numbers used in request-response messages
const (
	BwReqMagic  uint32 = 0x63727063
	BwRespMagic uint32 = 0x73727063
)

// Operation types
type BwOp uint32

const (
	BwOpCall   BwOp = iota // performs a procedure call
	BwOpCredit             // just updates the credit (no call)
	BwOpMax                // maximum number of opcodes
)

// Client flags
type BwCFlag uint8

const (
	BwCFlagDsync BwCFlag = 0x1
)

// Server flags
type BwSFlag uint8

const (
	BwSFlagDrop BwSFlag = 0x1
)

// Header used for CLIENT -> SERVER
type CbwHdr struct {
	Magic  uint32
	Op     BwOp
	Len    uint64
	Id     uint64
	Demand uint64
	TsSent uint64
	Flags  uint8
}

// Header used for SERVER -> CLIENT
type SbwHdr struct {
	Magic  uint32
	Op     BwOp
	Len    uint64
	Id     uint64
	Credit uint64
	TsSent uint64
	Flags  uint8
}
