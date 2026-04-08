package msemaphore

// memSemaphoreImpl is the interface every memory-semaphore algorithm
// implementation must satisfy. It mirrors the C++ MemSemaphoreImpl class.
type memSemaphoreImpl interface {
	TryWait() bool
	Wait()
	QueueDelayTsc() uint64
	QueueLength() uint64
	Post()
	GetCapacity() int
}
