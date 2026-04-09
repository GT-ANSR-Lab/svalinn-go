#define _GNU_SOURCE
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

#include "pow_pmc_amd_zen2.h"


static char *PMU_DEV_PATH = "/sys/bus/event_source/devices/power";

static PowPmcAmdZen2State *state = NULL;

// Helper: open a perf event by PMU type + config
static int OpenRaplEvent(uint32_t type, int cpu, uint64_t config) {
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

// Discover the PMU type for "power" subsystem
static int GetRaplPmuType(uint32_t *type) {
	char path[4097];
    snprintf(path, sizeof(path), "%s/type", PMU_DEV_PATH);

    FILE* f = fopen(path, "r");
    if (!f) {
        fprintf(stderr, "Failed to open %s: %s\n",
                path, strerror(errno));
        return -1;
    }

    *type = 0;
    if (fscanf(f, "%u", type) != 1) {
        fclose(f);
        return -1;
    }
    fclose(f);
    return 0;
}


// Discover the cpumask for "power" subsystem for the given numa node
static int GetRaplPmuCpu(uint32_t socket, int *cpu) {
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

// Discover the event config for a named event (e.g. "energy-pkg")
static int GetRaplEventConfig(const char* event_name, uint64_t *config) {
    char path[4097];
    snprintf(path, sizeof(path),
             "%s/events/%s", PMU_DEV_PATH, event_name);

    FILE* f = fopen(path, "r");
    if (!f) {
        fprintf(stderr, "Failed to open %s: %s\n",
                path, strerror(errno));
        return -1;
    }

    *config = 0;
    if (fscanf(f, "event=0x%lx", config) != 1) {
        fclose(f);
        return -1;
    }
    fclose(f);
	return 0;
}

// Read scale (Joules per raw unit)
static int GetRaplScale(const char* event_name, double *scale) {
    char path[4097];
    snprintf(path, sizeof(path),
             "%s/events/%s.scale", PMU_DEV_PATH, event_name);

    FILE* f = fopen(path, "r");
    if (!f) {
        fprintf(stderr, "Failed to open %s: %s\n",
                path, strerror(errno));
        return -1;
    }

    *scale = 1.0;
    if (fscanf(f, "%lf", scale) != 1) {
        fclose(f);
        return -1;

    }
    fclose(f);
	return 0;
}

void PowPmc_AmdZen2_Init() {

    // Get the calling cpu's numa node
    uint32_t cpu = sched_getcpu();
    uint32_t node = numa_node_of_cpu(cpu);

	/* Allocate the state */
    state = (PowPmcAmdZen2State *)malloc(sizeof(PowPmcAmdZen2State));
    assert(state);

	/* Get PMU type */
	if (GetRaplPmuType(&state->m_pmu_type)) {
		free(state);
		state = NULL;
		return;
	}

	/* Get the cpu for the current numa node */
	if (GetRaplPmuCpu(node, &state->m_pmu_cpu)) {
		free(state);
		state = NULL;
		return;
	}

	/* Get the config */
	if (GetRaplEventConfig("energy-pkg", &state->m_energy_pkg_config)) {
		free(state);
		state = NULL;
		return;
	}

	/* Get the scale */
	if (GetRaplScale("energy-pkg", &state->m_energy_pkg_scale)) {
		free(state);
		state = NULL;
		return;
	}

	/* Open the perf handle */
	state->m_energy_pkg_fd = OpenRaplEvent(state->m_pmu_type, state->m_pmu_cpu,
										   state->m_energy_pkg_config);
	if (state->m_energy_pkg_fd < 0) {
		fprintf(stderr, "Failed to open perf event (%s)\n",
				strerror(errno));
		free(state);
		state = NULL;
		return;
	}

	/* Enable the counters */
	ioctl(state->m_energy_pkg_fd, PERF_EVENT_IOC_RESET, 0);
	ioctl(state->m_energy_pkg_fd, PERF_EVENT_IOC_ENABLE, 0);
}

double PowPmc_AmdZen2_GetEnergyConsumed() {
    if (!state) {
        return 0.0;
    }

	uint64_t energy_pkg_raw = 0;

    ssize_t ret = read(state->m_energy_pkg_fd, &energy_pkg_raw, sizeof(energy_pkg_raw));
    if (ret != (ssize_t)sizeof(energy_pkg_raw)) {
        return 0.0;
    }

	return (double)energy_pkg_raw * state->m_energy_pkg_scale;
}

void PowPmc_AmdZen2_DeInit() {

    if (!state) {
        return;
    }

    /* Close the perf descriptors */
	close(state->m_energy_pkg_fd);

	free(state);
    state = NULL;
}

PowPmcOps pow_pmc_amd_zen2_ops = {
	.Init = PowPmc_AmdZen2_Init,
	.GetEnergyConsumed = PowPmc_AmdZen2_GetEnergyConsumed,
	.DeInit = PowPmc_AmdZen2_DeInit
};
