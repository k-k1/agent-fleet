package afdb

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	idleCheckInterval = 60 * time.Second
	idleStopThreshold = 30 * time.Minute
)

// idleThreshold returns the idle stop threshold, overridable by AF_DB_IDLE_SECONDS.
func idleThreshold() time.Duration {
	if s := os.Getenv("AF_DB_IDLE_SECONDS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return idleStopThreshold
}

// StartIdleLoop starts the background goroutine that stops idle instances.
// Called once from main.go's serve path.
func StartIdleLoop() {
	go runIdleLoop()
}

func runIdleLoop() {
	idleSince := make(map[string]time.Time)
	for {
		// When AF_DB_IDLE_SECONDS is set (e.g. in tests), use the same value for
		// the check interval so the loop actually fires within the window.
		interval := idleCheckInterval
		if t := idleThreshold(); t < interval {
			interval = t
		}
		time.Sleep(interval)
		CheckAllInstances(idleSince)
	}
}

// CheckAllInstances checks all running instances for idleness and stops those
// that have been idle past the threshold. Exported so tests can call it directly.
func CheckAllInstances(idleSince map[string]time.Time) {
	var instances []*Instance
	_ = withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return nil
		}
		for _, inst := range r.Instances {
			if isInstanceRunning(inst) {
				cp := *inst
				instances = append(instances, &cp)
			}
		}
		return nil
	})

	threshold := idleThreshold()

	for _, inst := range instances {
		key := instanceKey(inst.Engine, inst.Major)
		n := CountClientBackends(inst)
		if n != 0 {
			delete(idleSince, key)
			continue
		}
		// Zero active connections — check how long it has been idle.
		var effectiveLastUsed time.Time
		_ = withLock(func() error {
			r, err := readRegistry()
			if err != nil {
				return nil
			}
			if i, ok := r.Instances[key]; ok {
				effectiveLastUsed = i.LastUsedAt
			}
			return nil
		})
		if time.Since(effectiveLastUsed) < threshold {
			delete(idleSince, key)
			continue
		}
		if _, seen := idleSince[key]; !seen {
			idleSince[key] = time.Now()
		}
		if time.Since(idleSince[key]) >= threshold {
			fmt.Fprintf(os.Stderr, "af-db: idle-stop %s-%s (no clients for %s)\n",
				inst.Engine, inst.Major, threshold)
			var stopErr error
			if inst.Engine == "mysql" {
				stopErr = stopMySQLServer(inst)
			} else {
				stopErr = stopServer(inst)
			}
			if stopErr != nil {
				fmt.Fprintf(os.Stderr, "af-db: idle-stop error: %v\n", stopErr)
				continue
			}
			_ = withLock(func() error {
				r, err := readRegistry()
				if err != nil {
					return nil
				}
				if i, ok := r.Instances[key]; ok {
					i.PID = 0
				}
				return writeRegistry(r)
			})
			delete(idleSince, key)
		}
	}
}
