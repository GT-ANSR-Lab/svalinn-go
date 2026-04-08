package protego

// Server config
const (
	SpgLatencyBudget    uint64  = 5350
	SpgCmSlopeThresh    float64 = 0.2
	SpgCmUpdateInterval uint64  = 200
	SpgCmP99Rtt         uint64  = 100
	SpgRttUs            uint64  = 40
	SpgAI               float64 = 0.001
	SpgMD               float64 = 0.02
)

// Client config
const (
	CpgMaxClientDelayUs uint64 = 40
)
