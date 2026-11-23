package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/autorunfile"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// fakeAutorunAgent answers GET /repos/{name}/autorun-file the way the Agent does.
type fakeAutorunAgent struct {
	mu     sync.Mutex
	srv    *httptest.Server
	status int
	body   string
	paths  []string
}

func (a *fakeAutorunAgent) answer(status int, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status, a.body = status, body
}

func (a *fakeAutorunAgent) file(content string) {
	b, _ := json.Marshal(agentAutorunFile{Path: autorunfile.Path, Branch: "main", Commit: "c0ffee", Found: true, Content: content})
	a.answer(http.StatusOK, string(b))
}

func (a *fakeAutorunAgent) requested() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.paths...)
}

// repoEnv is member "a" with memo queues on, the switch on, and the fake Agent behind a
// workspace in state ("" = running).
func repoEnv(t *testing.T, state string) (*inboxEnv, *fakeAutorunAgent) {
	e := newInboxEnv(t)
	e.memoQueuesOn(true)
	e.mgr.memoRepoSettings = true
	a := &fakeAutorunAgent{status: http.StatusOK}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.paths = append(a.paths, r.Method+" "+r.URL.EscapedPath())
		if r.Header.Get("Authorization") != "Bearer tok" {
			agentErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	}))
	t.Cleanup(a.srv.Close)
	ws := store.Workspace{ID: "ws-a", TenantID: e.t1.ID, MembershipID: e.members["a"], ContainerName: "a",
		Network: "n", DataDir: "/d", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
	if err := e.st.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	e.mgr.mu.Lock()
	e.mgr.rts[e.members["a"]] = cachedRT{rt: stubRuntime{endpoint: a.srv.URL, token: "tok", state: state}, ws: ws}
	e.mgr.mu.Unlock()
	return e, a
}

func (e *inboxEnv) repoSettings(categoryID string) (*httptest.ResponseRecorder, memoRepoSettingsWire) {
	e.t.Helper()
	rec := e.do("a", "GET", "/api/memo-queues/"+categoryID+"/repo-settings", nil)
	var out memoRepoSettingsWire
	if rec.Code == http.StatusOK {
		out = decode[memoRepoSettingsWire](e.t, rec)
	}
	return rec, out
}

func diffByKey(ds []memoRepoDiff) map[string]memoRepoDiff {
	m := map[string]memoRepoDiff{}
	for _, d := range ds {
		m[d.Key] = d
	}
	return m
}

func TestMemoRepoSettingsDiff(t *testing.T) {
	e, a := repoEnv(t, "")
	id := e.category("app", "rounds")
	put := map[string]any{"instruction": "implement it", "spendCapUsd": 60, "maxActive": 2, "maxFiresPerDay": 10,
		"continuationMax": 4, "model": "claude-opus-5-5", "enabled": true}
	if rec := e.do("a", "PUT", "/api/memo-queues/"+id, put); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	a.file(`version: 1
categories:
  rounds:
    model: claude-opus-5-5
    spend_cap_usd: 30
    max_active: 3
    max_fires_per_day: 12
    continuation_max: 2
    batch: 2
    instruction: implement it
    enabled: false
    accept_own_sessions: true
  other:
    max_actve: 1
`)
	rec, out := e.repoSettings(id)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := a.requested(); len(got) != 1 || got[0] != "GET /repos/app/autorun-file" {
		t.Fatalf("Agent calls %v", got)
	}
	if !out.Found || !out.Entry || !out.Queue || out.Commit != "c0ffee" || out.Branch != "main" || out.Path != autorunfile.Path ||
		out.Repo != "app" || out.Category != "rounds" || out.CategoryID != id {
		t.Fatalf("header %+v", out)
	}
	// The other category's error is not this category's.
	if len(out.Errors) != 0 {
		t.Fatalf("errors %+v", out.Errors)
	}
	if len(out.Warnings) != 2 || out.Warnings[0].Key != "enabled" || out.Warnings[1].Key != "accept_own_sessions" {
		t.Fatalf("warnings %+v", out.Warnings)
	}
	d := diffByKey(out.Diffs)
	if len(out.Diffs) != 5 {
		t.Fatalf("diffs %+v", out.Diffs)
	}
	// Equal values (model, instruction) are no difference; lowering a limit is not a raise.
	for key, want := range map[string]struct {
		field, cur, file string
		line             int
		raise            bool
	}{
		"spend_cap_usd":     {"spendCapUsd", "60", "30", 5, false},
		"max_active":        {"maxActive", "2", "3", 6, true},
		"max_fires_per_day": {"maxFiresPerDay", "10", "12", 7, true},
		"continuation_max":  {"continuationMax", "4", "2", 8, false},
		"batch":             {"batch", "1", "2", 9, false},
	} {
		got, ok := d[key]
		if !ok || got.Field != want.field || string(got.Current) != want.cur || string(got.File) != want.file ||
			got.Line != want.line || got.Raise != want.raise {
			t.Errorf("%s: got %+v (cur %s file %s), want %+v", key, got, got.Current, got.File, want)
		}
	}
	// Nothing was saved.
	qs, err := e.st.ListMemoQueues(context.Background(), e.members["a"])
	if err != nil || len(qs) != 1 {
		t.Fatalf("queues %v %+v", err, qs)
	}
	if q := qs[0]; q.SpendCapUSD != 60 || !q.Enabled || q.AcceptOwnSessions {
		t.Errorf("settings changed: %+v", qs[0])
	}
}

