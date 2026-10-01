package mcpx

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// branchTestEnv serves the Agent with answer for every request and records "METHOD path body".
// HOME and the meta store are temp dirs, and the served session is owner01, working in
// ~/repos/proj@wt.
func branchTestEnv(t *testing.T, status int, answer string) *[]string {
	t.Helper()
	withMCPFlags(t, false, true, false)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	oldSource := mcpSourceSession
	mcpSourceSession = "owner01"
	mcpAdvertised.mu.Lock()
	oldNames := mcpAdvertised.names
	mcpAdvertised.names = nil
	mcpAdvertised.mu.Unlock()
	t.Cleanup(func() {
		mcpSourceSession = oldSource
		mcpAdvertised.mu.Lock()
		mcpAdvertised.names = oldNames
		mcpAdvertised.mu.Unlock()
	})
	session.WriteMeta(session.Meta{Name: "owner01", Dir: filepath.Join(home, "repos", "proj@wt"), Subdir: "console", Kind: "claude"})

	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.EscapedPath()+" "+string(b))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)
	return &seen
}

type branchToolResult struct {
	IsError bool
	Text    string
}

func callBranchName(t *testing.T, args map[string]any) branchToolResult {
	t.Helper()
	out := callSelfTool(t, mcpToolBranchName, args)
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil || len(parsed.Result.Content) == 0 {
		t.Fatalf("branch_name answer = %s (%v)", out, err)
	}
	return branchToolResult{parsed.Result.IsError, parsed.Result.Content[0].Text}
}

// Every session gets branch_name whatever its other capabilities; the operator surface does not,
// because "your own working copy" means nothing there.
func TestBranchNameAdvertisedOnEverySessionSurface(t *testing.T) {
	withMCPFlags(t, false, true, false)
	oldPeer, oldSpawn := mcpPeerMessagingEnabled, mcpFleetSpawnEnabled
	t.Cleanup(func() { mcpPeerMessagingEnabled, mcpFleetSpawnEnabled = oldPeer, oldSpawn })
	for mask := 0; mask < 8; mask++ {
		setSessionChromiumEnabled(mask&1 != 0)
		mcpPeerMessagingEnabled, mcpFleetSpawnEnabled = mask&2 != 0, mask&4 != 0
		if !toolListed(mcpStdioToolList(), mcpToolBranchName) {
			t.Errorf("mask=%d: session surface lacks %s", mask, mcpToolBranchName)
		}
	}
	for _, write := range []bool{false, true} {
		withMCPFlags(t, write, false, false)
		if toolListed(mcpStdioToolList(), mcpToolBranchName) {
			t.Errorf("operator surface (write=%v) advertises %s", write, mcpToolBranchName)
		}
	}
}

func toolListed(tools []map[string]any, name string) bool {
	for _, tool := range tools {
		if tool["name"] == name {
			return true
		}
	}
	return false
}

// With no arguments the tool asks about the caller's own working copy (Meta.Dir, not the
// subdirectory it runs in) and names the work item its own session was launched for.
func TestBranchNameDefaultsToOwnWorkingCopyAndSession(t *testing.T) {
	answer := `{"name":"feature/1128-branch-name-tool","name_empty":false,"base":"develop","base_branch":"origin/develop","kind":"feature","provisional":false,"warnings":[],"sources":{},"gitflow":"absent"}`
	seen := branchTestEnv(t, http.StatusOK, answer)

	got := callBranchName(t, map[string]any{})
	if got.IsError {
		t.Fatalf("branch_name failed: %s", got.Text)
	}
	if want := `POST /repos/proj@wt/branch-name {"session":"owner01"}`; len(*seen) != 1 || (*seen)[0] != want {
		t.Fatalf("Agent saw %q, want %q", *seen, want)
	}
	if got.Text != answer {
		t.Fatalf("result = %s, want the resolver's answer verbatim", got.Text)
	}
}

