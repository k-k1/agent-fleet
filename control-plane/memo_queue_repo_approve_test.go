package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/autorunfile"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func (e *inboxEnv) queueOf(categoryID string) (store.MemoQueue, bool) {
	e.t.Helper()
	qs, err := e.st.ListMemoQueues(context.Background(), e.members["a"])
	if err != nil {
		e.t.Fatal(err)
	}
	for _, q := range qs {
		if q.CategoryID == categoryID {
			return q, true
		}
	}
	return store.MemoQueue{}, false
}

func (e *inboxEnv) approve(categoryID string, body map[string]any) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("a", "POST", "/api/memo-queues/"+categoryID+"/repo-settings/approve", body)
}

func approveBody(out memoRepoSettingsWire, keys ...string) map[string]any {
	if keys == nil {
		for _, d := range out.Diffs {
			keys = append(keys, d.Key)
		}
	}
	return map[string]any{"commit": out.Commit, "approvalTag": out.ApprovalTag, "keys": keys}
}

const approveFile = `version: 1
categories:
  rounds:
    spend_cap_usd: 30
    max_active: 3
    batch: 2
    model: claude-opus-5-5
    enabled: false
`

func approveEnv(t *testing.T) (*inboxEnv, *fakeAutorunAgent, string) {
	e, a := repoEnv(t, "")
	id := e.category("app", "rounds")
	put := map[string]any{"instruction": "implement it", "spendCapUsd": 60, "maxActive": 2, "model": "claude-opus-5-5",
		"enabled": true, "acceptOwnSessions": true}
	if rec := e.do("a", "PUT", "/api/memo-queues/"+id, put); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	a.file(approveFile)
	return e, a, id
}

// Approving every difference saves the file's values, keeps what only the Console sets, and
// records the file's commit; the difference is gone afterwards and a later Console save keeps the
// record.
func TestMemoRepoApproveAll(t *testing.T) {
	e, _, id := approveEnv(t)
	_, out := e.repoSettings(id)
	if len(out.Diffs) != 3 || out.ApprovalTag == "" || out.ApprovedCommit != "" {
		t.Fatalf("before: %+v", out)
	}
	rec := e.approve(id, approveBody(out))
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if dto := decode[memoQueueDTO](t, rec); dto.SpendCapUSD != 30 || dto.MaxActive != 3 || dto.Batch != 2 {
		t.Fatalf("answer %+v", dto)
	}
	q, _ := e.queueOf(id)
	if q.SpendCapUSD != 30 || q.MaxActive != 3 || q.Batch != 2 || q.Instruction != "implement it" ||
		!q.Enabled || !q.AcceptOwnSessions || q.RepoCommit != testCommit || q.RepoApprovedAt == "" {
		t.Fatalf("saved %+v", q)
	}
	_, after := e.repoSettings(id)
	if len(after.Diffs) != 0 || after.ApprovedCommit != testCommit || after.ApprovedAt != q.RepoApprovedAt {
		t.Fatalf("after: %+v", after)
	}
	if rec := e.do("a", "PUT", "/api/memo-queues/"+id, map[string]any{"instruction": "other", "enabled": true}); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if q, _ := e.queueOf(id); q.RepoCommit != testCommit || q.Instruction != "other" {
		t.Fatalf("a Console save dropped the approval: %+v", q)
	}
}

// Approving one difference applies that one value only.
func TestMemoRepoApproveSome(t *testing.T) {
	e, _, id := approveEnv(t)
	_, out := e.repoSettings(id)
	if rec := e.approve(id, approveBody(out, "batch")); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	q, _ := e.queueOf(id)
	if q.Batch != 2 || q.SpendCapUSD != 60 || q.MaxActive != 2 || q.RepoCommit != testCommit {
		t.Fatalf("saved %+v", q)
	}
	_, after := e.repoSettings(id)
	if d := diffByKey(after.Diffs); len(after.Diffs) != 2 || d["spend_cap_usd"].Key == "" || d["max_active"].Key == "" {
		t.Fatalf("left: %+v", after.Diffs)
	}
}

// A category without settings gets them from the approval, off like any new queue.
func TestMemoRepoApproveCreatesTheSettings(t *testing.T) {
	e, a := repoEnv(t, "")
	id := e.category("app", "fresh")
	a.file("version: 1\ncategories:\n  fresh:\n    instruction: do it\n    agent_kind: codex\n")
	_, out := e.repoSettings(id)
	if out.Queue || out.ApprovalTag == "" || len(out.Diffs) != 2 {
		t.Fatalf("before: %+v", out)
	}
	if rec := e.approve(id, approveBody(out)); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	q, ok := e.queueOf(id)
	if !ok || q.Instruction != "do it" || q.AgentKind != "codex" || q.Enabled || q.RepoCommit != testCommit {
		t.Fatalf("saved %v %+v", ok, q)
	}
}

