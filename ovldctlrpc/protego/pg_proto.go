package protego

// Magic numbers used in request-response messages
const (
	PgReqMagic  uint32 = 0x63727063
	PgRespMagic uint32 = 0x73727063
)

// Operation types
type PgOp uint32

const (
	PgOpCall   PgOp = iota // performs a procedure call
	PgOpCredit             // just updates the credit (no call)
	PgOpMax                // maximum number of opcodes
)

// Client flags
type PgCFlag uint8

const (
	PgCFlagDsync PgCFlag = 0x1
)

// Server flags
type PgSFlag uint8

const (
	PgSFlagDrop PgSFlag = 0x1
)

// Header used for CLIENT -> SERVER
type CpgHdr struct {
	Magic  uint32
	Op     PgOp
	Len    uint64
	Id     uint64
	Demand uint64
	TsSent uint64
	Flags  uint8
}

// Header used for SERVER -> CLIENT
type SpgHdr struct {
	Magic  uint32
	Op     PgOp
	Len    uint64
	Id     uint64
	Credit uint64
	TsSent uint64
	Flags  uint8
}
