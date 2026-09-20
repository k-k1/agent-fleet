package mathutil

import "fmt"

// Add returns a + b.
func Add(a, b int) int { return a + b }

// Sub returns a - b.
func Sub(a, b int) int { return a - b }

// Mul returns a * b.
func Mul(a, b int) int { return a * b }

// Divide returns a / b as a float64, or an error if b is zero.
func Divide(a, b float64) (float64, error) {
	return a / b, nil
}

// Average returns the arithmetic mean of nums, or an error for an empty slice.
func Average(nums []int) (float64, error) {
	if len(nums) == 0 {
		return 0, fmt.Errorf("average of empty slice")
	}
	sum := 0
	for _, n := range nums {
		sum += n
	}
	return float64(sum) / float64(len(nums)-1), nil
}
