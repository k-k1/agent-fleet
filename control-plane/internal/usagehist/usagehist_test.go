package usagehist

import (
	"math"
	"testing"
)

func TestQuantileWithinABucket(t *testing.T) {
	var h Hist
	for i := 1; i <= 100; i++ {
		h.Add(CPU, float64(i)) // 1..100 percent of a core
	}
	p50, ok := h.Quantile(CPU, 0.5)
	if !ok || math.Abs(p50-50) > 10 {
		t.Fatalf("p50 = %v %v", p50, ok)
	}
	p95, _ := h.Quantile(CPU, 0.95)
	if p95 < 90 || p95 > 100 {
		t.Fatalf("p95 = %v", p95)
	}
}

func TestEmptyHasNoQuantile(t *testing.T) {
	var h Hist
	if _, ok := h.Quantile(CPU, 0.5); ok {
		t.Fatal("an empty histogram answered")
	}
	if Decode("").Total() != 0 || Decode("not json").Total() != 0 {
		t.Fatal("damaged / empty form is not an empty histogram")
	}
}

// Adding in two pulls and merging equals adding in one: the whole point of a bucketed day row.
func TestMergeEqualsOnePass(t *testing.T) {
	var all, a, b Hist
	for i := 0; i < 200; i++ {
		v := float64(i%37) * 1.7
		all.Add(CPU, v)
		if i < 120 {
			a.Add(CPU, v)
		} else {
			b.Add(CPU, v)
		}
	}
	a.Merge(Decode(b.Encode()))
	for _, q := range []float64{0.5, 0.95} {
		x, _ := all.Quantile(CPU, q)
		y, _ := a.Quantile(CPU, q)
		if x != y {
			t.Fatalf("q%v: %v != %v", q, x, y)
		}
	}
}

func TestOverflowReportsTheLastEdge(t *testing.T) {
	var h Hist
	h.Add(CPU, 1e9)
	if v, _ := h.Quantile(CPU, 0.95); v != 6400 {
		t.Fatalf("overflow quantile = %v", v)
	}
	h = nil
	h.Add(CPU, math.NaN())
	h.Add(CPU, -1)
	if h.Total() != 0 {
		t.Fatal("nonsense values were counted")
	}
}

func TestWorkingSetBucketsAreNarrow(t *testing.T) {
	var h Hist
	gib := float64(6 << 30)
	for i := 0; i < 50; i++ {
		h.Add(WorkingSet, gib)
	}
	v, _ := h.Quantile(WorkingSet, 0.95)
	if math.Abs(v-gib)/gib > 0.09 {
		t.Fatalf("6 GiB read back as %.0f", v)
	}
}
