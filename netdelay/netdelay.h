/*
 * netdelay.c — eBPF-based TCP network stack delay measurement.
 *
 * Attaches kprobes to sock_def_readable (data queued into socket receive
 * buffer) and tcp_recvmsg (userspace reads data) to measure how long
 * data sits in the socket receive buffer before being consumed.
 *
 * The BPF programs are written as hand-coded instruction arrays to avoid
 * any external build dependency (clang/LLVM, libbpf, etc.).
 *
 * Public C API (called from Go via cgo):
 *   netdelay_init()                   — create maps, load BPF, attach kprobes
 *   netdelay_read_and_reset_max_delay — read the max delay and reset to 0
 *   netdelay_deinit()                 — detach probes and close fds
 */

#define _GNU_SOURCE
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <fcntl.h>
#include <errno.h>
#include <sys/syscall.h>
#include <sys/ioctl.h>
#include <linux/bpf.h>
#include <linux/perf_event.h>
#include <linux/version.h>

/* ================================================================
 * BPF instruction construction macros
 * ================================================================ */

#define INS(CODE, DST, SRC, OFF, IMM) \
    ((struct bpf_insn){.code=(CODE),.dst_reg=(DST),.src_reg=(SRC),.off=(OFF),.imm=(IMM)})

/* Load/store */
#define I_LDX_MEM(SZ,D,S,O)  INS(BPF_LDX|BPF_MEM|(SZ), D, S, O, 0)
#define I_STX_MEM(SZ,D,S,O)  INS(BPF_STX|BPF_MEM|(SZ), D, S, O, 0)
#define I_ST_MEM(SZ,D,O,IMM) INS(BPF_ST |BPF_MEM|(SZ), D, 0, O, IMM)

/* ALU64 */
#define I_MOV64_REG(D,S)     INS(BPF_ALU64|BPF_MOV|BPF_X, D, S, 0, 0)
#define I_MOV64_IMM(D,IMM)   INS(BPF_ALU64|BPF_MOV|BPF_K, D, 0, 0, IMM)
#define I_ADD64_IMM(D,IMM)   INS(BPF_ALU64|BPF_ADD|BPF_K, D, 0, 0, IMM)
#define I_SUB64_REG(D,S)     INS(BPF_ALU64|BPF_SUB|BPF_X, D, S, 0, 0)

/* Jumps */
#define I_JEQ_IMM(D,IMM,O)   INS(BPF_JMP|BPF_JEQ|BPF_K, D, 0, O, IMM)
#define I_JNE_IMM(D,IMM,O)   INS(BPF_JMP|BPF_JNE|BPF_K, D, 0, O, IMM)
#define I_JGE_REG(D,S,O)     INS(BPF_JMP|BPF_JGE|BPF_X, D, S, O, 0)
#define I_CALL(FN)            INS(BPF_JMP|BPF_CALL, 0, 0, 0, FN)
#define I_EXIT()              INS(BPF_JMP|BPF_EXIT, 0, 0, 0, 0)

/* 64-bit immediate load with pseudo map-fd source (two-insn sequence) */
#ifndef BPF_PSEUDO_MAP_FD
#define BPF_PSEUDO_MAP_FD 1
#endif
#define I_LD_MAP_FD(D,FD) \
    INS(BPF_LD|BPF_DW|BPF_IMM, D, BPF_PSEUDO_MAP_FD, 0, FD), \
    INS(0, 0, 0, 0, 0)

/* Register aliases */
#define R0  0
#define R1  1
#define R2  2
#define R3  3
#define R4  4
#define R6  6
#define R7  7
#define R8  8
#define R9  9
#define R10 10

/* x86_64 pt_regs offset for rdi (first function argument) */
#define PT_REGS_DI 112

/* ================================================================
 * BPF programs (hand-coded instruction arrays)
 * ================================================================ */

/*
 * Entry probe: kprobe/sock_def_readable
 *
 * Records ktime_get_ns() keyed by struct sock * when data is queued
 * into the socket receive buffer.  Uses BPF_NOEXIST so only the
 * *first* unread packet's timestamp is kept; later arrivals on the
 * same socket are ignored until userspace reads.
 *
 * Instruction layout (LD_MAP_FD occupies 2 slots):
 *
 *  [0]     r6 = *(u64*)(ctx + 112)        ; sock ptr from pt_regs->di
 *  [1]     *(u64*)(fp - 8) = r6           ; key on stack
 *  [2-3]   r1 = map_fd(sock_ts)           ; map fd (patched at runtime)
 *  [4]     r2 = fp
 *  [5]     r2 += -8                        ; &key
 *  [6]     call map_lookup_elem
 *  [7]     if r0 != 0 goto +10 → [18]     ; already tracked, skip
 *  [8]     call ktime_get_ns
 *  [9]     *(u64*)(fp - 16) = r0          ; timestamp value on stack
 *  [10-11] r1 = map_fd(sock_ts)           ; (patched)
 *  [12]    r2 = fp
 *  [13]    r2 += -8                        ; &key
 *  [14]    r3 = fp
 *  [15]    r3 += -16                       ; &value
 *  [16]    r4 = BPF_NOEXIST(1)
 *  [17]    call map_update_elem
 *  [18]    r0 = 0
 *  [19]    exit
 */
