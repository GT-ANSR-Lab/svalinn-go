package utils

import (
	"time"
)

var (
	epoch = time.Unix(0, 0)
)

// Get the current timestamp in microseconds. This is monotonic time.
func MicroTime() uint64 {
	return uint64(time.Since(epoch).Microseconds())
}

// Get the current timestamp in nanoseconds. This is monotonic time.
func NanoTime() uint64 {
	return uint64(time.Since(epoch).Nanoseconds())
}
