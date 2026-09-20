package mathutil

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Errorf("Add(2,3) = %d, want 5", got)
	}
}

func TestDivide(t *testing.T) {
	got, err := Divide(10, 2)
	if err != nil || got != 5 {
		t.Errorf("Divide(10,2) = %v, %v, want 5, nil", got, err)
	}
	if _, err := Divide(1, 0); err == nil {
		t.Errorf("Divide(1,0) should return an error, got nil")
	}
}

func TestAverage(t *testing.T) {
	got, err := Average([]int{2, 4, 6})
	if err != nil || got != 4 {
		t.Errorf("Average([2,4,6]) = %v, %v, want 4, nil", got, err)
	}
	if _, err := Average(nil); err == nil {
		t.Errorf("Average(nil) should return an error, got nil")
	}
}
