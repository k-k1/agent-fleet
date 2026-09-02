package main

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"os/exec"
	// ⚠️ 標準ライブラリの runtime。CP 自身の internal/runtime を素の `runtime` で
	// 綴る（他 41 ファイルと同じ）ために、**衝突する側**へ別名を付けている。
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
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
	ncpu = goruntime.NumCPU()
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

// hostStats serves host load / memory（docs/log/23 残③: adminAPI のメソッドとして
// 登録側で withSuperAdmin に包む）.
func (a adminAPI) hostStats(w http.ResponseWriter, _ *http.Request, _ store.Identity) {
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

// cpuTracker derives a CPU percentage from the cumulative cpu.stat counter by
// remembering the previous reading per container id（docs/log/23 P2-W4: 生の
// package 変数 map+mutex から struct 化。プロセス内キャッシュなのでマルチ
// インスタンス CP でも各インスタンスが自分の差分を持てばよく、共有不要）。
// Keyed by id, so a recreate (new id) starts fresh. Entries accumulate slowly
// (one per container ever seen) and are trivially small, so we do not prune.
type cpuTracker struct {
	mu   sync.Mutex
	prev map[string]cpuSample
}

// pct swaps in the new reading and returns the percentage vs the previous one
// (!ok on the first sample for an id, wall-clock anomalies, or counter resets).
func (t *cpuTracker) pct(id string, usage uint64, now time.Time) (float64, bool) {
	t.mu.Lock()
	prev, had := t.prev[id]
	t.prev[id] = cpuSample{usageUsec: usage, at: now}
	t.mu.Unlock()
	if !had {
		return 0, false
	}
	wall := now.Sub(prev.at).Microseconds()
	if wall <= 0 || usage < prev.usageUsec {
		return 0, false
	}
	return float64(usage-prev.usageUsec) / float64(wall) * 100, true
}

var cpuSamples = &cpuTracker{prev: map[string]cpuSample{}}

// dockerContainerID returns the id of a RUNNING container, else "". docker inspect
// resolves a stopped/exited container too, so we must gate on .State.Running —
// otherwise containerStats reports a stopped workspace as running (a wrong 稼働中 dot,
// hidden 停止中 notice, and an enabled 強制停止 button).
func dockerContainerID(ctx context.Context, name string) string {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}} {{.Id}}", name).Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) < 2 || f[0] != "true" {
		return ""
	}
	return f[1]
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

// readCgroupKV reads one "key value" line from a flat cgroup file (e.g. memory.events,
// whose lines are "oom_kill 3"). Reports !ok when the file or key is absent.
func readCgroupKV(path, key string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == key {
			v, err := strconv.ParseUint(f[1], 10, 64)
			return v, err == nil
		}
	}
	return 0, false
}

// --- OOM detection ---
//
// memory.events' oom_kill is the cumulative count of processes the memory cgroup has
// OOM-killed in this container. The container can survive that (its init lives on while
// a heavy child — a build, an agent — is reaped), so this is the ONLY container-level
// signal for an in-container OOM: docker's .State.OOMKilled flips only when the init
// itself dies. We track the counter per container id and flag oom_recent while a NEW
// kill is within oomRecentWindow, so the WsBar chip can warn right after an OOM even
// though the poll that saw the increment has passed.

const oomRecentWindow = 5 * time.Minute

type oomState struct {
	total  uint64
	lastAt time.Time
}

// oomTracker remembers each container's cumulative oom_kill so a rise can be detected
// across polls. Keyed by id (a recreate = new id starts fresh); entries are tiny and
// few, so like cpuTracker they are not pruned.
type oomTracker struct {
	mu sync.Mutex
	m  map[string]oomState
}

