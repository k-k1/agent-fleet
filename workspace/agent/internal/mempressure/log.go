package mempressure

import "log"

func logBoundary(b Boundary) {
	if b.OK {
		log.Printf("mem-pressure: cgroup verified as this container's own; sampling every %s", Interval)
		return
	}
	log.Printf("mem-pressure: %s; the axis is reported unavailable", b.Reason)
}

func logMemMax(b Boundary) {
	if b.MemMax == "" {
		return
	}
	log.Printf("mem-pressure: memory.max agreement with the requested limit: %s (%s)", b.MemMax, b.MemMaxDetail)
}
