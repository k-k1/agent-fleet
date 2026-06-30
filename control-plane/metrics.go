package main

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Resource metrics surfaced as small chips in the Console WsBar.
//
// Two scopes with different audiences:
//   - Container stats (your own Workspace: mem / CPU vs its quota) → every user.
//     This is the user's own container, so it leaks nothing, and it is the most
//     actionable signal ("my workspace is about to OOM").
//   - Host stats (loadavg, total memory) → super_admin only. Exposing host-wide
//     load and capacity to a tenant would leak how busy other tenants are,
//     against the 相互不可視 principle (runtime.go §9.7).
//
// The CP runs as a host process (deploy/local/run-dev.sh), so it reads /proc and
// the per-container cgroup v2 scope directly. No Agent change, no image rebuild,
// and it works on already-running containers.

// --- Host stats (super_admin only) ---

func readHostStats() (load1 float64, ncpu int, memUsed, memTotal uint64) {
	ncpu = runtime.NumCPU()
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			load1, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	var avail uint64
	if f, err := os.Open("/proc/meminfo"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text()) // e.g. "MemTotal:  28158804 kB"
			if len(fields) < 2 {
				continue
			}
			kb, _ := strconv.ParseUint(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				memTotal = kb * 1024
			case "MemAvailable:":
				avail = kb * 1024
			}
		}
	}
	if memTotal > avail {
		memUsed = memTotal - avail
	}
	return
}

func (c config) handleHostStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireSuperAdmin(w, r); !ok {
		return
	}
	load1, ncpu, memUsed, memTotal := readHostStats()
	writeJSON(w, http.StatusOK, map[string]any{
		"load1": load1, "ncpu": ncpu, "mem_used": memUsed, "mem_total": memTotal,
	})
}

// --- Container stats (all users, scoped to the caller's own workspace) ---

type cpuSample struct {
	usageUsec uint64
	at        time.Time
}

// cpuPrev holds the previous cpu.stat reading per container id so we can derive a
// percentage from the cumulative counter. Keyed by id, so a recreate (new id)
// starts fresh. Entries accumulate slowly (one per container ever seen) and are
// trivially small, so we do not prune.
var (
	cpuMu   sync.Mutex
	cpuPrev = map[string]cpuSample{}
)

func dockerContainerID(ctx context.Context, name string) string {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.Id}}", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// cgroupScope is the host cgroup v2 path for a docker container under the systemd
// cgroup driver (docker info: systemd / v2 on this host).
func cgroupScope(id string) string {
	return "/sys/fs/cgroup/system.slice/docker-" + id + ".scope"
}

// readCgroupUint reads a single-value cgroup file. "max" (unlimited) reports !ok.
func readCgroupUint(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

func readUsageUsec(scope string) (uint64, bool) {
	b, err := os.ReadFile(scope + "/cpu.stat")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "usage_usec" {
			v, err := strconv.ParseUint(f[1], 10, 64)
			return v, err == nil
		}
	}
	return 0, false
}

// containerCPUPct returns CPU usage as a percentage of one core (the docker-stats
// convention: 100% = one full core, can exceed 100% on multi-core). It needs two
// samples, so the first call after a container appears reports !ok.
func containerCPUPct(id, scope string) (float64, bool) {
	usage, ok := readUsageUsec(scope)
	if !ok {
		return 0, false
	}
	now := time.Now()
	cpuMu.Lock()
	prev, had := cpuPrev[id]
	cpuPrev[id] = cpuSample{usageUsec: usage, at: now}
	cpuMu.Unlock()
	if !had {
		return 0, false
	}
	wall := now.Sub(prev.at).Microseconds()
	if wall <= 0 || usage < prev.usageUsec {
		return 0, false
	}
	return float64(usage-prev.usageUsec) / float64(wall) * 100, true
}

func (c config) handleWorkspaceStats(w http.ResponseWriter, r *http.Request) {
	rt, ok := c.rtFor(w, r)
	if !ok {
		return
	}
	id := dockerContainerID(r.Context(), rt.name)
	if id == "" {
		writeJSON(w, http.StatusOK, map[string]any{"running": false})
		return
	}
	scope := cgroupScope(id)
	memUsed, okMem := readCgroupUint(scope + "/memory.current")
	if !okMem {
		// Scope path absent (e.g. cgroupfs driver or a different layout): the
		// container is up but we can't read it — degrade rather than 500.
		writeJSON(w, http.StatusOK, map[string]any{"running": true})
		return
	}
	out := map[string]any{"running": true, "mem_used": memUsed}
	if memMax, ok := readCgroupUint(scope + "/memory.max"); ok {
		out["mem_max"] = memMax
	}
	if pct, ok := containerCPUPct(id, scope); ok {
		out["cpu_pct"] = pct
	}
	writeJSON(w, http.StatusOK, out)
}
