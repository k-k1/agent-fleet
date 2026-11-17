// Package usagehist is the fixed-bucket histogram behind the per-member usage history (ADR 0139
// decision 3). The Control Plane keeps no raw minutes, yet it has to answer "p95 over a day"
// for a day that arrives in several pulls (a tick, a Stop, the next Start). A quantile cannot
// be merged, so the day row stores bucket counts instead, which add.
//
// The cost is resolution: a quantile is read back by interpolating inside the bucket it falls
// in, so it is exact to the bucket's width and no better. The edges are chosen so a bucket is
// at most about 8% wide where the figure is compared with a ladder rung (memory) and a few
// percent of a core where it is compared with a burst baseline (CPU).
package usagehist

import (
	"encoding/json"
	"math"
	"sort"
)

// Spec names a set of ascending upper edges. A value v lands in the first bucket whose edge is
// above it (bucket i covers [edges[i-1], edges[i]), the first from 0); a value at or above the
// last edge lands in one extra overflow bucket, which a quantile reports as the last edge (a
// lower bound, never an invention).
type Spec struct {
	edges []float64
}

// Buckets is the number of counters a histogram of this spec has.
func (s Spec) Buckets() int { return len(s.edges) + 1 }

func geometric(start, ratio float64, n int) []float64 {
	out := make([]float64, n)
	v := start
	for i := range out {
		out[i] = v
		v *= ratio
	}
	return out
}

var (
	// CPU is percent of ONE core, as the Agent reports it.
	CPU = Spec{edges: []float64{
		1, 2, 3, 5, 7.5, 10, 15, 20, 25, 30, 40, 50, 60, 70, 80, 90, 100, 125, 150, 175, 200,
		250, 300, 400, 500, 600, 800, 1000, 1600, 3200, 6400,
	}}
	// WorkingSet is bytes: 64 MiB up to about 250 GiB in 8% steps.
	WorkingSet = Spec{edges: geometric(64<<20, 1.08, 109)}
	// Refault is file refaults per second.
	Refault = Spec{edges: []float64{
		0.1, 0.25, 0.5, 1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 50000,
	}}
)

// Hist is one histogram's counters. The zero value is an empty histogram of any spec.
type Hist []int

// Add counts one value. A NaN or negative value is ignored: the callers pass measured figures,
// and a nonsense one must not move a quantile.
func (h *Hist) Add(s Spec, v float64) {
	if math.IsNaN(v) || v < 0 {
		return
	}
	i := sort.Search(len(s.edges), func(i int) bool { return v < s.edges[i] })
	for len(*h) <= i {
		*h = append(*h, 0)
	}
	(*h)[i]++
}

// Total is the number of values counted.
func (h Hist) Total() int {
	n := 0
	for _, c := range h {
		n += c
	}
	return n
}

// Merge adds o into h. Both must be of the same spec.
func (h *Hist) Merge(o Hist) {
	for len(*h) < len(o) {
		*h = append(*h, 0)
	}
	for i, c := range o {
		(*h)[i] += c
	}
}

// Quantile reads q (0..1) back. ok is false for an empty histogram: there is no median of
// nothing, and the caller must show "not measured" and not a number.
func (h Hist) Quantile(s Spec, q float64) (float64, bool) {
	n := h.Total()
	if n == 0 {
		return 0, false
	}
	if q < 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	rank := int(math.Ceil(q * float64(n)))
	if rank < 1 {
		rank = 1
	}
	seen := 0
	for i, c := range h {
		if c == 0 {
			continue
		}
		if seen+c < rank {
			seen += c
			continue
		}
		if i >= len(s.edges) {
			return s.edges[len(s.edges)-1], true
		}
		lo := 0.0
		if i > 0 {
			lo = s.edges[i-1]
		}
		hi := s.edges[i]
		return lo + (hi-lo)*float64(rank-seen)/float64(c), true
	}
	return 0, false
}

// Encode is the stored form: a JSON array with trailing zeros dropped, "" for empty.
func (h Hist) Encode() string {
	end := len(h)
	for end > 0 && h[end-1] == 0 {
		end--
	}
	if end == 0 {
		return ""
	}
	b, _ := json.Marshal([]int(h[:end]))
	return string(b)
}

// Decode reads Encode's form. An unreadable value is an empty histogram: a damaged row loses
// its quantiles and keeps everything else, rather than failing the whole read.
func Decode(s string) Hist {
	if s == "" {
		return nil
	}
	var h []int
	if json.Unmarshal([]byte(s), &h) != nil {
		return nil
	}
	return Hist(h)
}
