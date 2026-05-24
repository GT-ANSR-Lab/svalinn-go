package seda

//
// Parameters used in Svalinn evaluation (on xl170)
//
// SedaTarget  uint64  = 2500
// SedaTimeout uint64  = 5000
// SedaAdjI    float64 = 2.0
// SedaAdjD    float64 = 1.3
// SedaQdelayThresh      uint64 = 1500

// Seda paramaters
const (
	SedaAlpha   float64 = 0.7
	SedaTarget  uint64  = 2500
	SedaTimeout uint64  = 5000
	SedaErrD    float64 = 0.0
	SedaErrI    float64 = -0.5
	SedaAdjI    float64 = 2.0
	SedaAdjD    float64 = 1.3
	SedaCi      float64 = -0.1
	SedaQdelayThresh      uint64 = 1500
)

// Client config
const (
	CsdMaxClientDelayUs uint64 = 100
	CsdTbInitRate       uint64 = 4
	CsdTbMinRate        uint64 = 2
	CsdTbMaxToken       uint64 = 4
)
