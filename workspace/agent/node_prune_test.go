package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeNode creates ~/.nvm/versions/node/<v>/bin/node, with install-node's marker when
// marked.
func fakeNode(t *testing.T, v string, marked bool) {
	t.Helper()
	dir := filepath.Join(nvmNodeRoot(), v)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "node"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if marked {
		if err := os.WriteFile(filepath.Join(dir, nodeInstallMarker), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func nodeDirs(t *testing.T) []string {
	t.Helper()
	ents, _ := os.ReadDir(nvmNodeRoot())
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func TestPruneSupersededNodesKeepsNewestAndNvmInstalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.9.0", true)   // superseded, ours → removed
	fakeNode(t, "v22.20.0", false) // superseded, nvm's (an exact pin) → kept
	fakeNode(t, "v22.23.4", true)  // newest of 22 → kept
	fakeNode(t, "v24.1.0", true)   // only patch of 24 → kept
	removed, err := pruneSupersededNodes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "v22.9.0" {
		t.Fatalf("removed %v, want [v22.9.0]", removed)
	}
	if got := strings.Join(nodeDirs(t), ","); got != "v22.20.0,v22.23.4,v24.1.0" {
		t.Fatalf("left %s", got)
	}
}

// The newest is chosen numerically (v22.9.0 < v22.23.2), and a newer nvm install still
// supersedes our older one.
func TestPruneSupersededNodesNumericAndNvmNewest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.23.2", true)
	fakeNode(t, "v22.30.0", false)
	removed, _ := pruneSupersededNodes()
	if strings.Join(removed, ",") != "v22.23.2" {
		t.Fatalf("removed %v, want [v22.23.2]", removed)
	}
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.9.0", false)
	fakeNode(t, "v22.23.2", true)
	if removed, _ := pruneSupersededNodes(); len(removed) != 0 {
		t.Fatalf("removed %v: v22.23.2 is the newest, not v22.9.0", removed)
	}
}

// A tree without bin/node is neither the newest nor removed.
func TestPruneSupersededNodesIgnoresBrokenTrees(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.1.0", true)
	broken := filepath.Join(nvmNodeRoot(), "v22.99.0")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if removed, _ := pruneSupersededNodes(); len(removed) != 0 {
		t.Fatalf("removed %v: a broken tree must not supersede a working one", removed)
	}
	if _, err := os.Stat(broken); err != nil {
		t.Fatalf("broken tree removed: %v", err)
	}
}

func TestPruneSupersededNodesNoNvmDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if removed, err := pruneSupersededNodes(); err != nil || len(removed) != 0 {
		t.Fatalf("removed %v err %v", removed, err)
	}
}

func TestInstalledNodeVersions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeNode(t, "v22.9.0", false)
	fakeNode(t, "v22.23.4", true)
	fakeNode(t, "v24.1.0", true)
	got := installedNodeVersions()
	if got["22"] != "v22.23.4" || got["24"] != "v24.1.0" || len(got) != 2 {
		t.Fatalf("installedNodeVersions = %v", got)
	}
}

// A POST refused because another major's download runs reports that job as
// "installing", even if it ended right after the refusal.
func TestNodeInstallBusyReportsRunningJob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	nodeInstaller.mu.Lock()
	nodeInstaller.state, nodeInstaller.major, nodeInstaller.err = "installing", "24", ""
	nodeInstaller.mu.Unlock()
	t.Cleanup(func() {
		nodeInstaller.mu.Lock()
		nodeInstaller.state, nodeInstaller.major, nodeInstaller.err = "", "", ""
		nodeInstaller.mu.Unlock()
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/env/node-install", strings.NewReader(`{"major":"22"}`))
	r.Header.Set("Content-Type", "application/json")
	handleNodeInstall(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"state":"installing"`) || !strings.Contains(w.Body.String(), `"major":"24"`) {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
}
