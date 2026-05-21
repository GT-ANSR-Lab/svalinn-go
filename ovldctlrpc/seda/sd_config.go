package seda

// Seda paramaters
const (
	SedaAlpha   float64 = 0.7
	SedaTarget  uint64  = 80
	SedaTimeout uint64  = 1000
	SedaErrD    float64 = 0.0
	SedaErrI    float64 = -0.5
	SedaAdjI    float64 = 4.0
	SedaAdjD    float64 = 1.1
	SedaCi      float64 = -0.1
	SedaQdelayThresh      uint64 = 200
)

// Client config
const (
	CsdMaxClientDelayUs uint64 = 100
	CsdTbInitRate       uint64 = 4
	CsdTbMinRate        uint64 = 2
	CsdTbMaxToken       uint64 = 4
)
