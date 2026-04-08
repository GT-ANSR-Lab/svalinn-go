#include <assert.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <stdio.h>
#include <numa.h>
#include <sched.h>
#include <unistd.h>
#include <fcntl.h>
#include <sys/ioctl.h>
#include <sys/syscall.h>
#include <linux/perf_event.h>


#include "mem_pmc_amd_zen2.h"


/* AMD Zen2-specific constants */
static char *PMU_DEV_PATH = "/sys/bus/event_source/devices/amd_df";
static uint64_t CHAN_CTRL_PERF_EVENT_CONFIGS[MAX_NUM_MEM_CH] = {
    0x3807ULL,
    0x3847ULL,
    0x3887ULL,
    0x38c7ULL,
    0x100003807ULL,
    0x100003847ULL,
    0x100003887ULL,
    0x1000038c7ULL,
};

static int CacheLineSize = 64;
static MemPmcAmdZen2State *state = NULL;


// Helper: open a perf event by PMU type + config
static int OpenPerfEvent(uint32_t type, int cpu, uint64_t config) {
    struct perf_event_attr attr = {};
    attr.size        = sizeof(attr);
    attr.type        = type;
    attr.config      = config;
    attr.disabled    = 1;
    attr.inherit     = 1;

    return syscall(__NR_perf_event_open, &attr,
                   -1,   // pid: -1 = system-wide
                    cpu,
                   -1,   // group_fd
                    0);  // flags
}

// Discover the PMU type for "amd_df" subsystem
static int GetAmdDfPmuType(uint32_t *type) {
	char path[4097];
    snprintf(path, sizeof(path), "%s/type", PMU_DEV_PATH);

    FILE* f = fopen(path, "r");
    if (!f) {
        fprintf(stderr, "Failed to open %s: %s\n",
                path, strerror(errno));
        return -1;
    }

    *type = 0;
    fscanf(f, "%u", type);
    fclose(f);
    return 0;
}


static int GetAmdDfPmuCpu(uint32_t socket, int *cpu) {
    char path[4097];
    snprintf(path, sizeof(path), "%s/cpumask", PMU_DEV_PATH);

    FILE* f = fopen(path, "r");
    if (!f) {
        fprintf(stderr, "Failed to open %s: %s\n",
                path, strerror(errno));
        return -1;
    }

    uint32_t current_socket = 0;
    int val;
    *cpu = -1;

    while (fscanf(f, "%d", &val) == 1) {
        if (current_socket == socket) {
            *cpu = val;
            break;
        }
        current_socket++;
        char sep;
        if (fscanf(f, "%c", &sep) != 1 || sep != ',')
            break;
    }

    fclose(f);

    if (*cpu < 0) {
        fprintf(stderr, "Socket %u not found in cpumask\n", socket);
		return -1;
	}

    return 0;
}

void MemPmc_AmdZen2_Init() {

    char path[4097];

    // Get the calling cpu's numa node
    uint32_t cpu = sched_getcpu();
    uint32_t node = numa_node_of_cpu(cpu);

    /* Allocate the state */
    state = (MemPmcAmdZen2State *)malloc(sizeof(MemPmcAmdZen2State));
    assert(state);

	/* Get PMU type */
	if (GetAmdDfPmuType(&state->m_pmu_type)) {
		free(state);
		state = NULL;
		return;
	}

	/* Get the cpu for the current numa node */
	if (GetAmdDfPmuCpu(node, &state->m_pmu_cpu)) {
		free(state);
		state = NULL;
		return;
	}

    // Open a perf handle for every memory channel
    state->m_max_num_mem_ch = MAX_NUM_MEM_CH;
    state->m_num_mem_ch = 0;
    for (uint32_t i = 0; i < state->m_max_num_mem_ch; ++i) {
        // Open the perf handle
        state->m_chan_fd[i] = OpenPerfEvent(state->m_pmu_type, state->m_pmu_cpu,
											CHAN_CTRL_PERF_EVENT_CONFIGS[i]);
        if (state->m_chan_fd[i] < 0) {
            fprintf(stderr, "Skipping channel %d (perf_event_open: %s)\n",
                    i, strerror(errno));
            continue;
        }
        state->m_num_mem_ch++;
    }

    // Enable the counters
    for (uint32_t i = 0; i < state->m_max_num_mem_ch; ++i) {
        if (state->m_chan_fd[i] < 0) {
            continue;
        }
        ioctl(state->m_chan_fd[i], PERF_EVENT_IOC_RESET, 0);
        ioctl(state->m_chan_fd[i], PERF_EVENT_IOC_ENABLE, 0);
    }
}


uint64_t MemPmc_AmdZen2_GetMaxMemChan() {
    if (!state) {
        return 0;
    }
    return state->m_max_num_mem_ch;
}


uint64_t MemPmc_AmdZen2_GetActiveMemChan() {
    if (!state) {
        return 0;
    }
    return state->m_num_mem_ch;
}


uint64_t MemPmc_AmdZen2_GetMemChanAccesses(int chan) {

    if (!state) {
        return 0;
    }

    uint64_t accesses = 0;

    // Verify channel number is valid
    if (chan < 0 || chan >= state->m_max_num_mem_ch) {
        return 0;
    }

    // Verify the channel is active
    if (state->m_chan_fd[chan] < 0) {
        return 0;
    }

    // Read the counters
    ssize_t ret = read(state->m_chan_fd[chan], &accesses, sizeof(accesses));
    if (ret != (ssize_t)sizeof(accesses)) {
        return 0;
    }

    return accesses * CacheLineSize;
}


uint64_t MemPmc_AmdZen2_GetMemAccesses() {

    if (!state) {
        return 0;
    }

    uint64_t total = 0;
    uint64_t accesses = 0;
    ssize_t ret;

    for (uint32_t i = 0; i < state->m_max_num_mem_ch; ++i) {
        // Verify the channel is active
        if (state->m_chan_fd[i] < 0) {
            continue;
        }
        // Read the counters
        ret = read(state->m_chan_fd[i], &accesses, sizeof(accesses));
        if (ret != (ssize_t)sizeof(accesses)) {
            continue;
        }
        total += accesses;
    }

    return total * CacheLineSize;
}


void MemPmc_AmdZen2_DeInit() {

    if (!state) {
        return;
    }

    /* Close the perf descriptors */
    for (uint32_t i = 0; i < state->m_max_num_mem_ch; ++i) {
        if (state->m_chan_fd[i] < 0) {
            continue;
        }
        close(state->m_chan_fd[i]);
    }

    free(state);
    state = NULL;
}

MemPmcOps mem_pmc_amd_zen2_ops = {
    .Init = MemPmc_AmdZen2_Init,
    .GetMaxMemChan = MemPmc_AmdZen2_GetMaxMemChan,
    .GetActiveMemChan = MemPmc_AmdZen2_GetActiveMemChan,
    .GetMemChanAccesses = MemPmc_AmdZen2_GetMemChanAccesses,
    .GetMemAccesses = MemPmc_AmdZen2_GetMemAccesses,
    .DeInit = MemPmc_AmdZen2_DeInit
};
