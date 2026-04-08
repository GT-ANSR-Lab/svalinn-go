package msemaphore

import (
	"sync"

	"utils"
)

// waiter is an intrusive list node representing a goroutine parked on the
// semaphore. It embeds a utils.ListNode[waiter] so that enqueue/dequeue do
// not allocate. Waiters (and their channels) are recycled through a
// sync.Pool (see waiterPool).
type waiter struct {
	node       utils.ListNode[waiter]
	ch         chan struct{}
	enqueueTsc uint64
}

// waiterPool recycles *waiter (and the channel embedded in each one). After
// warmup, Get/Put never allocate on the hot path.
var waiterPool = sync.Pool{
	New: func() any {
		// Buffered with capacity 1: senders never block, so the releaser
		// can hand off the slot under the semaphore lock without waiting.
		return &waiter{ch: make(chan struct{}, 1)}
	},
}

func acquireWaiter() *waiter {
	return waiterPool.Get().(*waiter)
}

func releaseWaiter(w *waiter) {
	waiterPool.Put(w)
}

// waiterQueue is a thin wrapper around utils.ListHead[waiter] that also
// tracks the queue length so QueueLength is O(1).
type waiterQueue struct {
	list utils.ListHead[waiter]
	n    uint64
}

func (q *waiterQueue) init() {
	q.list.Init()
	q.n = 0
}

func (q *waiterQueue) empty() bool {
	return q.list.Empty()
}

func (q *waiterQueue) length() uint64 {
	return q.n
}

func (q *waiterQueue) pushBack(w *waiter) {
	w.node.Init(w)
	q.list.AddTail(&w.node)
	q.n++
}

func (q *waiterQueue) popFront() *waiter {
	w := q.list.Pop()
	if w == nil {
		return nil
	}
	q.n--
	return w
}
