#pragma once

#include <stdint.h>

/* Declarations of relevant functions in patched PCM library */
extern uint64_t pcm_c_get_cas_count(uint32_t channel);
extern uint64_t pcm_c_get_max_channel_count(void);
extern uint64_t pcm_c_get_active_channel_count(void);
extern int pcm_c_init(int socket);
