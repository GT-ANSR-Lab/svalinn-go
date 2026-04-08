#ifndef __MEM_PMC_INTEL_H__
#define __MEM_PMC_INTEL_H__

#include <stdint.h>

#include "mem_pmc.h"

extern MemPmcOps mem_pmc_intel_ops;

typedef struct MemPmcIntelState {
    /* Channel counts */
    size_t m_num_mem_ch;
    size_t m_max_num_mem_ch;
} MemPmcIntelState;

void MemPmc_Intel_Init();
uint64_t MemPmc_Intel_GetMaxMemChan();
uint64_t MemPmc_Intel_GetActiveMemChan();
uint64_t MemPmc_Intel_GetMemChanAccesses(int chan);
uint64_t MemPmc_Intel_GetMemAccesses();
void MemPmc_Intel_DeInit();

#endif // __MEM_PMC_INTEL_H__
