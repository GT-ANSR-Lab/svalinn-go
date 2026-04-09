package pmc

/*
#include <stdint.h>
#include "mem_pmc.h"
*/
import "C"

// Thin cgo wrappers over the memory PMC C API. These are exported so that
// higher-level packages (e.g. perf) can build cached, no-cgo read paths on
// top of them. End-user code should prefer the perf package.

func MemPmcInit() {
	C.MemPmc_Init()
}

func MemPmcGetMaxMemChan() uint64 {
	return uint64(C.MemPmc_GetMaxMemChan())
}

func MemPmcGetActiveMemChan() uint64 {
	return uint64(C.MemPmc_GetActiveMemChan())
}

func MemPmcGetMemChanAccesses(chann int) uint64 {
	return uint64(C.MemPmc_GetMemChanAccesses(C.int(chann)))
}

func MemPmcGetMemAccesses() uint64 {
	return uint64(C.MemPmc_GetMemAccesses())
}

func MemPmcDeInit() {
	C.MemPmc_DeInit()
}
