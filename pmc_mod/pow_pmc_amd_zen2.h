#ifndef __POW_PMC_AMD_ZEN2_H__
#define __POW_PMC_AMD_ZEN2_H__

#include <stdint.h>

#include "pow_pmc.h"

extern PowPmcOps pow_pmc_amd_zen2_ops;

typedef struct PowPmcAmdZen2State {
    /* PMU device info */
    uint32_t         m_pmu_type;
    int              m_pmu_cpu;
	uint64_t         m_energy_pkg_config;
	double           m_energy_pkg_scale;

	/* Perf event handle */
	int              m_energy_pkg_fd;
} PowPmcAmdZen2State;

void PowPmc_AmdZen2_Init();
double PowPmc_AmdZen2_GetEnergyConsumed();
void PowPmc_AmdZen2_DeInit();


#endif // __POW_PMC_AMD_ZEN2_H__
