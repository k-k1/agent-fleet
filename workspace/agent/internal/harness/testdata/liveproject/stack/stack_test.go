package stack

import "testing"

func TestPushPop(t *testing.T) {
	var s Stack
	s.Push(1)
	s.Push(2)
	v, ok := s.Pop()
	if !ok || v != 2 {
		t.Errorf("Pop() = %d, %v, want 2, true", v, ok)
	}
}

func TestPopEmpty(t *testing.T) {
	var s Stack
	_, ok := s.Pop()
	if ok {
		t.Errorf("Pop() on empty stack should return ok=false")
	}
}
