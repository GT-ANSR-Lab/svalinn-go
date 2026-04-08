package breakwater

// Server config
const (
	SbwDelayTarget uint64  = 2400
	SbwDropThresh  uint64  = 4800
	SbwRttUs       uint64  = 40
	SbwAI          float64 = 0.001
	SbwMD          float64 = 0.02
)

// Client config
const (
	CbwMaxClientDelayUs uint64 = 40
)