// Dropping a cap is a raise; a category with no settings yet is compared with the defaults.
func TestMemoRepoSettingsRaiseAndDefaults(t *testing.T) {
	e, a := repoEnv(t, "")
	id := e.category("app", "rounds")
	if rec := e.do("a", "PUT", "/api/memo-queues/"+id, map[string]any{"instruction": "x", "spendCapUsd": 20}); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	a.file("version: 1\ncategories:\n  rounds:\n    spend_cap_usd: 0\n")
	_, out := e.repoSettings(id)
	if d := diffByKey(out.Diffs)["spend_cap_usd"]; !d.Raise || string(d.File) != "0" {
		t.Fatalf("dropping the cap: %+v", out.Diffs)
	}

	fresh := e.category("app", "fresh")
	a.file("version: 1\ncategories:\n  fresh:\n    max_active: 2\n    agent_kind: codex\n    worktree: true\n")
	_, out = e.repoSettings(fresh)
	if out.Queue || !out.Entry {
		t.Fatalf("header %+v", out)
	}
	// max_active 2 and worktree true are the defaults: only the agent differs.
	if len(out.Diffs) != 1 || out.Diffs[0].Key != "agent_kind" || string(out.Diffs[0].Current) != `"claude"` || string(out.Diffs[0].File) != `"codex"` {
		t.Fatalf("diffs %+v", out.Diffs)
	}
}

// A value the Console could not save is an error on its line, and no difference is offered.
func TestMemoRepoSettingsRefusesWhatTheConsoleWouldRefuse(t *testing.T) {
	e, a := repoEnv(t, "")
	id := e.category("app", "rounds")
	if rec := e.do("a", "PUT", "/api/memo-queues/"+id, map[string]any{"instruction": "x", "enabled": true, "openAfter": 3}); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range []struct {
		name, entry string
		line        int
		key, part   string
	}{
		{"range", "    model: m\n    max_active: 999\n", 5, "max_active", "maxActive"},
		{"empty instruction on an enabled queue", "    instruction: ''\n", 4, "instruction", "cannot be enabled"},
		{"window beside the current threshold", "    open_cron: '0 9 * * *'\n    open_minutes: 60\n", 4, "open_cron", "not both"},
		{"bad zone", "    open_after: 0\n    open_cron: '0 9 * * *'\n    open_minutes: 60\n    tz: Mars/Olympus\n", 7, "tz", "IANA"},
		{"zone without a window", "    tz: Asia/Tokyo\n", 4, "tz", "needs open_cron"},
		{"file-level error", "    max_active: 2\nextra: 1\n", 5, "extra", "unknown key"},
		{"entry error", "    max_active: two\n", 4, "max_active", "whole number"},
	} {
		a.file("version: 1\ncategories:\n  rounds:\n" + c.entry)
		rec, out := e.repoSettings(id)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
		if len(out.Diffs) != 0 || len(out.Errors) == 0 {
			t.Errorf("%s: diffs %+v errors %+v", c.name, out.Diffs, out.Errors)
			continue
		}
		got := out.Errors[0]
		if got.Line != c.line || got.Key != c.key || !strings.Contains(got.Reason, c.part) {
			t.Errorf("%s: got %+v, want line %d key %s %q", c.name, got, c.line, c.key, c.part)
		}
	}
}

