package pmc

/*
#include <stdint.h>
#include "mem_pmc.h"
*/
import "C"

// Thin cgo wrappers over the memory PMC C API. These are package-private;
// the public, cached API lives in pmc.go.

func cMemPmcInit() {
	C.MemPmc_Init()
}

func cMemPmcGetMaxMemChan() uint64 {
	return uint64(C.MemPmc_GetMaxMemChan())
}

func cMemPmcGetActiveMemChan() uint64 {
	return uint64(C.MemPmc_GetActiveMemChan())
}

func cMemPmcGetMemChanAccesses(chann int) uint64 {
	return uint64(C.MemPmc_GetMemChanAccesses(C.int(chann)))
}

func cMemPmcGetMemAccesses() uint64 {
	return uint64(C.MemPmc_GetMemAccesses())
}

func cMemPmcDeInit() {
	C.MemPmc_DeInit()
}
