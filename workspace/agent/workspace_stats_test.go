package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceStatsRouteRegistered(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/workspace/stats", nil)
	_, pattern := buildMux().Handler(req)
	if pattern != "GET /workspace/stats" {
		t.Fatalf("route pattern=%q", pattern)
	}
}

// CP はここで返るキーをそのままメンバー詳細と WS バーへ載せる（docs/63 §63.9）。
// キー名が docker 経路（control-plane/metrics.go）と一致していることが契約で、
// ずれても例外は出ない——タイルが黙って「–」に戻るだけなので、ここで固定する。
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
	// cpu.stat を置いていないので CPU は測れない = キーごと出ない。
	if v, ok := out["cpu_pct"]; ok {
		t.Errorf("cpu_pct = %v, want the key absent when unreadable", v)
	}
}