static struct bpf_insn entry_insns[] = {
    /* 0  */ I_LDX_MEM(BPF_DW, R6, R1, PT_REGS_DI),
    /* 1  */ I_STX_MEM(BPF_DW, R10, R6, -8),
    /* 2  */ I_LD_MAP_FD(R1, 0),  /* patched: sock_ts_map */
    /* 4  */ I_MOV64_REG(R2, R10),
    /* 5  */ I_ADD64_IMM(R2, -8),
    /* 6  */ I_CALL(BPF_FUNC_map_lookup_elem),
    /* 7  */ I_JNE_IMM(R0, 0, 10),            /* → [18] */
    /* 8  */ I_CALL(BPF_FUNC_ktime_get_ns),
    /* 9  */ I_STX_MEM(BPF_DW, R10, R0, -16),
    /* 10 */ I_LD_MAP_FD(R1, 0),  /* patched: sock_ts_map */
    /* 12 */ I_MOV64_REG(R2, R10),
    /* 13 */ I_ADD64_IMM(R2, -8),
    /* 14 */ I_MOV64_REG(R3, R10),
    /* 15 */ I_ADD64_IMM(R3, -16),
    /* 16 */ I_MOV64_IMM(R4, 1),              /* BPF_NOEXIST */
    /* 17 */ I_CALL(BPF_FUNC_map_update_elem),
    /* 18 */ I_MOV64_IMM(R0, 0),
    /* 19 */ I_EXIT(),
};
#define ENTRY_INSN_CNT (sizeof(entry_insns)/sizeof(entry_insns[0]))

/* Indices of the LD_MAP_FD instructions to patch in entry_insns */
#define ENTRY_PATCH_LOOKUP  2
#define ENTRY_PATCH_UPDATE 10

/*
 * Exit probe: kprobe/tcp_recvmsg
 *
 * Fires when userspace reads from the socket. Looks up the entry
 * timestamp for the socket, computes the socket buffer residence
 * delay, updates the max delay in delay_map, and deletes the entry.
 *
 *  [0]     r6 = *(u64*)(ctx + 112)        ; sock ptr
 *  [1]     *(u64*)(fp - 8) = r6           ; key
 *  [2-3]   r1 = map_fd(sock_ts)           ; (patched)
 *  [4]     r2 = fp
 *  [5]     r2 += -8
 *  [6]     call map_lookup_elem
 *  [7]     if r0 == 0 goto +14 → [22]     ; no entry, skip to delete
 *  [8]     r7 = *(u64*)(r0 + 0)           ; entry timestamp
 *  [9]     call ktime_get_ns              ; r0 = now
 *  [10]    r8 = r0                         ; r8 = now
 *  [11]    r8 -= r7                        ; r8 = delay
 *  [12]    *(u32*)(fp - 12) = 0           ; key=0 for delay_map
 *  [13-14] r1 = map_fd(delay)             ; (patched)
 *  [15]    r2 = fp
 *  [16]    r2 += -12
 *  [17]    call map_lookup_elem
 *  [18]    if r0 == 0 goto +3 → [22]      ; shouldn't happen
 *  [19]    r9 = *(u64*)(r0 + 0)           ; current max
 *  [20]    if r9 >= r8 goto +1 → [22]     ; current >= new, skip
 *  [21]    *(u64*)(r0 + 0) = r8           ; update max
 *  [22-23] r1 = map_fd(sock_ts)           ; (patched)
 *  [24]    r2 = fp
 *  [25]    r2 += -8
 *  [26]    call map_delete_elem
 *  [27]    r0 = 0
 *  [28]    exit
 */
