package browserx

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// buildMux is the mux this family's HTTP contract tests run against.
//
// routes.go is not visible from browserx (that would be an import the wrong way round), so this
// registers only the 15 browser routes, under the same pattern strings. The pattern strings
// matter because the handlers read r.PathValue("id"): calling a handler directly leaves {id}
// empty and the test stops reproducing a real request.
//
// The table itself lives in exactly one place, browserx (Routes in mux.go); routes.go registers
// the same Routes(), so there is no copy left to rot.
//
// The W5 live server (TestBrowserLiveServerHelper), which also needs `/healthz`, stays in
// package main (workspace/agent/browser_live_helper_test.go): the CP's browser_live_e2e_test.go
// starts it by building the root package with `go test -c .`.
func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	RegisterRoutes(mux)
	return mux
}

// agentRouteGoldenPath holds the Agent's full route table, captured by phase 0 (ADR 0067
// decision 6).
const agentRouteGoldenPath = "../../testdata/routes.golden"

// TestBrowserRoutesMatchAgentRouteTable checks that the table browserx owns (Routes in mux.go)
// matches the browser section of the Agent's assembled route table (routes.golden) exactly.
//
// The copy is gone, but a forgotten registration is still a possible failure: drop
// browserx.RegisterRoutes(mux) from routes.go and every test in this package stays green,
// because Routes() itself is still correct. The golden is taken from the real mux, so it is the
// only thing that notices.
func TestBrowserRoutesMatchAgentRouteTable(t *testing.T) {
	f, err := os.Open(filepath.Clean(agentRouteGoldenPath))
	if err != nil {
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
		if _, path, ok := strings.Cut(line, " "); ok && isBrowserRoutePath(path) {
			want = append(want, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	// Nothing is more dangerous than a golden that matched nothing (same reason as
	// routes_golden_test.go).
	if len(want) == 0 {
		t.Fatalf("%s contains no browser route at all - the golden format changed, or the paths did", agentRouteGoldenPath)
	}

	var got []string
	for _, r := range Routes() {
		got = append(got, r.Pattern)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("browserx's mux has drifted from the Agent route table\n--- browserx (%d)\n%s\n--- routes.golden (%d)\n%s",
			len(got), strings.Join(got, "\n"), len(want), strings.Join(want, "\n"))
	}
}

// isBrowserRoutePath is exactly the path space the browser section of routes.go owns.
func isBrowserRoutePath(path string) bool {
	return strings.HasPrefix(path, "/browser/") ||
		path == "/ws/browser" ||
		path == "/ws/browser-attachments"
}
