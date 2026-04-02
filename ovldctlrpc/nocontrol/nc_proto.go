package nocontrol

// Magic numbers used in request-response messages
const (
	NcReqMagic  uint32 = 0x63727063
	NcRespMagic uint32 = 0x73727063
)

// Operation types
type NcOp uint32

const (
	NcOpCall NcOp = iota
	NcOpWinUpdate
	NcOpMax
)

// Header used for CLIENT -> SERVER
type CncHdr struct {
	Magic uint32
	Op    NcOp
	Len   uint64
	Id    uint64
	Ts    uint64
}

// Header used for SERVER -> CLIENT
type SncHdr struct {
	Magic uint32
	Op    NcOp
	Len   uint64
	Id    uint64
	Ts    uint64
}
