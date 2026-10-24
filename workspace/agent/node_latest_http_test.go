package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// nodeHome isolates HOME and creates ~/.nvm/versions/node/<v>/bin for each version.
func nodeHome(t *testing.T, versions ...string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, v := range versions {
		if err := os.MkdirAll(filepath.Join(nvmNodeRoot(), v, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func nodeLatestReq(t *testing.T, major string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	handleNodeLatest(w, httptest.NewRequest(http.MethodGet, "/env/node-latest?major="+major, nil))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestNodeLatestNotInstalledReportsInstallVersion(t *testing.T) {
	nodeHome(t)
	stubLookup(t, nodeLatestLookup, "v24.11.0", nil)
	code, out := nodeLatestReq(t, "24")
	if code != http.StatusOK || out["current"] != "" || out["latest"] != "v24.11.0" || out["update_available"] != false {
		t.Fatalf("code %d body %v", code, out)
	}
}

// current is the patch sessions get (nodeBinFor: the highest, numerically).
func TestNodeLatestComparesWithResolvedPatch(t *testing.T) {
	nodeHome(t, "v22.9.0", "v22.23.2")
	stubLookup(t, nodeLatestLookup, "v22.23.4", nil)
	if _, out := nodeLatestReq(t, "22"); out["current"] != "v22.23.2" || out["update_available"] != true {
		t.Fatalf("outdated: %v", out)
	}
	stubLookup(t, nodeLatestLookup, "v22.23.2", nil)
	if _, out := nodeLatestReq(t, "22"); out["update_available"] != false {
		t.Fatalf("current: %v", out)
	}
}

func TestNodeLatestFailureIs502(t *testing.T) {
	nodeHome(t)
	stubLookup(t, nodeLatestLookup, "", errors.New("dial tcp: no route"))
	if code, _ := nodeLatestReq(t, "24"); code != http.StatusBadGateway {
		t.Fatalf("offline: want 502 got %d", code)
	}
}

func TestNodeLatestRejectsUnofferedMajor(t *testing.T) {
	nodeHome(t)
	calls, _ := stubLookup(t, nodeLatestLookup, "v1.0.0", nil)
	for _, m := range []string{"", "system", "999", "22%26x%3D1"} {
		if code, _ := nodeLatestReq(t, m); code != http.StatusBadRequest {
			t.Errorf("major %q: want 400 got %d", m, code)
		}
	}
	if *calls != 0 {
		t.Fatalf("a refused major cost %d lookups", *calls)
	}
}
