package utils

// Embedded doubly linked list abstraction

type ListNode[T any] struct {
	Next, Prev *ListNode[T]
	Value      *T
}

func (ln *ListNode[T]) Init(v *T) {
	ln.Next = nil
	ln.Prev = nil
	ln.Value = v
}

func (ln *ListNode[T]) Del() {
	ln.Next.Prev = ln.Prev
	ln.Prev.Next = ln.Next
	ln.Next = nil
	ln.Prev = nil
}

type ListHead[T any] struct {
	Node ListNode[T]
}

func (lh *ListHead[T]) Init() {
	lh.Node.Next = &lh.Node
	lh.Node.Prev = &lh.Node
	lh.Node.Value = nil
}

func (lh *ListHead[T]) Empty() bool {
	return lh.Node.Next == &lh.Node
}

func (lh *ListHead[T]) Add(ln *ListNode[T]) {
	ln.Next = lh.Node.Next
	ln.Prev = &lh.Node
	lh.Node.Next.Prev = ln
	lh.Node.Next = ln
}

func (lh *ListHead[T]) AddTail(ln *ListNode[T]) {
	ln.Next = &lh.Node
	ln.Prev = lh.Node.Prev
	lh.Node.Prev.Next = ln
	lh.Node.Prev = ln
}

func (lh *ListHead[T]) Del(ln *ListNode[T]) {
	ln.Next.Prev = ln.Prev
	ln.Prev.Next = ln.Next
	ln.Next = nil
	ln.Prev = nil
}

func (lh *ListHead[T]) Top() *T {
	if lh.Empty() {
		return nil
	}
	return lh.Node.Next.Value
}

func (lh *ListHead[T]) Tail() *T {
	if lh.Empty() {
		return nil
	}
	return lh.Node.Prev.Value
}

func (lh *ListHead[T]) Pop() *T {
	if lh.Empty() {
		return nil
	}
	ln := lh.Node.Next
	lh.Del(ln)
	return ln.Value
}

func (lh *ListHead[T]) ForEach(fn func(off int, obj *T)) {
	if lh.Empty() {
		return
	}
	i := 0
	node := lh.Node.Next
	for node != &lh.Node {
		fn(i, node.Value)
		i += 1
		node = node.Next
	}
}
