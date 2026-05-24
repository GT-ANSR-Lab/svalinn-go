package pcc

import "math"

//
// Parameters used in Svalinn evaluation (on xl170)
//
// SpccQdelayBudget              uint64  = 1500
// SpccRttUs                     uint64  = 10
// SpccPreMonIntUs               uint64  = 500
// SpccMonIntUs                  uint64  = 1000
// SpccEpsilon                   uint64  = 10
// SpccMicroExpStrictLabelling   bool = false
// SpccMicroExpPerturbCp         bool = true
// SpccCalcUtilFn - tput
// SpccCompUtilFn - deadband (1%)


// Server config
const (
	SpccQdelayBudget              uint64  = 1500
	SpccRttUs                     uint64  = 10
  	SpccPreMonIntUs               uint64  = 500
  	SpccMonIntUs                  uint64  = 1000
  	SpccEpsilon                   uint64  = 10
    SpccMicroExpStrictLabelling   bool = false
    SpccMicroExpPerturbCp         bool = true
)

// Utility calculation function
func SpccCalcUtilFn(stats *SpccMicroExpStats) float64 {
	return float64(stats.OutResps) / float64(stats.Duration)
}

// Utility comparison function
func SpccCompUtilFn(minusStats, plusStats *SpccMicroExpStats) SpccDirType {

	var utilDiff float64
	var utilDiffPcnt float64

	utilDiff = math.Abs(plusStats.Utility - minusStats.Utility)
	utilDiffPcnt = utilDiff / minusStats.Utility

	if utilDiffPcnt < 0.01 {
		return SpccDirStay
	}
	if plusStats.Utility > minusStats.Utility {
		return SpccDirPlus
	}
	return SpccDirMinus
}

// Client config
const (
	CpccMaxClientDelayUs uint64 = 10
)
