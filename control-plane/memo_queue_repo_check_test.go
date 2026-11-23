package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// putRounds gives category rounds of repository app enabled settings with instruction "old".
func putRounds(e *inboxEnv) string {
	e.t.Helper()
	id := e.category("app", "rounds")
	put := map[string]any{"instruction": "old", "spendCapUsd": 60, "maxActive": 2, "enabled": true}
	if rec := e.do("a", "PUT", "/api/memo-queues/"+id, put); rec.Code != http.StatusOK {
		e.t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	return id
}

func (e *inboxEnv) queueDTO(categoryID string) memoQueueDTO {
	e.t.Helper()
	for _, q := range decode[memoQueuesWire](e.t, e.do("a", "GET", "/api/memo-queues", nil)).Queues {
		if q.CategoryID == categoryID {
			return q
		}
	}
	e.t.Fatalf("queue %s not listed", categoryID)
	return memoQueueDTO{}
}

// instruction_file is read from the settings file's commit and compared, and approved, as the
// instruction (trimmed, as the settings PUT saves one).
func TestMemoRepoInstructionFile(t *testing.T) {
	e, a := repoEnv(t, "")
	id := putRounds(e)
	a.file("version: 1\ncategories:\n  rounds:\n    max_active: 2\n    instruction_file: .agent-fleet/rounds.md\n")
	a.instruction(".agent-fleet/rounds.md", "new text\n")
	rec, out := e.repoSettings(id)
	if rec.Code != http.StatusOK || len(out.Errors) != 0 || len(out.Warnings) != 0 || len(out.Diffs) != 1 {
		t.Fatalf("%d %+v", rec.Code, out)
	}
	if d := out.Diffs[0]; d.Key != "instruction" || d.Line != 5 || string(d.Current) != `"old"` || string(d.File) != `"new text"` {
		t.Fatalf("diff %+v", d)
	}
	want := "GET /repos/app/autorun-file/instruction"
	if got := a.requested(); len(got) != 2 || got[1] != want {
		t.Fatalf("Agent calls %v", got)
	}
	if rec := e.approve(id, approveBody(out)); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if q, _ := e.queueOf(id); q.Instruction != "new text" || q.RepoCommit != testCommit {
		t.Fatalf("saved %+v", q)
	}
	// The approval read the instruction from the same commit again.
	if got := a.requested(); len(got) != 4 || got[3] != want {
		t.Fatalf("Agent calls %v", got)
	}
	// A file that changed between the look and the approval is not what the member saw.
	a.instruction(".agent-fleet/rounds.md", "newer\n")
	_, out = e.repoSettings(id)
	a.instruction(".agent-fleet/rounds.md", "newest\n")
	if rec := e.approve(id, approveBody(out)); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errCodeMemoQueueRepoStale) {
		t.Fatalf("changed file: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMemoRepoInstructionFileRefusals(t *testing.T) {
	e, a := repoEnv(t, "")
	id := putRounds(e)
	a.file("version: 1\ncategories:\n  rounds:\n    instruction_file: run.md\n")
	for name, content := range map[string]string{
		"not regular": "!file_not_regular",
		"too large":   "!file_too_large",
		"not UTF-8":   "!file_not_utf8",
		"over limit":  strings.Repeat("x", memoQueueInstructionMax+1),
		"credential":  "use ghp_" + strings.Repeat("A", 36),
	} {
		a.instruction("run.md", content)
		rec, out := e.repoSettings(id)
		if rec.Code != http.StatusOK || len(out.Errors) != 1 || out.Errors[0].Key != "instruction_file" || out.Errors[0].Line != 4 ||
			len(out.Diffs) != 0 || strings.Contains(rec.Body.String(), "AAAA") {
			t.Errorf("%s: %d %+v", name, rec.Code, out)
		}
	}
	a.mu.Lock()
	delete(a.instr, "run.md")
	a.mu.Unlock()
	if _, out := e.repoSettings(id); len(out.Errors) != 1 || !strings.Contains(out.Errors[0].Reason, "does not hold") {
		t.Fatalf("missing: %+v", out)
	}
	// The workspace cannot answer: nothing is compared.
	a.instruction("run.md", "!commit_missing")
	if rec, _ := e.repoSettings(id); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), errCodeMemoQueueRepoUnreadable) {
		t.Fatalf("commit missing: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.approve(id, map[string]any{"commit": testCommit, "approvalTag": "x", "keys": []string{"instruction"}}); rec.Code != http.StatusBadGateway {
		t.Fatalf("approve, commit missing: %d %s", rec.Code, rec.Body.String())
	}
}

// Categories that share one entry through an alias each read the file; reading it for one does
// not change the entry the next one sees.
func TestMemoRepoInstructionFileSharedEntry(t *testing.T) {
	e, a := repoEnv(t, "")
	id := putRounds(e)
	a.file("version: 1\ncategories:\n  other: &s\n    instruction_file: run.md\n  rounds: *s\n")
	a.instruction("run.md", "shared\n")
	for i := 0; i < 2; i++ {
		if _, out := e.repoSettings(id); len(out.Errors) != 0 || len(out.Diffs) != 1 || string(out.Diffs[0].File) != `"shared"` {
			t.Fatalf("read %d: %+v", i, out)
		}
	}
}

// A check is kept on the queue and shown with it while the settings and the name are the ones it
// was compared with.
func TestMemoRepoCheckRecorded(t *testing.T) {
	e, a := repoEnv(t, "")
	id := putRounds(e)
	if q := e.queueDTO(id); q.RepoCheck != nil {
		t.Fatalf("never read: %+v", q.RepoCheck)
	}
	a.file("version: 1\ncategories:\n  rounds:\n    max_active: 3\n    spend_cap_usd: 30\n")
	_, out := e.repoSettings(id)
	rc := e.queueDTO(id).RepoCheck
	if rc == nil || rc.State != memoRepoCheckDiffers || rc.Diffs != 2 || rc.Commit != testCommit || rc.CheckedAt == "" {
		t.Fatalf("differs: %+v", rc)
	}
	// Approving one of the two changes the settings: the read no longer applies.
	if rec := e.approve(id, approveBody(out, "spend_cap_usd")); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if rc := e.queueDTO(id).RepoCheck; rc != nil {
		t.Fatalf("after approval: %+v", rc)
	}
	e.repoSettings(id)
	if rc := e.queueDTO(id).RepoCheck; rc == nil || rc.State != memoRepoCheckDiffers || rc.Diffs != 1 {
		t.Fatalf("read again: %+v", rc)
	}
	// A settings change in the Console, and a rename, each leave the read behind.
	put := map[string]any{"instruction": "old", "spendCapUsd": 30, "maxActive": 1, "enabled": true}
	if rec := e.do("a", "PUT", "/api/memo-queues/"+id, put); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if rc := e.queueDTO(id).RepoCheck; rc != nil {
		t.Fatalf("after a settings change: %+v", rc)
	}
	for _, tc := range []struct{ file, state string }{
		{"version: 1\ncategories:\n  rounds:\n    max_active: 1\n", memoRepoCheckSame},
		{"version: 1\ncategories:\n  other: {}\n", memoRepoCheckAbsent},
		{"version: 2\n", memoRepoCheckInvalid},
	} {
		a.file(tc.file)
		e.repoSettings(id)
		if rc := e.queueDTO(id).RepoCheck; rc == nil || rc.State != tc.state || rc.Diffs != 0 {
			t.Errorf("%q: %+v", tc.file, rc)
		}
	}
	if rec := e.do("a", "PATCH", "/api/memo-categories/"+id, map[string]any{"name": "renamed"}); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if rc := e.queueDTO(id).RepoCheck; rc != nil {
		t.Fatalf("after a rename: %+v", rc)
	}
}

func TestMemoRepoRecheck(t *testing.T) {
	e, a := repoEnv(t, "")
	id := putRounds(e)
	off := e.category("app", "off")
	if rec := e.do("a", "PUT", "/api/memo-queues/"+off, map[string]any{"instruction": "x"}); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	a.file("version: 1\ncategories:\n  rounds:\n    max_active: 3\n  off:\n    max_active: 3\n")
	now := time.Now()
	rc := &memoRepoRechecker{mgr: e.mgr, every: time.Hour, now: func() time.Time { return now }}
	rc.tick(context.Background())
	if got := a.requested(); len(got) != 1 {
		t.Fatalf("Agent calls %v", got)
	}
	if c := e.queueDTO(id).RepoCheck; c == nil || c.State != memoRepoCheckDiffers || c.Diffs != 1 {
		t.Fatalf("enabled queue: %+v", c)
	}
	// A queue that is not enabled is read when the member asks, not on the interval.
	if c := e.queueDTO(off).RepoCheck; c != nil {
		t.Fatalf("disabled queue: %+v", c)
	}
	// Read less than half an interval ago: not read again.
	now = now.Add(29 * time.Minute)
	rc.tick(context.Background())
	if got := a.requested(); len(got) != 1 {
		t.Fatalf("fresh read repeated: %v", got)
	}
	now = now.Add(2 * time.Minute)
	a.file("version: 1\ncategories:\n  rounds:\n    max_active: 2\n")
	rc.tick(context.Background())
	if c := e.queueDTO(id).RepoCheck; len(a.requested()) != 2 || c == nil || c.State != memoRepoCheckSame {
		t.Fatalf("second read: %v %+v", a.requested(), c)
	}
	// An unreadable file leaves the last read as it was.
	now = now.Add(time.Hour)
	a.answer(http.StatusBadGateway, `{"error":{"code":"fetch_failed","message":"x"}}`)
	rc.tick(context.Background())
	if c := e.queueDTO(id).RepoCheck; len(a.requested()) != 3 || c == nil || c.State != memoRepoCheckSame {
		t.Fatalf("failed read: %v %+v", a.requested(), c)
	}
}

// A workspace that is not running is never started, nor asked.
func TestMemoRepoRecheckLeavesStoppedWorkspaces(t *testing.T) {
	e, a := repoEnv(t, "stopped")
	putRounds(e)
	a.file("version: 1\ncategories:\n  rounds:\n    max_active: 3\n")
	(&memoRepoRechecker{mgr: e.mgr, every: time.Hour, now: time.Now}).tick(context.Background())
	if got := a.requested(); len(got) != 0 {
		t.Fatalf("Agent calls %v", got)
	}
}
