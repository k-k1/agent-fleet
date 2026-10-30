package main

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mempressure"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceStatsRouteRegistered(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/workspace/stats", nil)
	_, pattern := buildMux().Handler(req)
	if pattern != "GET /workspace/stats" {
		t.Fatalf("route pattern=%q", pattern)
	}
}

// The CP puts the keys returned here straight onto the member detail and the WS bar
// (docs/log/63 §63.9). The contract is that these key names match the docker path
// (control-plane/metrics.go); a mismatch raises nothing, the tiles just quietly fall back to
// "-", so pin them here.
func TestWorkspaceStatsWireKeys(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"memory.current": "1073741824\n",
		"memory.max":     "4294967296\n",
		"memory.events":  "oom_kill 0\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AF_CGROUP_DIR", dir)

	w := httptest.NewRecorder()
	handleWorkspaceStats(w, httptest.NewRequest(http.MethodGet, "/workspace/stats", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["mem_used"] != float64(1073741824) || out["mem_max"] != float64(4294967296) {
		t.Errorf("mem = %v/%v, want 1073741824/4294967296", out["mem_used"], out["mem_max"])
	}
	if v, ok := out["oom_kill_total"]; !ok || v != float64(0) {
		t.Errorf("oom_kill_total = %v (present=%v), want a present 0", v, ok)
	}
	// No cpu.stat was written, so CPU is unmeasurable and the key is omitted entirely.
	if v, ok := out["cpu_pct"]; ok {
		t.Errorf("cpu_pct = %v, want the key absent when unreadable", v)
	}
}

// The admin member views flag a start whose CLI install failed from this key alone, so it
// must carry only failures: ok / skipped CLIs and an unreadable status file leave it out.
func TestWorkspaceStatsBootInstallFailed(t *testing.T) {
	t.Setenv("AF_CGROUP_DIR", t.TempDir())
	stats := func() map[string]any {
		w := httptest.NewRecorder()
		handleWorkspaceStats(w, httptest.NewRequest(http.MethodGet, "/workspace/stats", nil))
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	old := bootInstallStatusPath
	t.Cleanup(func() { bootInstallStatusPath = old })

	dir := filepath.Join(t.TempDir(), "bi")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "s.tsv")
	body := "claude\tfailed\tno_space\ncodex\tok\t\nrtk\tskipped\n../etc/x\tfailed\tother\n" + strings.Repeat("a", 40) + "\tfailed\tother\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	bootInstallStatusPath = p
	got, _ := stats()["boot_install_failed"].(map[string]any)
	if len(got) != 1 || got["claude"] != "no_space" {
		t.Errorf("boot_install_failed = %v, want only claude=no_space (a path-like or over-long tool name is dropped)", got)
	}

	if err := os.WriteFile(p, []byte("codex\tok\t\nrtk\tskipped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, ok := stats()["boot_install_failed"]; ok {
		t.Errorf("no failure: boot_install_failed = %v, want the key absent", v)
	}

	bootInstallStatusPath = filepath.Join(dir, "missing.tsv")
	if v, ok := stats()["boot_install_failed"]; ok {
		t.Errorf("unknown status: boot_install_failed = %v, want the key absent", v)
	}
}

// The mem_pressure axis (ADR 0120 phase 0): absent while no sampler runs, an explicit
// available=false (with no figures) when pressure cannot be measured, and the latest reading
// otherwise. Never a zero for an unreadable signal.
func TestWorkspaceStatsMemPressure(t *testing.T) {
	cg := t.TempDir()
	t.Setenv("AF_CGROUP_DIR", cg)
	get := func() map[string]any {
		w := httptest.NewRecorder()
		handleWorkspaceStats(w, httptest.NewRequest(http.MethodGet, "/workspace/stats", nil))
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	t.Cleanup(func() { mempressure.Default = nil })

	mempressure.Default = nil
	if v, ok := get()["mem_pressure"]; ok {
		t.Fatalf("no sampler: mem_pressure = %v, want the key absent", v)
	}

	sampler := func(b mempressure.Boundary) *mempressure.Sampler {
		return mempressure.New(cg, b, mempressure.Sources{ReadFile: os.ReadFile, Now: time.Now}, nil)
	}

	// Unverified cgroup: present, unavailable, no figure.
	s := sampler(mempressure.Boundary{Reason: "host root"})
	s.Tick()
	mempressure.Default = s
	mp, _ := get()["mem_pressure"].(map[string]any)
	if mp == nil || mp["available"] != false || mp["reason"] != "host root" || mp["some_avg10"] != nil {
		t.Fatalf("unverified: mem_pressure = %v", mp)
	}

	// Verified but PSI missing: unavailable, not zero.
	s = sampler(mempressure.Boundary{OK: true})
	s.Tick()
	mempressure.Default = s
	mp, _ = get()["mem_pressure"].(map[string]any)
	if mp == nil || mp["available"] != false {
		t.Fatalf("no PSI: mem_pressure = %v", mp)
	}
	if _, ok := mp["some_avg10"]; ok {
		t.Fatalf("no PSI: some_avg10 present: %v", mp)
	}

	// Readable: the figures, with an honest zero being a zero.
	psi := "some avg10=0.00 avg60=1.25 avg300=0.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=1\n"
	if err := os.WriteFile(filepath.Join(cg, "memory.pressure"), []byte(psi), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Tick()
	mp, _ = get()["mem_pressure"].(map[string]any)
	if mp == nil || mp["available"] != true || mp["some_avg10"] != float64(0) || mp["some_avg60"] != 1.25 {
		t.Fatalf("readable: mem_pressure = %v", mp)
	}
}
