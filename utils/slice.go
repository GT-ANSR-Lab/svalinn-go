package utils

// Count the number of elements that satisfy the given predicate
func CountIf[T any](s *[]T, pred func(*T) bool) uint64 {
	count := uint64(0)
	for i := range *s {
		if pred(&(*s)[i]) {
			count++
		}
	}
	return count
}

// Remove the elements that satisfy the given predicate
// Returns the number of elements removed
func RemoveIf[T any](s *[]T, pred func(*T) bool) uint64 {
	count := uint64(0)
	n := (*s)[:0]
	for i := range *s {
		if !pred(&(*s)[i]) {
			n = append(n, (*s)[i])
		} else {
			count++
		}
	}
	*s = n
	return count
}

// Select the elements that satisfy the given predicate
// Returns the number of elements selected
func SelectIf[T any](s *[]T, pred func(*T) bool) uint64 {
	count := uint64(0)
	n := (*s)[:0]
	for i := range *s {
		if pred(&(*s)[i]) {
			n = append(n, (*s)[i])
			count++
		}
	}
	*s = n
	return count
}

// Append the elements from the source slice to destination slice
// that satisfy the given predicate
// Returns the number of elements copied
func CopyIf[T any](dst *[]T, src *[]T, pred func(*T) bool) uint64 {
	count := uint64(0)
	for i := range *src {
		if pred(&(*src)[i]) {
			*dst = append(*dst, (*src)[i])
			count++
		}
	}
	return count
}

// Accumulate the values in the slice and return the accumulated value
func Accumulate[T any, U any](s *[]T, init U, fn func(U, *T) U) U {
	acc := init
	for i := range *s {
		acc = fn(acc, &(*s)[i])
	}
	return acc
}
