#include <assert.h>
#include <stdio.h>

#include "lock.h"
#include "cpu_id.h"
#include "mem_pmc.h"
#include "mem_pmc_intel.h"
#include "mem_pmc_amd_zen2.h"


/* Base class pointer of the memory PMC operations, pointing to
 * microarchitecture-specific memory PMC routines */
static MemPmcOps *ops = NULL;

/* Lock to ensure we only init the state once (singleton state) */
static DEFINE_SPINLOCK(ops_lock);


void MemPmc_Init() {
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
        ops = &mem_pmc_intel_ops;
    } else if (CpuIdInfo_IsAmd(&cpu_info)) {
        if (CpuIdInfo_IsAmdZen2(&cpu_info)) {
            ops = &mem_pmc_amd_zen2_ops;
        } else {
            printf("This AMD microarchitecture model is not supported by Memory PMC module\n");
            spin_unlock(&ops_lock);
            return;
        }
    } else {
        printf("%s microarchitecture vendor is not supported by Memory PMC module\n", cpu_info.m_vendor);
        spin_unlock(&ops_lock);
        return;
    }

    /* Invoke the microarchitecture-specific initialization */
    assert(ops);
    ops->Init();

    spin_unlock(&ops_lock);
}

uint64_t MemPmc_GetMaxMemChan() {
    if (!ops) {
        return 0;
    }
    return ops->GetMaxMemChan();
}

uint64_t MemPmc_GetActiveMemChan() {
    if (!ops) {
        return 0;
    }
    return ops->GetActiveMemChan();
}

uint64_t MemPmc_GetMemChanAccesses(int chan) {
    if (!ops) {
        return 0;
    }
    return ops->GetMemChanAccesses(chan);
}

uint64_t MemPmc_GetMemAccesses() {
    if (!ops) {
        return 0;
    }
    return ops->GetMemAccesses();
}

void MemPmc_DeInit() {

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