// The settings that decide when the queue runs by itself are approved as one group, implied
// changes included.
func TestMemoRepoApproveFiringGroup(t *testing.T) {
	e, a := repoEnv(t, "")
	id := e.category("app", "win")
	if rec := e.do("a", "PUT", "/api/memo-queues/"+id, map[string]any{"instruction": "x", "openCron": "0 9 * * *",
		"openMinutes": 60, "tz": "Asia/Tokyo"}); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	a.file("version: 1\ncategories:\n  win:\n    open_cron: ''\n    open_after: 5\n    batch: 3\n")
	_, out := e.repoSettings(id)
	if len(out.Diffs) != 5 {
		t.Fatalf("diffs %+v", out.Diffs)
	}
	if rec := e.approve(id, approveBody(out, "open_after", "open_cron")); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "together") {
		t.Fatalf("part of the group: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.approve(id, approveBody(out, "open_after", "open_cron", "open_minutes", "tz")); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	q, _ := e.queueOf(id)
	if q.OpenCron != "" || q.OpenMinutes != 0 || q.TZ != "" || q.OpenAfter != 5 || q.Batch != 1 {
		t.Fatalf("saved %+v", q)
	}
}

// A Console save between the check and the approval makes the approval stale, also within the
// same second (updatedAt would not tell).
func TestMemoRepoApproveAfterAConsoleSave(t *testing.T) {
	e, _, id := approveEnv(t)
	_, out := e.repoSettings(id)
	if rec := e.do("a", "PUT", "/api/memo-queues/"+id, map[string]any{"instruction": "implement it", "spendCapUsd": 70,
		"maxActive": 2, "model": "claude-opus-5-5", "enabled": true, "acceptOwnSessions": true}); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.approve(id, approveBody(out, "batch")); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errCodeMemoQueueRepoStale) {
		t.Fatalf("approve after a save: %d %s", rec.Code, rec.Body.String())
	}
	if q, _ := e.queueOf(id); q.Batch != 1 || q.SpendCapUSD != 70 {
		t.Fatalf("saved %+v", q)
	}
}

// A rename between the check and the approval would make the file's entry for the new name
// apply: the approval is stale instead, with or without settings.
func TestMemoRepoApproveAfterARename(t *testing.T) {
	for _, withQueue := range []bool{true, false} {
		e, a := repoEnv(t, "")
		id := e.category("app", "rounds")
		if withQueue {
			if rec := e.do("a", "PUT", "/api/memo-queues/"+id, map[string]any{"instruction": "x"}); rec.Code != http.StatusOK {
				t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
			}
		}
		a.file("version: 1\ncategories:\n  rounds:\n    instruction: x\n    batch: 2\n  other:\n    instruction: x\n    batch: 5\n")
		_, out := e.repoSettings(id)
		if d := diffByKey(out.Diffs)["batch"]; string(d.File) != "2" {
			t.Fatalf("queue=%v: diffs %+v", withQueue, out.Diffs)
		}
		if rec := e.do("a", "PATCH", "/api/memo-categories/"+id, map[string]any{"name": "other"}); rec.Code != http.StatusOK {
			t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.approve(id, approveBody(out, "batch")); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errCodeMemoQueueRepoStale) {
			t.Fatalf("queue=%v: approve after a rename: %d %s", withQueue, rec.Code, rec.Body.String())
		}
		if q, ok := e.queueOf(id); ok != withQueue || q.Batch == 5 || q.RepoCommit != "" {
			t.Fatalf("queue=%v: saved %v %+v", withQueue, ok, q)
		}
	}
}

// Turning memo queues off while the workspace reads the file leaves nothing written.
func TestMemoRepoApproveTenantTurnedOffDuringTheRead(t *testing.T) {
	e, a, id := approveEnv(t)
	_, out := e.repoSettings(id)
	a.mu.Lock()
	a.onServe = func() { e.memoQueuesOn(false) }
	a.mu.Unlock()
	if rec := e.approve(id, approveBody(out)); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errCodeMemoQueuesDisabled) {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if q, _ := e.queueOf(id); q.SpendCapUSD != 60 || q.RepoCommit != "" {
		t.Fatalf("saved %+v", q)
	}
}

