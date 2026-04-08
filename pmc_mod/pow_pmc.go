package pmc

/*
#include "pow_pmc.h"
*/
import "C"

// Thin cgo wrappers over the power PMC C API. These are package-private;
// the public, cached API lives in pmc.go.

func cPowPmcInit() {
	C.PowPmc_Init()
}

func cPowPmcGetEnergyConsumed() float64 {
	return float64(C.PowPmc_GetEnergyConsumed())
}

func cPowPmcDeInit() {
	C.PowPmc_DeInit()
}
