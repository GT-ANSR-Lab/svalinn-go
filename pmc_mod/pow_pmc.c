#include <assert.h>
#include <stdio.h>

#include "lock.h"
#include "cpu_id.h"
#include "pow_pmc.h"
#include "pow_pmc_intel.h"
#include "pow_pmc_amd_zen2.h"


/* Base class pointer of the power PMC operations, pointing to
 * microarchitecture-specific power PMC routines */
static PowPmcOps *ops = NULL;

/* Lock to ensure we only init the state once (singleton state) */
static DEFINE_SPINLOCK(ops_lock);


void PowPmc_Init() {
    CpuIdInfo cpu_info;

    /* Ensure only one thread goes through */
    spin_lock(&ops_lock);

    /* Check if ops is already initialized */
    if (ops) {
        spin_unlock(&ops_lock);
        return;
    }

    /* Get the CPU microarchitecture info */
    CpuIdInfo_Get(&cpu_info);

    /* Choose the right ops */
    if (CpuIdInfo_IsIntel(&cpu_info)) {
        ops = &pow_pmc_intel_ops;
    } else if (CpuIdInfo_IsAmd(&cpu_info)) {
        if (CpuIdInfo_IsAmdZen2(&cpu_info)) {
            ops = &pow_pmc_amd_zen2_ops;
        } else {
            printf("This AMD microarchitecture model is not supported by Power PMC module\n");
            spin_unlock(&ops_lock);
            return;
        }
    } else {
        printf("%s microarchitecture vendor is not supported by Power PMC module\n", cpu_info.m_vendor);
        spin_unlock(&ops_lock);
        return;
    }

    /* Invoke the microarchitecture-specific initialization */
    assert(ops);
    ops->Init();

    spin_unlock(&ops_lock);
}

double PowPmc_GetEnergyConsumed() {
    if (!ops) {
        return 0.0;
    }
    return ops->GetEnergyConsumed();
}

void PowPmc_DeInit() {

    /* Ensure only one thread goes through */
    spin_lock(&ops_lock);

    /* Check if ops is already de-initialized */
    if (!ops) {
        spin_unlock(&ops_lock);
        return;
    }

    /* Call the microarchitecture-specific cleanup routine */
    ops->DeInit();
    ops = NULL;

    spin_unlock(&ops_lock);
}
