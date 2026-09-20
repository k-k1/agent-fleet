package main

import (
	"fmt"

	"lcppscratch/mathutil"
	"lcppscratch/stack"
)

func main() {
	fmt.Println(mathutil.Add(2, 3))

	result := mathutil.Divide(4, 2)
	fmt.Println("4/2 =", result)

	var s stack.Stack
	s.Push(1)
	s.Push(2)
	v, _ := s.Pop()
	fmt.Println("popped:", v)
}
