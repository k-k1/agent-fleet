package workitems

// mux_test.go holds the mux the comment and merge contract tests use, and seedBktStore, which
// the move put out of sight of main (bkt_cred_test.go).
//
// Before the move these tests built package main's `buildMux()` (routes.go). routes.go is not
// visible from this package (that import would run the wrong way), so this mux registers just
// the 5 work item routes, WITH THE SAME PATTERN STRINGS AND WRAPPERS as routes.go: the two
// writes go through memberorigin.Require with the same limits.
//
// A copy rots silently, so TestWorkItemRoutesMatchAgentRouteTable compares the patterns against
// routes.golden, taken from the real mux (the shape memoryx settled on). That the real route
// table wraps the two writes is checked in package main (workitems_routes_test.go), where the
// real mux is.

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// workItemTestRoutes is the work item section of routes.go.
var workItemTestRoutes = map[string]http.Handler{
	"POST /work-items/fetch":         http.HandlerFunc(HandleWorkItemsFetch),
	"POST /work-items/comment":       memberorigin.Require(WorkItemCommentMaxRequest, http.HandlerFunc(HandleWorkItemsComment)),
	"POST /work-items/detail":        http.HandlerFunc(HandleWorkItemsDetail),
	"POST /work-items/merge":         memberorigin.Require(WorkItemMergeMaxRequest, http.HandlerFunc(HandleWorkItemsMerge)),
	"POST /work-items/jira-projects": http.HandlerFunc(HandleWorkItemsJiraProjects),
}

func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	for pattern, h := range workItemTestRoutes {
		mux.Handle(pattern, h)
	}
	return mux
}

// seedBktStore is a copy of package main's helper of the same name (bkt_cred_test.go).
func seedBktStore(t *testing.T, fn func(s *secrets.Data)) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if err := secrets.Update(func(s *secrets.Data) error { fn(s); return nil }); err != nil {
		t.Fatal(err)
	}
}

// agentRouteGoldenPath is the Agent's full route table as captured by Phase 0 (ADR 0067
// decision 6).
const agentRouteGoldenPath = "../../testdata/routes.golden"

// TestWorkItemRoutesMatchAgentRouteTable checks that workItemTestRoutes matches the work item
// section of the Agent's real route table (routes.golden) exactly: a registration dropped from
// routes.go or a drifted pattern would otherwise leave this package's tests green.
func TestWorkItemRoutesMatchAgentRouteTable(t *testing.T) {
	f, err := os.Open(filepath.Clean(agentRouteGoldenPath))
	if err != nil {
		// Not a Skip: a move that changes the depth of this relative path must not pass silently.
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
		if _, path, ok := strings.Cut(line, " "); ok && strings.HasPrefix(path, "/work-items/") {
			want = append(want, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatalf("%s contains no work item route at all - the golden format changed, or the path did", agentRouteGoldenPath)
	}

	got := make([]string, 0, len(workItemTestRoutes))
	for pattern := range workItemTestRoutes {
		got = append(got, pattern)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the work item test mux has drifted from the Agent's route table\n--- workitems (%d)\n%s\n--- routes.golden (%d)\n%s",
			len(got), strings.Join(got, "\n"), len(want), strings.Join(want, "\n"))
	}
}
