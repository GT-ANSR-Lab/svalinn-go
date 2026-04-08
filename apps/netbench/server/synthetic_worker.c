#include <stdint.h>
#include <stdlib.h>
#include <numa.h>
#include <emmintrin.h>
#include <math.h>

#define CACHELINE_SIZE 64

/**
 * Common utility functions
 */

// Flush the cache line to memory
static inline void clflush(volatile void *p) {
    asm volatile("clflush (%0)" ::"r"(p));
}

// Store data (indicated by the param c) to the cache line using the
// non-temporal store.
static inline void nt_cacheline_store(char *p, int c) {
  __m128i i = _mm_set_epi8(c, c, c, c, c, c, c, c, c, c, c, c, c, c, c, c);
  _mm_stream_si128((__m128i *)&p[0], i);
  _mm_stream_si128((__m128i *)&p[16], i);
  _mm_stream_si128((__m128i *)&p[32], i);
  _mm_stream_si128((__m128i *)&p[48], i);
}

/**
 * Square root computation worker
 */
typedef struct SqrtWorker {
    char _dummy[CACHELINE_SIZE];
} SqrtWorker;

SqrtWorker *sqrt_worker_create() {
    SqrtWorker *w = malloc(sizeof(SqrtWorker));
    if (!w) {
        return NULL;
    }
    memset(w, 0, sizeof(SqrtWorker));
    return w;
}

void sqrt_worker_work(SqrtWorker *w, uint64_t n) {
    const double kNumber = 2350845.545;
    for (uint64_t i = 0; i < n; ++i) {
        volatile double v = sqrt(i * kNumber);
    }
}

void sqrt_worker_destroy(SqrtWorker *w) {
    if (!w) {
        return;
    }
    free(w);
}


/**
 * Memory bandwidth antagonist worker
 */

typedef struct MemBWAntagonistWorker {
    char *buf;
    size_t size;
    int nop_period;
    int nop_num;
} MemBWAntagonistWorker;

MemBWAntagonistWorker *membw_worker_create(size_t size, int nop_period, int nop_num) {
    char *buf = (char *)numa_alloc_local(size);
    if ((uintptr_t)buf % CACHELINE_SIZE != 0) {
        return NULL;
    }

    for (size_t i = 0; i < size; i += CACHELINE_SIZE) {
        clflush(buf + i);
    }

    MemBWAntagonistWorker *w = malloc(sizeof(MemBWAntagonistWorker));
    w->buf = buf;
    w->size = size;
    w->nop_period = nop_period;
    w->nop_num = nop_num;
    return w;
}

void membw_worker_work(MemBWAntagonistWorker *w, uint64_t n) {
    int cnt = 0;
    for (uint64_t k = 0; k < n; k++) {
        for (size_t i = 0; i < w->size; i += CACHELINE_SIZE) {
            nt_cacheline_store(w->buf + i, 0);
            if (cnt++ == w->nop_period) {
                cnt = 0;
                for (int j = 0; j < w->nop_num; j++) {
                    asm("");
                }
            }
        }
    }
}

void membw_worker_destroy(MemBWAntagonistWorker *w) {
    if (!w) {
        return;
    }
    numa_free(w->buf, w->size);
    free(w);
}
