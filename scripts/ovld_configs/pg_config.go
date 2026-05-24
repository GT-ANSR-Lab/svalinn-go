package protego

//
// Parameters used in Svalinn evaluation (on xl170)
//
// SpgLatencyBudget    uint64  = 1500
// SpgCmSlopeThresh    float64 = 0.1
// SpgCmSlopeInv       uint64  = 10
// SpgCmUpdateInterval uint64  = 1000
// SpgCmP99Rtt         uint64  = 500


// Server config
const (
	SpgLatencyBudget    uint64  = 1500
	SpgCmSlopeThresh    float64 = 0.1
	SpgCmSlopeInv       uint64  = 10
	SpgCmUpdateInterval uint64  = 1000
	SpgCmP99Rtt         uint64  = 500
	SpgRttUs            uint64  = 10
	SpgAI               float64 = 0.001
	SpgMD               float64 = 0.02
)

// Client config
const (
	CpgMaxClientDelayUs uint64 = 10
)
