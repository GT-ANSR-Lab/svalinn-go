package utils

import (
	"cmp"
)

// Get the maximum
func Max[T cmp.Ordered](v1 T, v2 T) T {
	if v1 > v2 {
		return v1
	}
	return v2
}

// Get the minimum
func Min[T cmp.Ordered](v1 T, v2 T) T {
	if v1 < v2 {
		return v1
	}
	return v2
}

// Get the absolute value
func Abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