static struct bpf_insn exit_insns[] = {
    /* 0  */ I_LDX_MEM(BPF_DW, R6, R1, PT_REGS_DI),
    /* 1  */ I_STX_MEM(BPF_DW, R10, R6, -8),
    /* 2  */ I_LD_MAP_FD(R1, 0),  /* patched: sock_ts_map */
    /* 4  */ I_MOV64_REG(R2, R10),
    /* 5  */ I_ADD64_IMM(R2, -8),
    /* 6  */ I_CALL(BPF_FUNC_map_lookup_elem),
    /* 7  */ I_JEQ_IMM(R0, 0, 14),            /* → [22] */
    /* 8  */ I_LDX_MEM(BPF_DW, R7, R0, 0),
    /* 9  */ I_CALL(BPF_FUNC_ktime_get_ns),
    /* 10 */ I_MOV64_REG(R8, R0),
    /* 11 */ I_SUB64_REG(R8, R7),
    /* 12 */ I_ST_MEM(BPF_W, R10, -12, 0),
    /* 13 */ I_LD_MAP_FD(R1, 0),  /* patched: delay_map */
    /* 15 */ I_MOV64_REG(R2, R10),
    /* 16 */ I_ADD64_IMM(R2, -12),
    /* 17 */ I_CALL(BPF_FUNC_map_lookup_elem),
    /* 18 */ I_JEQ_IMM(R0, 0, 3),             /* → [22] */
    /* 19 */ I_LDX_MEM(BPF_DW, R9, R0, 0),
    /* 20 */ I_JGE_REG(R9, R8, 1),            /* → [22] */
    /* 21 */ I_STX_MEM(BPF_DW, R0, R8, 0),
    /* 22 */ I_LD_MAP_FD(R1, 0),  /* patched: sock_ts_map */
    /* 24 */ I_MOV64_REG(R2, R10),
    /* 25 */ I_ADD64_IMM(R2, -8),
    /* 26 */ I_CALL(BPF_FUNC_map_delete_elem),
    /* 27 */ I_MOV64_IMM(R0, 0),
    /* 28 */ I_EXIT(),
};
#define EXIT_INSN_CNT (sizeof(exit_insns)/sizeof(exit_insns[0]))

/* Indices of LD_MAP_FD instructions to patch in exit_insns */
#define EXIT_PATCH_TS_LOOKUP   2
#define EXIT_PATCH_DELAY_MAP  13
#define EXIT_PATCH_TS_DELETE  22

/* ================================================================
 * Global state
 * ================================================================ */

#define MAX_CPUS 1024

static int g_sock_ts_map_fd = -1;
static int g_delay_map_fd   = -1;
static int g_entry_prog_fd  = -1;
static int g_exit_prog_fd   = -1;

static int g_entry_event_fds[MAX_CPUS];
static int g_exit_event_fds[MAX_CPUS];
static int g_num_cpus = 0;
static int g_initialized = 0;

/* ================================================================
 * Low-level helpers
 * ================================================================ */

static inline int sys_bpf(int cmd, union bpf_attr *attr, unsigned int size) {
    return (int)syscall(__NR_bpf, cmd, attr, size);
}

static inline int sys_perf_event_open(struct perf_event_attr *attr,
                                      pid_t pid, int cpu, int group_fd,
                                      unsigned long flags) {
    return (int)syscall(__NR_perf_event_open, attr, pid, cpu, group_fd, flags);
}

static int create_map(enum bpf_map_type type, int key_sz, int val_sz, int max_ent) {
    union bpf_attr attr;
    memset(&attr, 0, sizeof(attr));
    attr.map_type    = type;
    attr.key_size    = key_sz;
    attr.value_size  = val_sz;
    attr.max_entries = max_ent;
    return sys_bpf(BPF_MAP_CREATE, &attr, sizeof(attr));
}

static int load_prog(struct bpf_insn *insns, int insn_cnt) {
    char log_buf[16384];
    union bpf_attr attr;
    memset(&attr, 0, sizeof(attr));
    attr.prog_type    = BPF_PROG_TYPE_KPROBE;
    attr.insns        = (uint64_t)(unsigned long)insns;
    attr.insn_cnt     = insn_cnt;
    attr.license      = (uint64_t)(unsigned long)"GPL";
    attr.kern_version = LINUX_VERSION_CODE;
    attr.log_buf      = (uint64_t)(unsigned long)log_buf;
    attr.log_size     = sizeof(log_buf);
    attr.log_level    = 1;
    int fd = sys_bpf(BPF_PROG_LOAD, &attr, sizeof(attr));
    if (fd < 0) {
        fprintf(stderr, "[netdelay] BPF_PROG_LOAD failed: %s\nverifier log:\n%s\n",
                strerror(errno), log_buf);
    }
    return fd;
}

static int map_lookup(int fd, const void *key, void *val) {
    union bpf_attr attr;
    memset(&attr, 0, sizeof(attr));
    attr.map_fd = fd;
    attr.key    = (uint64_t)(unsigned long)key;
    attr.value  = (uint64_t)(unsigned long)val;
    return sys_bpf(BPF_MAP_LOOKUP_ELEM, &attr, sizeof(attr));
}

