package pcc

// Server config
const (
	SpccQdelayBudget              uint64  = 55
	SpccRttUs                     uint64  = 10
  	SpccPreMonIntUs               uint64  = 10
  	SpccMonIntUs                  uint64  = 10
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
	if plusStats.Utility > minusStats.Utility {
		return SpccDirPlus
	}
	return SpccDirMinus
}

// Client config
const (
	CpccMaxClientDelayUs uint64 = 10
)
