package sessionx

// mux_test.go holds the mux this family's HTTP contract tests run against, replacing the
// `buildMux` of package main that is no longer visible from here.
//
// routes.go is not visible from sessionx (that would be an import the wrong way round), so this
// registers only the session family's 47 routes, under the same pattern strings. Reusing the
// pattern strings verbatim keeps both the checks that read the pattern `mux.Handler(req)`
// returns and the `r.PathValue` the handlers read identical to what they were.
//
// A copy rots silently, and these 47 lines are a copy of routes.go, so
// TestSessionRoutesMatchAgentRouteTable compares them against routes.golden, which is captured
// from the real mux (the same shape browserx and memoryx use in their own mux_test.go).

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sessionTestRoutes is the same (method, path) → handler map as the session section of
// routes.go (47 routes).
var sessionTestRoutes = map[string]http.HandlerFunc{
	"GET /sessions":                              HandleListSessions,
	"GET /sessions/catalog":                      HandleSessionCatalog,
	"POST /sessions":                             HandleCreateSession,
	"GET /sessions-idempotency/{key}":            HandleIdempotencyLookup,
	"POST /sessions/{name}/fork":                 HandleForkSession,
	"POST /sessions/{name}/stop":                 HandleStopSession,
	"POST /sessions/{name}/halt":                 HandleHaltSession,
	"POST /sessions/{name}/recreate":             HandleRecreateSession,
	"GET /sessions/archived":                     HandleListArchived,
	"GET /sessions/usage":                        HandleSessionsUsage,
	"GET /sessions/cleanup":                      HandleSessionsCleanup,
	"POST /sessions/{name}/lock":                 HandleSessionLock,
	"POST /sessions/{name}/keep-awake":           HandleSessionKeepAwake,
	"POST /sessions/{name}/archive":              HandleArchiveSession,
	"POST /sessions/{name}/restore":              HandleRestoreSession,
	"POST /sessions/{name}/input":                HandleSessionInput,
	"POST /sessions/{name}/carried-answer":       HandleSessionCarriedAnswer,
	"GET /sessions/{name}/settings":              HandleSessionSettingsGet,
	"POST /sessions/{name}/settings":             HandleSessionSettings,
	"POST /sessions/{name}/driver":               HandleSessionDriver,
	"POST /sessions/{name}/paste-image":          HandlePasteImage,
	"GET /sessions/{name}/pasted/{file}":         HandlePastedImage,
	"GET /sessions/{name}/status":                HandleSessionStatus,
	"GET /sessions/{name}/output":                HandleSessionOutput,
	"GET /sessions/{name}/ssm-login":             HandleSSMLoginStatus,
	"POST /sessions/{name}/start":                HandleStartSession,
	"GET /sessions/{name}/messages":              HandleSessionMessages,
	"GET /sessions/{name}/handoff-proposal":      HandleSessionHandoffProposal,
	"POST /sessions/{name}/handoff-proposal":     HandleSessionHandoffProposal,
	"DELETE /sessions/{name}/handoff-proposal":   HandleSessionHandoffProposal,
	"GET /sessions/{name}/handoff-context":       HandleSessionHandoffContext,
	"GET /sessions/{name}/marks":                 HandleSessionMarks,
	"POST /sessions/{name}/marks":                HandleSessionMarks,
	"DELETE /sessions/{name}/marks":              HandleSessionMarks,
	"POST /sessions/{name}/title/accept":         HandleAcceptSuggestedTitle,
	"POST /sessions/{name}/title/dismiss":        HandleDismissSuggestedTitle,
	"POST /sessions/{name}/title/suggest":        HandleSuggestTitle,
	"POST /sessions/{name}/title/set":            HandleSetTitle,
	"POST /sessions/{name}/suggest-branch":       HandleSessionSuggestBranch,
	"POST /sessions/{name}/suggest-replies":      HandleSuggestReplies,
	"GET /sessions/{name}/skills":                HandleSessionSkills,
	"GET /sessions/{name}/committed":             HandleSessionCommittedFiles,
	"POST /sessions/{name}/rename-branch":        HandleSessionRenameBranch,
	"POST /chat/conversations/{id}/lock":         HandleChatLock,
	"POST /chat/conversations/{id}/paste-image":  HandleChatPasteImage,
	"GET /chat/conversations/{id}/pasted/{file}": HandleChatPastedImage,
	"POST /repos/{name}/lock":                    HandleRepoLock,
}

func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	for pattern, h := range sessionTestRoutes {
		mux.HandleFunc(pattern, h)
	}
	return mux
}

// TestSessionRoutesMatchAgentRouteTable checks that the copy above matches the real route
// table. The golden is captured from the real mux, so changing a pattern in routes.go without
// updating this file fails here.
//
// The direction of the match is "copy ⊆ golden"; the reverse does not hold, because the agent
// has routes outside the session family. That is why the count is checked too: an empty copy
// would satisfy "is a subset" on its own.
func TestSessionRoutesMatchAgentRouteTable(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "routes.golden"))
	if err != nil {
		t.Fatalf("cannot read routes.golden: %v (did the relative path depth change?)", err)
	}
	defer f.Close()

	golden := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		golden[line] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(golden) < 200 {
		t.Fatalf("only %d routes read from routes.golden = this check has gone silent", len(golden))
	}

	var missing []string
	for pattern := range sessionTestRoutes {
		if !golden[pattern] {
			missing = append(missing, pattern)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("copied routes missing from the real route table (changed in routes.go?): %v", missing)
	}
	if len(sessionTestRoutes) != 47 {
		t.Fatalf("copy has %d routes (want 47) = a thinning copy makes the subset check above measure nothing",
			len(sessionTestRoutes))
	}
}
