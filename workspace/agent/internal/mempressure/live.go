package mempressure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// cgroupDir mirrors resources.cgroupDir (AF_CGROUP_DIR, else /sys/fs/cgroup).
func cgroupDir() string {
	if v := os.Getenv("AF_CGROUP_DIR"); v != "" {
		return v
	}
	return "/sys/fs/cgroup"
}

// Start verifies the cgroup boundary, then runs the sampler until the process ends. agentAddr is
// the Agent's listen address (":7700"), the target of the latency probe. It is a no-op with
// AF_MEM_PRESSURE=off.
func Start(agentAddr string) {
	if !Enabled() {
		return
	}
	self, _ := os.ReadFile("/proc/self/cgroup")
	mi, _ := os.ReadFile("/proc/self/mountinfo")
	_, statErr := os.Stat(cgroupDir() + "/memory.max")
	b := CheckBoundary(cgroupDir(), string(self), string(mi), statErr == nil)
	if !b.OK {
		b.Reason = "cgroup not verified as this container's own: " + b.Reason
	}
	logBoundary(b)
	b.MemMax, b.MemMaxDetail = CheckMemoryMax(b, cgroupDir(), os.Getenv(RequestedLimitEnv))
	logMemMax(b)
	s := New(cgroupDir(), b, liveSources(agentAddr),
		NewSeries(paths.AgentStateDir()+"/mem-pressure/series.jsonl", 0))
	defMu.Lock()
	Default = s
	defMu.Unlock()
	go s.Run(nil)
}

func liveSources(agentAddr string) Sources {
	_, port, err := net.SplitHostPort(agentAddr)
	if err != nil {
		port = "7700"
	}
	url := "http://127.0.0.1:" + port + "/healthz"
	client := &http.Client{Timeout: probeTimeout}
	return Sources{
		ReadFile: os.ReadFile,
		Now:      time.Now,
		Probe: func() (time.Duration, error) {
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					err = fmt.Errorf("healthz: %s", resp.Status)
				}
			}
			return time.Since(start), err
		},
		Live:  liveSessions,
		Procs: procx.Snapshot,
		Rollup: func(pid int) (string, error) {
			b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/smaps_rollup")
			return string(b), err
		},
	}
}

// liveSessions lists the tmux sessions with their pane root pid in one tmux call. Managed
// sessions have no pane and are not attributable in phase 0 (the process tree under the pane is
// the only attribution built), so they are absent from coverage rather than counted as measured.
//
// The call has a deadline: a tmux server stalled by the very pressure being measured must not
// stall the sampler with it. A timeout is an error, recorded as a missing pass; "no server" is
// just no sessions.
func liveSessions() ([]Live, error) {
	cmd := tmuxx.Cmd("list-panes", "-a", "-F", "#{session_name} #{pane_pid}")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Start(); err != nil {
		return nil, nil
	}
	timedOut := false
	var mu sync.Mutex
	timer := time.AfterFunc(tmuxTimeout, func() {
		mu.Lock()
		timedOut = true
		mu.Unlock()
		_ = cmd.Process.Kill()
	})
	werr := cmd.Wait()
	timer.Stop()
	mu.Lock()
	to := timedOut
	mu.Unlock()
	if to {
		return nil, errors.New("tmux list-panes timed out")
	}
	if werr != nil {
		return nil, nil
	}
	out := buf.Bytes()
	seen := map[string]bool{}
	var live []Live
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 || !strings.HasPrefix(f[0], session.TmuxPrefix) {
			continue
		}
		name := strings.TrimPrefix(f[0], session.TmuxPrefix)
		pid, err := strconv.Atoi(f[1])
		if err != nil || seen[name] {
			continue
		}
		seen[name] = true
		kind := "unknown"
		if m, ok := session.ReadMeta(name); ok && m.Kind != "" {
			kind = m.Kind
		}
		live = append(live, Live{Name: name, Kind: kind, Root: pid})
	}
	return live, nil
}

// tmuxTimeout bounds the once-a-minute tmux call.
var tmuxTimeout = 2 * time.Second
