package breakwater

// Server config
const (
	SbwDelayTarget uint64  = 80
	SbwDropThresh  uint64  = 160
	SbwRttUs       uint64  = 10
	SbwAI          float64 = 0.001
	SbwMD          float64 = 0.02
)

// Client config
const (
	CbwMaxClientDelayUs uint64 = 10
)
