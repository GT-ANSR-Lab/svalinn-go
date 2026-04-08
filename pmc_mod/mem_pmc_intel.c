#include <assert.h>
#include <stdlib.h>
#include <numa.h>
#include <sched.h>

#include "pcm_c.h"
#include "mem_pmc_intel.h"


static int CacheLineSize = 64;
static MemPmcIntelState *state = NULL;


void MemPmc_Intel_Init() {

    // Get the calling cpu's numa node
    uint32_t cpu = sched_getcpu();
    uint32_t node = numa_node_of_cpu(cpu);

    /* Init the pcm module */
    pcm_c_init(node);

    /* Allocate the state */
    state = (MemPmcIntelState *)malloc(sizeof(MemPmcIntelState));
    assert(state);

    /* Initialize the state */
    state->m_num_mem_ch = pcm_c_get_active_channel_count();
    state->m_max_num_mem_ch = pcm_c_get_max_channel_count();
}

uint64_t MemPmc_Intel_GetMaxMemChan() {
    if (!state) {
        return 0;
    }
    return state->m_max_num_mem_ch;
}

uint64_t MemPmc_Intel_GetActiveMemChan() {
    if (!state) {
        return 0;
    }
    return state->m_num_mem_ch;
}

uint64_t MemPmc_Intel_GetMemChanAccesses(int chan) {
    if (!state) {
        return 0;
    }
    return pcm_c_get_cas_count(chan) * CacheLineSize;
}

uint64_t MemPmc_Intel_GetMemAccesses() {
    if (!state) {
        return 0;
    }

    uint64_t total = 0;

    for (int i = 0; i < state->m_max_num_mem_ch; ++i) {
        total += pcm_c_get_cas_count(i);
    }

    return total * CacheLineSize;
}

void MemPmc_Intel_DeInit() {

    if (!state) {
        return;
    }

    pcm_c_deinit();

    free(state);
    state = NULL;
}


MemPmcOps mem_pmc_intel_ops = {
    .Init = MemPmc_Intel_Init,
    .GetMaxMemChan = MemPmc_Intel_GetMaxMemChan,
    .GetActiveMemChan = MemPmc_Intel_GetActiveMemChan,
    .GetMemChanAccesses = MemPmc_Intel_GetMemChanAccesses,
    .GetMemAccesses = MemPmc_Intel_GetMemAccesses,
    .DeInit = MemPmc_Intel_DeInit
};
