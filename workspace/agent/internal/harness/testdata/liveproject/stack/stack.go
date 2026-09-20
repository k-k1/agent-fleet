package stack

// Stack is a simple LIFO stack of ints.
type Stack struct {
	items []int
}

// Push adds v to the top of the stack.
func (s *Stack) Push(v int) {
	s.items = append(s.items, v)
}

// Pop removes and returns the top item. ok is false if the stack is empty.
func (s *Stack) Pop() (v int, ok bool) {
	v = s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return v, true
}

// Len returns the number of items on the stack.
func (s *Stack) Len() int {
	return len(s.items)
}
