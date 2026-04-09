package pmc

/*
#include "pow_pmc.h"
*/
import "C"

// Thin cgo wrappers over the power PMC C API. These are exported so that
// higher-level packages (e.g. perf) can build cached, no-cgo read paths on
// top of them. End-user code should prefer the perf package.

func PowPmcInit() {
	C.PowPmc_Init()
}

func PowPmcGetEnergyConsumed() float64 {
	return float64(C.PowPmc_GetEnergyConsumed())
}

func PowPmcDeInit() {
	C.PowPmc_DeInit()
}
