package netdelay

/*
#cgo CFLAGS: -O2
#include "netdelay.h"
*/
import "C"

// Init loads the eBPF programs and attaches kprobes to tcp_v4_do_rcv and
// tcp_recvmsg. Returns 0 on success, -1 on failure. On failure the
// netstack delay measurement is silently disabled (ReadAndResetMaxDelay
// returns 0).
func Init() int {
	return int(C.netdelay_init())
}

// ReadAndResetMaxDelay returns the maximum network stack delay (in
// nanoseconds) observed since the last call, then resets the counter to
// zero. Returns 0 if Init was not called or failed.
func ReadAndResetMaxDelay() uint64 {
	return uint64(C.netdelay_read_and_reset_max_delay())
}

// DeInit detaches the kprobes and frees all eBPF resources.
func DeInit() {
	C.netdelay_deinit()
}
