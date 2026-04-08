#ifndef __POW_PMC_INTEL_H__
#define __POW_PMC_INTEL_H__

#include <stdint.h>

#include "pow_pmc.h"

extern PowPmcOps pow_pmc_intel_ops;

typedef struct PowPmcIntelState {
    /* PMU device info */
    uint32_t         m_pmu_type;
    int              m_pmu_cpu;
	uint64_t         m_energy_pkg_config;
	double           m_energy_pkg_scale;
	uint64_t         m_energy_ram_config;
	double           m_energy_ram_scale;

	/* Perf event handle */
	int              m_energy_pkg_fd;
	int              m_energy_ram_fd;
} PowPmcIntelState;

void PowPmc_Intel_Init();
double PowPmc_Intel_GetEnergyConsumed();
void PowPmc_Intel_DeInit();


#endif // __POW_PMC_INTEL_H__
