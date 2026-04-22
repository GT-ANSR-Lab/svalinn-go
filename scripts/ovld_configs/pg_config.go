package protego

// Server config
const (
	SpgLatencyBudget    uint64  = 2500
	SpgCmSlopeThresh    float64 = 0.2
	SpgCmSlopeInv       uint64  = 4
	SpgCmUpdateInterval uint64  = 200
	SpgCmP99Rtt         uint64  = 100
	SpgRttUs            uint64  = 10
	SpgAI               float64 = 0.001
	SpgMD               float64 = 0.02
)

// Client config
const (
	CpgMaxClientDelayUs uint64 = 10
)