// A tidy destination whose settings were removed does not block approving an unrelated setting,
// and stays: a tidy still offers it as a category (memoTidyDestinations), here one of another
// repository that only the stored list brings in.
func TestMemoRepoApproveKeepsAStaleTidyDestination(t *testing.T) {
	e, a, id := approveEnv(t)
	dest := e.category("lib", "dest")
	if rec := e.do("a", "PUT", "/api/memo-queues/"+dest, map[string]any{"instruction": "y"}); rec.Code != http.StatusOK {
		t.Fatalf("put dest: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("a", "PUT", "/api/memo-queues/"+id, map[string]any{"instruction": "implement it", "spendCapUsd": 60, "maxActive": 2,
		"model": "claude-opus-5-5", "enabled": true, "acceptOwnSessions": true, "tidy": true, "tidyDestinations": []string{dest}}); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("a", "DELETE", "/api/memo-queues/"+dest, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete dest: %d", rec.Code)
	}
	a.file(approveFile)
	_, out := e.repoSettings(id)
	if rec := e.approve(id, approveBody(out, "batch")); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if q, _ := e.queueOf(id); q.Batch != 2 || !q.Tidy || len(q.TidyDestinations) != 1 || q.TidyDestinations[0] != dest {
		t.Fatalf("saved %+v", q)
	}
	// The Console's PUT still checks what it is sent.
	if rec := e.do("a", "PUT", "/api/memo-queues/"+id, map[string]any{"instruction": "x", "tidy": true, "tidyDestinations": []string{dest}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT with a stale destination: %d %s", rec.Code, rec.Body.String())
	}
}

// Another member cannot approve for a member's category, with a valid commit, tag and keys, and
// the workspace is not asked.
func TestMemoRepoApproveIsTheMembersOwn(t *testing.T) {
	e, a, id := approveEnv(t)
	_, out := e.repoSettings(id)
	before := len(a.requested())
	if rec := e.do("b", "POST", "/api/memo-queues/"+id+"/repo-settings/approve", approveBody(out)); rec.Code != http.StatusNotFound {
		t.Fatalf("another member: %d %s", rec.Code, rec.Body.String())
	}
	if got := a.requested(); len(got) != before {
		t.Fatalf("the workspace was asked: %v", got[before:])
	}
	if q, _ := e.queueOf(id); q.SpendCapUSD != 60 || q.RepoCommit != "" {
		t.Fatalf("saved %+v", q)
	}
}

func TestMemoRepoApproveRefusals(t *testing.T) {
	e, a, id := approveEnv(t)
	_, out := e.repoSettings(id)
	for _, c := range []struct {
		name string
		body map[string]any
		code int
		want string
	}{
		{"no keys", map[string]any{"commit": out.Commit, "approvalTag": out.ApprovalTag, "keys": []string{}}, http.StatusBadRequest, "bad_request"},
		{"unknown key", approveBody(out, "enabled"), http.StatusBadRequest, "bad_request"},
		{"twice", approveBody(out, "batch", "batch"), http.StatusBadRequest, "bad_request"},
		{"bad commit", map[string]any{"commit": "HEAD", "keys": []string{"batch"}}, http.StatusBadRequest, "bad_request"},
		{"other commit", map[string]any{"commit": strings.Repeat("a", 40), "approvalTag": out.ApprovalTag, "keys": []string{"batch"}},
			http.StatusConflict, errCodeMemoQueueRepoStale},
		{"settings moved", map[string]any{"commit": out.Commit, "approvalTag": "not-the-tag", "keys": []string{"batch"}},
			http.StatusConflict, errCodeMemoQueueRepoStale},
		// The model is equal in the file: there is nothing to approve.
		{"no difference", approveBody(out, "model"), http.StatusConflict, errCodeMemoQueueRepoStale},
	} {
		rec := e.approve(id, c.body)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	// A file that became invalid is not applied.
	a.file("version: 1\ncategories:\n  rounds:\n    max_active: 999\n")
	if rec := e.approve(id, approveBody(out, "batch")); rec.Code != http.StatusConflict {
		t.Errorf("invalid file: %d %s", rec.Code, rec.Body.String())
	}
	a.file(approveFile)
	e.mgr.memoRepoSettings = false
	if rec := e.approve(id, approveBody(out, "batch")); rec.Code != http.StatusNotFound {
		t.Errorf("switch off: %d %s", rec.Code, rec.Body.String())
	}
	e.mgr.memoRepoSettings = true
	e.memoQueuesOn(false)
	if rec := e.approve(id, approveBody(out, "batch")); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errCodeMemoQueuesDisabled) {
		t.Errorf("tenant off: %d %s", rec.Code, rec.Body.String())
	}
	if q, _ := e.queueOf(id); q.Batch != 1 || q.SpendCapUSD != 60 || q.RepoCommit != "" {
		t.Fatalf("a refusal saved something: %+v", q)
	}
}

// The export parses back to the member's settings with no difference, for every way a queue can
// run, and leaves out what only the Console sets and what looks like a credential.
func TestMemoRepoExportRoundTrips(t *testing.T) {
	base := store.MemoQueue{Instruction: "Use the skill.\nThen stop.\n", AgentKind: "codex", Model: "gpt-6.1-terra", Effort: "high",
		SpendCapUSD: 12.5, Worktree: true, ContinuationMax: 3, MaxActive: 2, Batch: 4, MaxTaskHours: 8, MaxFiresPerDay: 30}
	window := base
	window.OpenCron, window.OpenMinutes, window.TZ = "0 9 * * 1-5", 90, "Asia/Tokyo"
	threshold := base
	threshold.OpenAfter, threshold.Worktree, threshold.Tidy, threshold.SpendCapUSD = 4, false, true, 0
	numeric := base
	numeric.Model, numeric.Instruction = "123", "true"
	for name, q := range map[string]store.MemoQueue{"always": base, "window": window, "threshold": threshold, "numeric strings": numeric} {
		vals, omitted := memoRepoExportValues(q)
		if len(omitted) != 0 {
			t.Errorf("%s: omitted %+v", name, omitted)
		}
		b, err := autorunfile.Format("rounds", vals)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		f := autorunfile.Parse(b)
		e := f.Categories["rounds"]
		if len(f.Errors) != 0 || e == nil || len(e.Errors) != 0 || len(e.Warnings) != 0 {
			t.Fatalf("%s: %s\nfile %+v entry %+v", name, b, f.Errors, e)
		}
		res := memoRepoCompare(q, true, e)
		if len(res.Errors) != 0 || len(res.Warnings) != 0 || len(res.Diffs) != 0 {
			t.Errorf("%s: %s\nerrors %+v warnings %+v diffs %+v", name, b, res.Errors, res.Warnings, res.Diffs)
		}
		for _, k := range []string{"enabled", "accept_own_sessions", "tidy_destinations", "instruction_file"} {
			if e.Has(k) || strings.Contains(string(b), "\n    "+k+":") {
				t.Errorf("%s: wrote %s", name, k)
			}
		}
	}
	secret := base
	secret.Instruction = "token ghp_" + strings.Repeat("Z", 36)
	vals, omitted := memoRepoExportValues(secret)
	if len(omitted) != 1 || omitted[0].Key != "instruction" {
		t.Fatalf("omitted %+v", omitted)
	}
	for _, v := range vals {
		if v.Key == "instruction" {
			t.Fatalf("exported the credential")
		}
	}
}

func TestMemoRepoExportRoute(t *testing.T) {
	e, _, id := approveEnv(t)
	rec := e.do("a", "GET", "/api/memo-queues/"+id+"/repo-settings/export", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	out := decode[memoRepoExportWire](t, rec)
	if out.Path != autorunfile.Path || out.Category != "rounds" || out.Omitted == nil ||
		!strings.Contains(out.YAML, "version: 1\n") || !strings.Contains(out.YAML, "  rounds:\n") || !strings.Contains(out.YAML, "spend_cap_usd: 60\n") {
		t.Fatalf("export %+v", out)
	}
	if f := autorunfile.Parse([]byte(out.YAML)); !f.Usable("rounds") {
		t.Fatalf("unusable: %+v", f.Errors)
	}
	none := e.category("app", "none")
	if rec := e.do("a", "GET", "/api/memo-queues/"+none+"/repo-settings/export", nil); rec.Code != http.StatusNotFound {
		t.Errorf("no settings: %d", rec.Code)
	}
	if rec := e.do("b", "GET", "/api/memo-queues/"+id+"/repo-settings/export", nil); rec.Code != http.StatusNotFound {
		t.Errorf("another member: %d", rec.Code)
	}
	// A token pasted into the category's name is neither written nor repeated.
	token := "ghp_" + strings.Repeat("Q", 36)
	if rec := e.do("a", "PATCH", "/api/memo-categories/"+id, map[string]any{"name": token}); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("a", "GET", "/api/memo-queues/"+id+"/repo-settings/export", nil); rec.Code != http.StatusUnprocessableEntity ||
		strings.Contains(rec.Body.String(), "QQQQ") || !strings.Contains(rec.Body.String(), errCodeMemoQueueRepoExportName) {
		t.Errorf("credential name: %d %s", rec.Code, rec.Body.String())
	}
	e.mgr.memoRepoSettings = false
	if rec := e.do("a", "GET", "/api/memo-queues/"+id+"/repo-settings/export", nil); rec.Code != http.StatusNotFound {
		t.Errorf("switch off: %d", rec.Code)
	}
}

// The list says whether the routes are on, so the Console offers them only then.
func TestMemoQueuesListSaysRepoSettings(t *testing.T) {
	e, _ := repoEnv(t, "")
	for _, on := range []bool{true, false} {
		e.mgr.memoRepoSettings = on
		rec := e.do("a", "GET", "/api/memo-queues", nil)
		if out := decode[memoQueuesWire](t, rec); rec.Code != http.StatusOK || out.RepoSettings != on {
			t.Errorf("on=%v: %d %s", on, rec.Code, rec.Body.String())
		}
	}
}
