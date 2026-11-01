//go:build !race

package memoryx

// raceEnabled reports whether the test binary was built with -race. Allocation-ceiling
// assertions consult it because the race runtime inflates runtime.MemStats.TotalAlloc.
const raceEnabled = false
