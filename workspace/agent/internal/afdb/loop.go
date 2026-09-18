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

// Autostart brings up every instance the member marked for it, and is called
// once from the Agent's boot path — so "when the workspace starts" is what a
// member gets, without a terminal.
//
// Only instances that already exist and are already installed are started: the
// flag can only be set from a row in the Databases tab, which needs a started
// engine to exist, so there is no path here that downloads a server at boot.
//
// Failures are logged, never fatal. This runs alongside session recovery on a
// memory-constrained host; a database that cannot start must not take the
// workspace's boot with it.
func Autostart(reason string) {
	if _, err := os.Stat(registryPath()); os.IsNotExist(err) {
		return
	}
	type want struct{ engine, major string }
	var wanted []want
	_ = withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return nil
		}
		for _, inst := range r.Instances {
			if inst.Autostart && !isInstanceRunning(inst) {
				wanted = append(wanted, want{inst.Engine, inst.Major})
			}
		}
		return nil
	})
	for _, w := range wanted {
		if w.engine == "mysql" {
			// The same gate `af-db up` applies. Autostart must not be the one path
			// that pushes a 2 GiB workspace over its limit.
			if err := checkMemoryGate(); err != nil {
				fmt.Fprintf(os.Stderr, "af-db autostart (%s): skipping mysql: %v\n", reason, err)
				continue
			}
		}
		if _, err := ensureUp(w.engine, w.major, startOpts{}); err != nil {
			fmt.Fprintf(os.Stderr, "af-db autostart (%s): %s-%s: %v\n", reason, w.engine, w.major, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "af-db autostart (%s): %s-%s up\n", reason, w.engine, w.major)
	}
}

// SetAutostart records whether an instance should come up with the workspace.
func SetAutostart(engine, major string, on bool) error {
	return withLock(func() error {
		r, err := readRegistry()
		if err != nil {
			return err
		}
		inst, ok := r.Instances[instanceKey(engine, major)]
		if !ok {
			return errNotRun(fmt.Sprintf("%s has never been started; start it once before asking for it at boot", engine))
		}
		inst.Autostart = on
		return writeRegistry(r)
	})
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
			// Under the start lock, like every other stop: otherwise the loop can
			// stop a server that a concurrent ensureUp has just finished starting
			// (or is still initialising), and the caller gets a URL to a server
			// this goroutine has already shut down.
			stopErr := withStartLock(inst.Engine, inst.Major, func() error {
				if inst.Engine == "mysql" {
					return stopMySQLServer(inst)
				}
				return stopServer(inst)
			})
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
