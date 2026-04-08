#ifndef __MEM_PMC_AMD_ZEN2_H__
#define __MEM_PMC_AMD_ZEN2_H__

#include <stdint.h>

#include "mem_pmc.h"


#define MAX_NUM_MEM_CH (8)

extern MemPmcOps mem_pmc_amd_zen2_ops;

typedef struct MemPmcAmdZen2State {
    /* PMU device info */
    long             m_pmu_type;
    int              m_pmu_cpu;
    /* Per-channel perf device file descriptor */
    int              m_chan_fd[MAX_NUM_MEM_CH];
    /* Channel counts */
    size_t           m_num_mem_ch;
    size_t           m_max_num_mem_ch;
} MemPmcAmdZen2State;

void MemPmc_AmdZen2_Init();
uint64_t MemPmc_AmdZen2_GetMaxMemChan();
uint64_t MemPmc_AmdZen2_GetActiveMemChan();
uint64_t MemPmc_AmdZen2_GetMemChanAccesses(int chan);
uint64_t MemPmc_AmdZen2_GetMemAccesses();
void MemPmc_AmdZen2_DeInit();


#endif // __MEM_PMC_AMD_ZEN2_H__