// observe records total for id and reports whether a NEW kill landed within the recent
// window. The FIRST sample for an id only establishes a baseline (a pre-existing count
// from before the CP started, or from a prior poll gap, is not news), so it is never
// "recent".
func (t *oomTracker) observe(id string, total uint64, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, had := t.m[id]
	if had && total > st.total {
		st.lastAt = now
	}
	st.total = total
	t.m[id] = st
	return had && !st.lastAt.IsZero() && now.Sub(st.lastAt) < oomRecentWindow
}

var oomKills = &oomTracker{m: map[string]oomState{}}

// stoppedState is a container's exit disposition, read once it is no longer running.
type stoppedState struct {
	oomKilled bool
	exitCode  int
}

// dockerStoppedState inspects a non-running container for whether the kernel OOM-killed
// it and its exit code — the "the whole workspace died from OOM" signal (distinct from
// an in-container child OOM, which oom_kill above catches while the container lives).
func dockerStoppedState(ctx context.Context, name string) stoppedState {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.OOMKilled}} {{.State.ExitCode}}", name).Output()
	if err != nil {
		return stoppedState{}
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		return stoppedState{}
	}
	st := stoppedState{oomKilled: f[0] == "true"}
	st.exitCode, _ = strconv.Atoi(f[1])
	return st
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
	return cpuSamples.pct(id, usage, time.Now())
}

// containerStats reads a container's live mem/CPU from its cgroup v2 scope,
// keyed by container name. Returns {running:false} when the container is gone,
// {running:true} (no metrics) when the scope path is unreadable, or the full
// {running, mem_used, mem_max?, cpu_pct?} otherwise. Shared by the own-workspace
// chip and the admin per-member view.
func containerStats(ctx context.Context, name string) map[string]any {
	id := dockerContainerID(ctx, name)
	if id == "" {
		// Stopped: surface an OOM-kill of the container itself so the Console can say
		// WHY it went down (crash / OOM) instead of guessing from the bare state.
		out := map[string]any{"running": false}
		if st := dockerStoppedState(ctx, name); st.oomKilled || st.exitCode != 0 {
			if st.oomKilled {
				out["oom_killed"] = true
			}
			out["exit_code"] = st.exitCode
		}
		return out
	}
	scope := cgroupScope(id)
	memUsed, okMem := readCgroupUint(scope + "/memory.current")
	if !okMem {
		// Scope path absent (e.g. cgroupfs driver or a different layout): the
		// container is up but we can't read it — degrade rather than 500.
		return map[string]any{"running": true}
	}
	out := map[string]any{"running": true, "mem_used": memUsed}
	if memMax, ok := readCgroupUint(scope + "/memory.max"); ok {
		out["mem_max"] = memMax
	}
	if pct, ok := containerCPUPct(id, scope); ok {
		out["cpu_pct"] = pct
	}
	// In-container OOM: a heavy child (build / agent) was memory-killed while the
	// container lived on. oom_kill_total is cumulative; oom_recent flags a fresh kill.
	if total, ok := readCgroupKV(scope+"/memory.events", "oom_kill"); ok {
		out["oom_kill_total"] = total
		if oomKills.observe(id, total, time.Now()) {
			out["oom_recent"] = true
		}
	}
	return out
}

// --- The runtime-neutral view (docs/log/63 §63.9) ---
//
// containerStats above only works where the CP and the workspace share a host:
// it shells out to `docker inspect` and reads the host's cgroup tree. On every ECS
// profile (Fargate and ecs-ec2) neither exists in the CP task, so it answered
// running:false with no gauges at all — three "–" tiles under a workspace that was
// plainly up.
//
// The fix is not another runtime adapter. Whatever the runtime, the numbers can
// only come from ONE place — inside the container, where /sys/fs/cgroup is
// namespaced to that workspace — so the Runtime port gains nothing by carrying a
// Stats method that four of five implementations would answer with the same HTTP
// call. workspaceStats keeps the cheap local read as the fast path and asks the
// Agent when it comes up empty.

