package afdb

import (
	"fmt"
	"os"
	"time"
)

const (
	idleCheckInterval = 60 * time.Second
	idleStopThreshold = 30 * time.Minute
)

// StartIdleLoop starts the background goroutine that stops idle Postgres instances.
// Called once from main.go's serve path.
func StartIdleLoop() {
	go runIdleLoop()
}

func runIdleLoop() {
	// Track consecutive idle ticks per instance key.
	idleSince := make(map[string]time.Time)

	for range time.Tick(idleCheckInterval) {
		checkAllInstances(idleSince)
	}
}

func checkAllInstances(idleSince map[string]time.Time) {
	var instances []*Instance
	_ = withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return nil
		}
		for _, inst := range r.Instances {
			if inst.Engine == "postgres" && isRunning(inst.PID) {
				cp := *inst
				instances = append(instances, &cp)
			}
		}
		return nil
	})

	for _, inst := range instances {
		key := instanceKey(inst.Engine, inst.Major)
		n := CountClientBackends(inst)
		if n != 0 {
			// Active connections (or error) — reset the idle clock.
			delete(idleSince, key)
			continue
		}
		// Zero client backends — check how long it has been idle.
		// Also respect LastUsedAt from url calls.
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
		if time.Since(effectiveLastUsed) < idleStopThreshold {
			delete(idleSince, key)
			continue
		}
		if _, seen := idleSince[key]; !seen {
			idleSince[key] = time.Now()
		}
		if time.Since(idleSince[key]) >= idleStopThreshold {
			fmt.Fprintf(os.Stderr, "af-db: idle-stop postgres-%d (no clients for %s)\n",
				inst.Major, idleStopThreshold)
			if err := stopServer(inst); err != nil {
				fmt.Fprintf(os.Stderr, "af-db: idle-stop error: %v\n", err)
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
