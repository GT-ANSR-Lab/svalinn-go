#ifndef __MEM_PMC_H__
#define __MEM_PMC_H__

#include <stdint.h>

/* An abstract interface to be implemented for every microarchitecture */
typedef struct MemPmcOps {
    void     (*Init)();
    uint64_t (*GetMaxMemChan)();
    uint64_t (*GetActiveMemChan)();
    uint64_t (*GetMemChanAccesses)(int chan);
    uint64_t (*GetMemAccesses)();
    void     (*DeInit)();
} MemPmcOps;


/* API used by the programs to get the memory performance counter values */
void MemPmc_Init();
uint64_t MemPmc_GetMaxMemChan();
uint64_t MemPmc_GetActiveMemChan();
uint64_t MemPmc_GetMemChanAccesses(int chan);
uint64_t MemPmc_GetMemAccesses();
void MemPmc_DeInit();

#endif  // __MEM_PMC_H__
