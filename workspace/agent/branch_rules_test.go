package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/branchrule"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestBranchNameRoutes drives the resolver's REST surface over a real working copy: the
// effective rule, a name for an item and for a session that recorded one, the advisory
// check, and the user store refusing a bare-* name.
func TestBranchNameRoutes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := filepath.Join(home, "repos", "app")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", dir},
		{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", dir, "remote", "add", "origin", "https://github.com/acme/app.git"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	old := branchBitbucket
	branchBitbucket = branchrule.NewBitbucketCache(func(context.Context, string, string) ([]byte, error) {
		t.Error("a GitHub origin must not ask Bitbucket")
		return nil, branchrule.ErrNoConnection
	})
	defer func() { branchBitbucket = old }()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{name}/branch-rule", handleGetBranchRule)
	mux.HandleFunc("POST /repos/{name}/branch-name", handleBranchName)
	mux.HandleFunc("POST /repos/{name}/branch-name/check", handleBranchNameCheck)
	mux.HandleFunc("GET /branch-rules/user", handleGetUserBranchRules)
	mux.HandleFunc("PUT /branch-rules/user", handlePutUserBranchRules)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var rule struct {
		RepoID     string                `json:"repo_id"`
		Name       string                `json:"name"`
		Base       string                `json:"base"`
		BaseBranch string                `json:"base_branch"`
		Kinds      []branchrule.KindView `json:"kinds"`
		Gitflow    string                `json:"gitflow"`
		Sources    map[string]any        `json:"sources"`
	}
	do(t, srv, "GET", "/repos/app/branch-rule", nil, http.StatusOK, &rule)
	if rule.RepoID != "github.com/acme/app" || rule.Name != "{prefix}{ref}-{slug}" || rule.Base != "head" ||
		rule.BaseBranch != "main" || len(rule.Kinds) != 8 || rule.Gitflow != "absent" || rule.Sources["name"] != "builtin: builtin" {
		t.Errorf("branch-rule = %+v", rule)
	}

	type nameOut struct {
		Name      string               `json:"name"`
		NameEmpty bool                 `json:"name_empty"`
		Base      string               `json:"base"`
		Kind      string               `json:"kind"`
		Warnings  []branchrule.Warning `json:"warnings"`
	}
	var got nameOut
	do(t, srv, "POST", "/repos/app/branch-name", map[string]any{
		"item": map[string]any{"provider": "github", "key": "acme/app#12", "title": "Crash on start", "labels": []string{"bug"}},
	}, http.StatusOK, &got)
	if got.Name != "fix/12-crash-on-start" || got.Kind != "bugfix" || got.Base != "head" || got.Warnings == nil {
		t.Errorf("item: %+v", got)
	}

	session.WriteMeta(session.Meta{Name: "s1", Dir: dir, Kind: "shell", WorkItem: &session.WorkItemRef{
		Provider: "github", Key: "acme/app#12", Title: "ログイン", Labels: []string{"bug"},
	}})
	got = nameOut{}
	do(t, srv, "POST", "/repos/app/branch-name", map[string]any{"session": "s1", "slug": "login fails"}, http.StatusOK, &got)
	if got.Name != "fix/12-login-fails" {
		t.Errorf("session: %+v", got)
	}
	do(t, srv, "POST", "/repos/app/branch-name", map[string]any{"session": "nope"}, http.StatusNotFound, nil)

	// The user's template {prefix}{key} with no item and no slug renders only the prefix.
	writeHomeUIPrefs(t, home, `{"workItemBranchTemplate":"{prefix}{key}"}`)
	got = nameOut{}
	do(t, srv, "POST", "/repos/app/branch-name", map[string]any{}, http.StatusOK, &got)
	if !got.NameEmpty || got.Name != "" {
		t.Errorf("empty: %+v", got)
	}

	var check struct {
		Warnings []branchrule.Warning `json:"warnings"`
	}
	do(t, srv, "POST", "/repos/app/branch-name/check", map[string]any{"name": "feat/12-x"}, http.StatusOK, &check)
	if len(check.Warnings) != 1 || check.Warnings[0].Code != "prefix_mismatch" {
		t.Errorf("check: %+v", check)
	}

	do(t, srv, "PUT", "/branch-rules/user", map[string]any{"rules": []any{map[string]any{"match": "*", "name": "{ref}"}}},
		http.StatusBadRequest, nil)
	var user branchrule.UserRules
	do(t, srv, "PUT", "/branch-rules/user", map[string]any{"rules": []any{
		map[string]any{"match": "github.com/acme/*", "base": "develop"},
	}}, http.StatusOK, &user)
	do(t, srv, "GET", "/branch-rules/user", nil, http.StatusOK, &user)
	if len(user.Rules) != 1 || *user.Rules[0].Base != "develop" {
		t.Errorf("user = %+v", user)
	}
	// develop exists nowhere, so the resolved base warns and falls back to head.
	got = nameOut{}
	do(t, srv, "POST", "/repos/app/branch-name", map[string]any{"slug": "x"}, http.StatusOK, &got)
	if got.Base != "head" || len(got.Warnings) != 1 || got.Warnings[0].Code != "base_missing" {
		t.Errorf("missing base: %+v", got)
	}
}

func writeHomeUIPrefs(t *testing.T, home, body string) {
	t.Helper()
	p := filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
