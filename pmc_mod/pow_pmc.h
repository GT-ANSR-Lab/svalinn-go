#ifndef __POW_PMC_H__
#define __POW_PMC_H__

#include <stdint.h>

/* An abstract interface to be implemented for every microarchitecture */
typedef struct PowPmcOps {
    void     (*Init)();
	double   (*GetEnergyConsumed)();
    void     (*DeInit)();
} PowPmcOps;


/* API used by the programs to get the power/energy counter values */
void PowPmc_Init();
double PowPmc_GetEnergyConsumed();
void PowPmc_DeInit();

#endif  // __POW_PMC_H__
