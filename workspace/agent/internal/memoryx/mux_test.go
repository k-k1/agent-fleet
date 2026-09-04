package memoryx

// mux_test.go holds the mux this family's HTTP contract tests use, and the helpers that came
// with it (`buildMux` / `smokeDo` / `containsString`, which the move put out of sight of main).
//
// Before the move these tests built package main's `buildMux()` (routes.go, all 247 routes).
// routes.go is not visible from memoryx (that import would run the wrong way), so this mux
// registers just the 10 memory routes, WITH THE SAME PATTERN STRINGS. Reusing the pattern
// strings verbatim keeps both the checks that read the pattern `mux.Handler(req)` returns and
// the r.PathValue a handler reads identical to what they were before the move.
//
// A copy rots silently. These 10 are a copy of routes.go, so
// TestMemoryRoutesMatchAgentRouteTable compares them against routes.golden, which is taken
// from the real mux (the shape browserx settled on in its own mux_test.go).
//
// The two tests that check registration in the route table ITSELF stay in package main
// (TestMemoryRoutesRegistered / TestMemoryP2RoutesRegistered in
// workspace/agent/memory_routes_test.go). They use no unexported memoryx symbol and instead
// check something only the real mux can show - that the 10 memory routes do not swallow the
// existing `/agents/{kind}/models` - so moving them here would leave them measuring nothing.

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// memoryTestRoutes is the same (method, path) -> handler mapping as the memory section of
// routes.go (10 routes).
var memoryTestRoutes = map[string]http.HandlerFunc{
	"GET /agents/memory/roots":         HandleMemoryRoots,
	"GET /agents/memory/snapshots":     HandleMemorySnapshots,
	"POST /agents/memory/snapshots":    HandleMemorySnapshotCreate,
	"GET /agents/memory/diff":          HandleMemoryDiff,
	"GET /agents/memory/tree":          HandleMemoryTree,
	"POST /agents/memory/restore":      HandleMemoryRestore,
	"PUT /agents/memory/settings":      HandleMemorySettings,
	"GET /agents/memory/export":        HandleMemoryExport,
	"POST /agents/memory/import":       HandleMemoryImport,
	"POST /agents/memory/import/apply": HandleMemoryImportApply,
}

func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	for pattern, h := range memoryTestRoutes {
		mux.HandleFunc(pattern, h)
	}
	return mux
}

// smokeDo is the same helper package main's routes_test.go has (docs/log/23 P0-2).
func smokeDo(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// agentRouteGoldenPath is the Agent's full route table as captured by Phase 0 (ADR 0067
// decision 6).
const agentRouteGoldenPath = "../../testdata/routes.golden"

// TestMemoryRoutesMatchAgentRouteTable checks that the copy above (memoryTestRoutes) matches
// the memory section of the Agent's real route table (routes.golden) exactly.
//
// A forgotten registration and a drifted pattern string are visible nowhere else: if
// `mux.HandleFunc("GET /agents/memory/roots", handleMemoryRoots)` disappeared from routes.go,
// this package's tests would stay green because they build their own mux. The golden is taken
// from the real mux, so it is the only place the difference shows.
func TestMemoryRoutesMatchAgentRouteTable(t *testing.T) {
	f, err := os.Open(filepath.Clean(agentRouteGoldenPath))
	if err != nil {
		// Not a Skip: if a move changes the depth of this relative path, a Skip would silently
		// pass over the check and stay green.
		t.Fatalf("read %s: %v", agentRouteGoldenPath, err)
	}
	defer f.Close()

	var want []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, path, ok := strings.Cut(line, " "); ok && strings.HasPrefix(path, "/agents/memory/") {
			want = append(want, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	// Nothing is more dangerous than an empty golden (same reason as browserx /
	// routes_golden_test.go).
	if len(want) == 0 {
		t.Fatalf("%s contains no memory route at all - the golden format changed, or the path did", agentRouteGoldenPath)
	}

	got := make([]string, 0, len(memoryTestRoutes))
	for pattern := range memoryTestRoutes {
		got = append(got, pattern)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("memoryx's test mux has drifted from the Agent's route table\n--- memoryx (%d)\n%s\n--- routes.golden (%d)\n%s",
			len(got), strings.Join(got, "\n"), len(want), strings.Join(want, "\n"))
	}
}
