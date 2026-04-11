package pcc

import "math"

// Server config
const (
	SpccQdelayBudget              uint64  = 250
	SpccRttUs                     uint64  = 10
  	SpccPreMonIntUs               uint64  = 200
  	SpccMonIntUs                  uint64  = 500
  	SpccEpsilon                   uint64  = 1
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

	if utilDiffPcnt < 0.05 {
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