static int map_update(int fd, const void *key, const void *val, uint64_t flags) {
    union bpf_attr attr;
    memset(&attr, 0, sizeof(attr));
    attr.map_fd = fd;
    attr.key    = (uint64_t)(unsigned long)key;
    attr.value  = (uint64_t)(unsigned long)val;
    attr.flags  = flags;
    return sys_bpf(BPF_MAP_UPDATE_ELEM, &attr, sizeof(attr));
}

/* Patch the imm field of an LD_MAP_FD instruction pair. */
static void patch_map_fd(struct bpf_insn *insns, int idx, int fd) {
    insns[idx].imm = fd;
}

/* ================================================================
 * Tracefs kprobe management
 * ================================================================ */

static const char *g_tracefs = NULL;

static const char *find_tracefs(void) {
    if (access("/sys/kernel/tracing/kprobe_events", W_OK) == 0)
        return "/sys/kernel/tracing";
    if (access("/sys/kernel/debug/tracing/kprobe_events", W_OK) == 0)
        return "/sys/kernel/debug/tracing";
    return NULL;
}

/* Write a line to the kprobe_events file (append mode). */
static int write_kprobe_event(const char *line) {
    char path[256];
    snprintf(path, sizeof(path), "%s/kprobe_events", g_tracefs);
    int fd = open(path, O_WRONLY | O_APPEND);
    if (fd < 0) return -1;
    int ret = (write(fd, line, strlen(line)) > 0) ? 0 : -1;
    close(fd);
    return ret;
}

/* Read the integer event ID for a kprobe from tracefs. */
static int read_event_id(const char *probe_name) {
    char path[256];
    snprintf(path, sizeof(path), "%s/events/kprobes/%s/id", g_tracefs, probe_name);
    int fd = open(path, O_RDONLY);
    if (fd < 0) return -1;
    char buf[32];
    int n = read(fd, buf, sizeof(buf) - 1);
    close(fd);
    if (n <= 0) return -1;
    buf[n] = '\0';
    return atoi(buf);
}

/*
 * Create a kprobe via tracefs, open perf events on all CPUs, and attach
 * the given BPF program.  Returns 0 on success, -1 on failure.
 * event_fds[] is populated with the per-CPU perf event file descriptors.
 */
static int attach_kprobe(const char *probe_name, const char *func_name,
                         int prog_fd, int *event_fds) {
    char line[128];

    /* Remove stale probe (ignore errors). */
    snprintf(line, sizeof(line), "-:kprobes/%s\n", probe_name);
    write_kprobe_event(line);

    /* Create kprobe. */
    snprintf(line, sizeof(line), "p:kprobes/%s %s\n", probe_name, func_name);
    if (write_kprobe_event(line) < 0) {
        fprintf(stderr, "[netdelay] failed to create kprobe %s: %s\n",
                probe_name, strerror(errno));
        return -1;
    }

    /* Get event ID. */
    int eid = read_event_id(probe_name);
    if (eid < 0) {
        fprintf(stderr, "[netdelay] failed to read event id for %s\n", probe_name);
        return -1;
    }

    /* Open a perf event on each CPU and attach the BPF program. */
    for (int cpu = 0; cpu < g_num_cpus; cpu++) {
        struct perf_event_attr pe;
        memset(&pe, 0, sizeof(pe));
        pe.type          = PERF_TYPE_TRACEPOINT;
        pe.size          = sizeof(pe);
        pe.config        = eid;
        pe.sample_period = 1;
        pe.wakeup_events = 1;

        int efd = sys_perf_event_open(&pe, -1 /* all pids */, cpu,
                                      -1 /* no group */, 0);
        if (efd < 0) {
            fprintf(stderr, "[netdelay] perf_event_open cpu=%d: %s\n",
                    cpu, strerror(errno));
            event_fds[cpu] = -1;
            continue;
        }
        if (ioctl(efd, PERF_EVENT_IOC_SET_BPF, prog_fd) < 0) {
            fprintf(stderr, "[netdelay] SET_BPF cpu=%d: %s\n",
                    cpu, strerror(errno));
            close(efd);
            event_fds[cpu] = -1;
            continue;
        }
        ioctl(efd, PERF_EVENT_IOC_ENABLE, 0);
        event_fds[cpu] = efd;
    }
    return 0;
}