// Explicit input is forwarded as the resolver's own request; an item means the session's
// recorded item is not consulted, so no session is filled in.
func TestBranchNameForwardsExplicitInput(t *testing.T) {
	seen := branchTestEnv(t, http.StatusOK, `{"name":"fix/PROJ-7-crash"}`)

	got := callBranchName(t, map[string]any{
		"repo": "other",
		"item": map[string]any{"provider": "jira", "key": "PROJ-7", "title": "Crash", "type": "Bug", "labels": []string{"a"}},
		"kind": "fix",
		"slug": "crash",
	})
	if got.IsError {
		t.Fatalf("branch_name failed: %s", got.Text)
	}
	want := `POST /repos/other/branch-name {"item":{"provider":"jira","key":"PROJ-7","title":"Crash","type":"Bug","labels":["a"]},"kind":"fix","slug":"crash"}`
	if len(*seen) != 1 || (*seen)[0] != want {
		t.Fatalf("Agent saw %q, want %q", *seen, want)
	}

	// kind and slug alone (the rename flow) still name the session, which supplies the number.
	*seen = nil
	callBranchName(t, map[string]any{"kind": "docs", "slug": "tool-docs", "session": "other02"})
	if want := `POST /repos/proj@wt/branch-name {"session":"other02","kind":"docs","slug":"tool-docs"}`; len(*seen) != 1 || (*seen)[0] != want {
		t.Fatalf("Agent saw %q, want %q", *seen, want)
	}
}

func TestBranchNameErrors(t *testing.T) {
	cases := []struct {
		name, status, body, repo, want string
	}{
		{"bad repo", "400", `{"code":"bad_repo","message":"invalid repo name"}`, "a b", `"a b"`},
		{"not a working copy", "404", `{"code":"not_git","message":"not a git working copy"}`, "gone", `"gone"`},
		{"unknown session", "404", `{"code":"session_not_found","message":"no such session"}`, "", `"owner01"`},
		{"older Agent", "404", "404 page not found\n", "", "ADR 0103"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status := map[string]int{"400": 400, "404": 404}[c.status]
			branchTestEnv(t, status, c.body)
			args := map[string]any{}
			if c.repo != "" {
				args["repo"] = c.repo
			}
			got := callBranchName(t, args)
			if !got.IsError || !strings.Contains(got.Text, c.want) {
				t.Fatalf("result = %+v, want an error naming %s", got, c.want)
			}
		})
	}

	// "." and ".." survive url.PathEscape, and the Agent's ServeMux answers them with a redirect
	// to a cleaned path that has no such route: followed, it reads as an Agent without the
	// resolver. The server here is a real mux with the real pattern, so that path is exercised.
	for _, repo := range []string{".", ".."} {
		t.Run("dot repo "+repo, func(t *testing.T) {
			branchTestEnv(t, 200, `{}`)
			var hits []string
			mux := http.NewServeMux()
			mux.HandleFunc("POST /repos/{name}/branch-name", func(w http.ResponseWriter, r *http.Request) {
				hits = append(hits, r.PathValue("name"))
				_, _ = w.Write([]byte(`{"name":"x"}`))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			u, _ := url.Parse(srv.URL)
			t.Setenv("AGENT_ADDR", u.Host)

			got := callBranchName(t, map[string]any{"repo": repo})
			if !got.IsError || !strings.Contains(got.Text, "~/repos 直下のフォルダ名として使えません") || len(hits) != 0 {
				t.Fatalf("repo %q: result = %+v, resolver hits %q; want an invalid-name refusal", repo, got, hits)
			}
		})
	}

	t.Run("resolver unreachable", func(t *testing.T) {
		branchTestEnv(t, 200, `{}`)
		t.Setenv("AGENT_ADDR", "127.0.0.1:1")
		got := callBranchName(t, map[string]any{})
		if !got.IsError || !strings.Contains(got.Text, "接続できません") {
			t.Fatalf("result = %+v, want an unreachable-resolver error", got)
		}
	})

	t.Run("own folder outside ~/repos", func(t *testing.T) {
		seen := branchTestEnv(t, 200, `{}`)
		session.WriteMeta(session.Meta{Name: "owner01", Dir: os.Getenv("HOME"), Kind: "claude"})
		got := callBranchName(t, map[string]any{})
		if !got.IsError || !strings.Contains(got.Text, "repo") || len(*seen) != 0 {
			t.Fatalf("result = %+v, Agent saw %q; want a refusal asking for repo", got, *seen)
		}
	})

	t.Run("no owner and no repo", func(t *testing.T) {
		branchTestEnv(t, 200, `{}`)
		_, probed := ownerTestEnv(t, nil)
		got := callBranchName(t, map[string]any{})
		if !got.IsError || !strings.Contains(got.Text, "repo") {
			t.Fatalf("result = %+v, want a refusal asking for repo", got)
		}
		for _, s := range *probed {
			if strings.Contains(s, "branch-name") {
				t.Fatalf("resolver was asked with no working copy: %q", *probed)
			}
		}
	})
}
