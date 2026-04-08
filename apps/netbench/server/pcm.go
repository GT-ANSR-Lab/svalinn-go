package main

/*
#cgo CFLAGS: -msse2 -O3
#cgo LDFLAGS: /home/bpardeshi3/osdi_2026/netbench/deps/aspen/deps/pcm/build/src/libpcm.a -lstdc++ -lm

#ifdef __cplusplus
extern "C" {
#endif

#include <stdint.h>

// Declarations from your patched PCM library
uint32_t pcm_iok_get_cas_count(uint32_t channel);
uint32_t pcm_iok_get_active_channel_count(void);
int pcm_iok_init(int socket);

#ifdef __cplusplus
}
#endif
*/
import "C"

// GetCASCount returns the CAS count for the given memory channel.
func GetCASCount(channel uint32) uint32 {
	return uint32(C.pcm_iok_get_cas_count(C.uint(channel)))
}

// GetActiveChannelCount returns the number of active memory channels.
func GetActiveChannelCount() uint32 {
	return uint32(C.pcm_iok_get_active_channel_count())
}

// InitPCM initializes the PCM IOKernel interface for a given socket (0 or 1).
func InitPCM(socket int) int {
	return int(C.pcm_iok_init(C.int(socket)))
}