static void detach_kprobe(const char *probe_name, int *event_fds) {
    for (int i = 0; i < g_num_cpus; i++) {
        if (event_fds[i] >= 0) {
            ioctl(event_fds[i], PERF_EVENT_IOC_DISABLE, 0);
            close(event_fds[i]);
            event_fds[i] = -1;
        }
    }
    char line[128];
    snprintf(line, sizeof(line), "-:kprobes/%s\n", probe_name);
    write_kprobe_event(line);
}

/* ================================================================
 * Public API
 * ================================================================ */

int netdelay_init(void) {
    if (g_initialized) return 0;

    /* Detect tracefs mount point. */
    g_tracefs = find_tracefs();
    if (!g_tracefs) {
        fprintf(stderr, "[netdelay] tracefs not found, netdelay disabled\n");
        return -1;
    }

    /* Number of online CPUs. */
    g_num_cpus = (int)sysconf(_SC_NPROCESSORS_ONLN);
    if (g_num_cpus <= 0) g_num_cpus = 1;
    if (g_num_cpus > MAX_CPUS) g_num_cpus = MAX_CPUS;

    /* Initialize event fd arrays. */
    for (int i = 0; i < MAX_CPUS; i++) {
        g_entry_event_fds[i] = -1;
        g_exit_event_fds[i]  = -1;
    }

    /* 1. Create BPF maps. */
    g_sock_ts_map_fd = create_map(BPF_MAP_TYPE_HASH, 8, 8, 65536);
    if (g_sock_ts_map_fd < 0) {
        fprintf(stderr, "[netdelay] create sock_ts map: %s\n", strerror(errno));
        return -1;
    }
    g_delay_map_fd = create_map(BPF_MAP_TYPE_ARRAY, 4, 8, 1);
    if (g_delay_map_fd < 0) {
        fprintf(stderr, "[netdelay] create delay map: %s\n", strerror(errno));
        return -1;
    }

    /* 2. Patch map fds into the BPF programs. */
    patch_map_fd(entry_insns, ENTRY_PATCH_LOOKUP, g_sock_ts_map_fd);
    patch_map_fd(entry_insns, ENTRY_PATCH_UPDATE, g_sock_ts_map_fd);

    patch_map_fd(exit_insns, EXIT_PATCH_TS_LOOKUP,  g_sock_ts_map_fd);
    patch_map_fd(exit_insns, EXIT_PATCH_DELAY_MAP,  g_delay_map_fd);
    patch_map_fd(exit_insns, EXIT_PATCH_TS_DELETE,   g_sock_ts_map_fd);

    /* 3. Load BPF programs. */
    g_entry_prog_fd = load_prog(entry_insns, ENTRY_INSN_CNT);
    if (g_entry_prog_fd < 0) return -1;

    g_exit_prog_fd = load_prog(exit_insns, EXIT_INSN_CNT);
    if (g_exit_prog_fd < 0) return -1;

    /* 4. Attach kprobes. */
    if (attach_kprobe("nd_entry", "sock_def_readable",
                      g_entry_prog_fd, g_entry_event_fds) < 0)
        return -1;
    if (attach_kprobe("nd_exit", "tcp_recvmsg",
                      g_exit_prog_fd, g_exit_event_fds) < 0)
        return -1;

    g_initialized = 1;
    fprintf(stderr, "[netdelay] initialized (%d CPUs)\n", g_num_cpus);
    return 0;
}

uint64_t netdelay_read_and_reset_max_delay(void) {
    if (!g_initialized || g_delay_map_fd < 0) return 0;

    uint32_t key = 0;
    uint64_t val = 0;

    if (map_lookup(g_delay_map_fd, &key, &val) != 0)
        return 0;

    /* Reset to zero.  There is a tiny race with concurrent BPF updates;
     * at worst we lose one sample — acceptable for an estimate. */
    uint64_t zero = 0;
    map_update(g_delay_map_fd, &key, &zero, 0 /* BPF_ANY */);

    return val;
}

void netdelay_deinit(void) {
    if (!g_initialized) return;

    detach_kprobe("nd_entry", g_entry_event_fds);
    detach_kprobe("nd_exit",  g_exit_event_fds);

    if (g_entry_prog_fd >= 0) { close(g_entry_prog_fd); g_entry_prog_fd = -1; }
    if (g_exit_prog_fd  >= 0) { close(g_exit_prog_fd);  g_exit_prog_fd  = -1; }
    if (g_sock_ts_map_fd >= 0) { close(g_sock_ts_map_fd); g_sock_ts_map_fd = -1; }
    if (g_delay_map_fd   >= 0) { close(g_delay_map_fd);   g_delay_map_fd   = -1; }

    g_initialized = 0;
    fprintf(stderr, "[netdelay] deinitialized\n");
}