// workspaceStats returns a workspace's live gauges regardless of runtime.
//
// state is a THUNK, not a value, because rt.State costs real money on ecs-ec2 (a
// DescribeVolumes plus a DescribeServices, uncached) and this runs on the 4-second
// events tick. Callers that already know the state pass a closure over it; the rest
// pass a memoized one so it is paid at most once, and only when the local read
// already failed.
func workspaceStats(ctx context.Context, m *manager, rt runtime.Runtime, state func() string) map[string]any {
	out := containerStats(ctx, rt.Name())
	if out["running"] != true {
		// ⚠️ The Console disables 強制停止 on exactly this field, so a docker read that
		// cannot see the container must not be the last word — on ECS it never can
		// (docs/log/64 §64.27).
		switch state() {
		case "running":
			out["running"] = true
		case "starting":
			out["starting"] = true
		}
	}
	// mem_used is the marker for "the local read saw this workspace". Absent means
	// the CP is not on the workspace's host; the Agent is then the only source.
	// Asking a workspace that is not running would just burn a 5s timeout per tick.
	if _, ok := out["mem_used"]; ok || out["running"] != true {
		return out
	}
	s, err := m.agentStats(ctx, rt)
	if err != nil {
		return out // still running, just unmeasured — the tiles stay "–"
	}
	for k, v := range s {
		out[k] = v
	}
	// oom_recent is a CP-side derivative (a RISE in the cumulative counter), so it
	// has to be tracked here for the Agent path too — otherwise an in-container OOM
	// is invisible on ECS. Keyed by workspace name rather than container id: the id
	// is exactly what this path does not have. A restart resets the container's
	// counter, and observe() only ever flags an increase, so the stale baseline
	// cannot produce a false alarm.
	if total, ok := out["oom_kill_total"].(uint64); ok && oomKills.observe(rt.Name(), total, time.Now()) {
		out["oom_recent"] = true
	}
	return out
}

// stats serves the own-workspace resource chip（docs/log/23 残③: workspaceAPI の
// メソッドとして登録側で withResolved に包む）.
func (a workspaceAPI) stats(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	rt := res.rt
	writeJSON(w, http.StatusOK, workspaceStats(ctx, a.mgr, rt, sync.OnceValue(func() string {
		return rt.State(ctx)
	})))
}

// --- Disk usage (admin per-member view) ---
//
// Unlike mem/CPU (a cheap cgroup file read) disk usage means walking the home
// tree, which is expensive on a large workspace. So it is computed on demand
// (admin opens a member) and cached per dataDir with a short TTL; the admin view
// polls every few seconds but only triggers a fresh du once per diskTTL.

const diskTTL = 60 * time.Second

type diskSample struct {
	bytes uint64
	at    time.Time
}

// diskUsageCache is the TTL cache for du results（docs/log/23 P2-W4: 生の package
// 変数 map+mutex から struct 化。プロセス内キャッシュで、外すと du の再実行が
// 増えるだけ — マルチインスタンス CP でも共有不要）。
type diskUsageCache struct {
	mu sync.Mutex
	m  map[string]diskSample
}

func (c *diskUsageCache) get(key string, now time.Time) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.m[key]; ok && now.Sub(s.at) < diskTTL {
		return s.bytes, true
	}
	return 0, false
}

func (c *diskUsageCache) put(key string, bytes uint64, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = diskSample{bytes: bytes, at: now}
}

var diskUsages = &diskUsageCache{m: map[string]diskSample{}}

// dirDiskUsage returns the byte size of <dataDir>/home via `du -sb`, cached for
// diskTTL per dataDir. Reports !ok when du fails (path gone, etc.).
func dirDiskUsage(ctx context.Context, dataDir string) (uint64, bool) {
	home := dataDir + "/home"
	now := time.Now()
	if v, ok := diskUsages.get(home, now); ok {
		return v, true
	}

	out, err := exec.CommandContext(ctx, "du", "-sb", home).Output()
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return 0, false
	}
	diskUsages.put(home, v, now)
	return v, true
}
