package seda

// Magic numbers used in request-response messages
const (
	SdReqMagic  uint32 = 0x63727063
	SdRespMagic uint32 = 0x73727063
)

// Operation types
type SdOp uint32

const (
	SdOpCall      SdOp = iota // performs a procedure call
	SdOpWinUpdate             // just updates the window (no call)
	SdOpMax                   // maximum number of opcodes
)

// Header used for CLIENT -> SERVER
type CsdHdr struct {
	Magic  uint32
	Op     SdOp
	Len    uint64
	Id     uint64
	Ts     uint64
}

// Header used for SERVER -> CLIENT
type SsdHdr struct {
	Magic  uint32
	Op     SdOp
	Len    uint64
	Id     uint64
	Ts     uint64
}