func TestMemoRepoSettingsFileAbsentOrWithoutTheCategory(t *testing.T) {
	e, a := repoEnv(t, "")
	id := e.category("app", "rounds")
	a.answer(http.StatusOK, `{"path":".agent-fleet/autorun.yaml","branch":"main","commit":"c0ffee","found":false,"content":""}`)
	rec, out := e.repoSettings(id)
	if rec.Code != http.StatusOK || out.Found || out.Entry || len(out.Diffs) != 0 || out.Commit != "c0ffee" {
		t.Fatalf("absent: %d %+v", rec.Code, out)
	}
	a.file("version: 1\ncategories:\n  other: {}\n")
	if _, out = e.repoSettings(id); !out.Found || out.Entry || len(out.Diffs) != 0 || len(out.Errors) != 0 {
		t.Fatalf("no entry: %+v", out)
	}
	a.file("version: 1\ncategories:\n  rounds:\n    instruction_file: .agent-fleet/rounds.md\n")
	if _, out = e.repoSettings(id); len(out.Warnings) != 1 || out.Warnings[0].Key != "instruction_file" || len(out.Diffs) != 0 {
		t.Fatalf("instruction_file: %+v", out)
	}
}

func TestMemoRepoSettingsRefusals(t *testing.T) {
	e, a := repoEnv(t, "")
	id := e.category("app", "rounds")

	e.mgr.memoRepoSettings = false
	if rec, _ := e.repoSettings(id); rec.Code != http.StatusNotFound {
		t.Errorf("switch off: %d %s", rec.Code, rec.Body.String())
	}
	if len(a.requested()) != 0 {
		t.Errorf("switch off still asked the Agent")
	}
	e.mgr.memoRepoSettings = true

	if rec, _ := e.repoSettings("nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown category: %d", rec.Code)
	}
	// Another member's category is not found.
	rec := e.do("c", "POST", "/api/memo-categories", map[string]any{"repo": "app", "name": "theirs"})
	theirs := decode[struct {
		ID string `json:"id"`
	}](t, rec).ID
	if theirs == "" {
		t.Fatalf("category of c: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := e.repoSettings(theirs); rec.Code != http.StatusNotFound {
		t.Errorf("another member's category: %d", rec.Code)
	}
	if rec, _ := e.repoSettings(e.category("", "loose")); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errCodeMemoQueueNoRepo) {
		t.Errorf("no repo: %d %s", rec.Code, rec.Body.String())
	}

	for _, c := range []struct {
		name, body string
		status     int
		want       int
		part       string
	}{
		{"fetch failed", `{"error":{"code":"fetch_failed","message":"fatal: secret-host.invalid refused"}}`, 502, 502, "fetch_failed"},
		{"too large", `{"error":{"code":"file_too_large","message":".agent-fleet/autorun.yaml is larger than 65536 bytes"}}`, 422, 422, "larger than"},
		{"no working copy", `{"error":{"code":"not_found","message":"no such repo: app"}}`, 404, 404, "no git working copy"},
		{"old Agent", `404 page not found`, 404, 502, "404"},
	} {
		a.answer(c.status, c.body)
		rec, _ := e.repoSettings(id)
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), errCodeMemoQueueRepoUnreadable) || !strings.Contains(rec.Body.String(), c.part) {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret-host") {
			t.Errorf("%s: git's output reached the answer: %s", c.name, rec.Body.String())
		}
	}
}

func TestMemoRepoSettingsNeedsARunningWorkspace(t *testing.T) {
	e, a := repoEnv(t, "stopped")
	id := e.category("app", "rounds")
	if rec, _ := e.repoSettings(id); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "workspace_not_running") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(a.requested()) != 0 {
		t.Errorf("asked a stopped workspace's Agent")
	}
}

// Every key the file may hold is compared, except instruction_file, which is not read yet.
func TestMemoRepoFieldsCoverTheFileKeys(t *testing.T) {
	have := map[string]bool{}
	for _, f := range memoRepoFields {
		have[f.key] = true
	}
	for _, k := range autorunfile.Keys {
		if !have[k.Name] && k.Name != "instruction_file" {
			t.Errorf("file key %s is not compared", k.Name)
		}
		delete(have, k.Name)
	}
	for k := range have {
		t.Errorf("%s is compared but the file cannot hold it", k)
	}
	// Each field names a member of the settings on the wire.
	b, _ := json.Marshal(memoQueueDTO{})
	var wire map[string]any
	_ = json.Unmarshal(b, &wire)
	for _, f := range memoRepoFields {
		if _, ok := wire[f.field]; !ok {
			t.Errorf("%s maps to %q, which the settings do not have", f.key, f.field)
		}
	}
}
