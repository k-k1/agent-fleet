package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceMachineRouteRegistered(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/workspace/machine", nil)
	_, pattern := buildMux().Handler(req)
	if pattern != "GET /workspace/machine" {
		t.Fatalf("route pattern=%q", pattern)
	}
}

// The CP decodes these key names field by field (agentMachine in control-plane), and a
// renamed tag raises nothing there — the row just falls back to the declared value. Pin
// the names at the boundary.
func TestWorkspaceMachineWireKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte("7516192768\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_CGROUP_DIR", dir)
	dmi := t.TempDir()
	for name, body := range map[string]string{"sys_vendor": "Amazon EC2\n", "product_name": "m8g.large\n"} {
		if err := os.WriteFile(filepath.Join(dmi, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AF_DMI_DIR", dmi)

	w := httptest.NewRecorder()
	handleWorkspaceMachine(w, httptest.NewRequest(http.MethodGet, "/workspace/machine", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["instance_type"] != "m8g.large" {
		t.Errorf("instance_type = %v, want m8g.large", out["instance_type"])
	}
	if out["mem_max"] != float64(7516192768) {
		t.Errorf("mem_max = %v, want 7516192768", out["mem_max"])
	}
	if s, _ := out["arch"].(string); s == "" {
		t.Errorf("arch = %v, want the image's architecture", out["arch"])
	}
}
