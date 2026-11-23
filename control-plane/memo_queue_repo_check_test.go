package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
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
	asked := testCommit + " .agent-fleet/rounds.md"
	if got := a.instrAsked; len(got) != 1 || got[0] != asked {
		t.Fatalf("instruction reads %v, want the settings file's commit", got)
	}
	if rec := e.approve(id, approveBody(out)); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if q, _ := e.queueOf(id); q.Instruction != "new text" || q.RepoCommit != testCommit {
		t.Fatalf("saved %+v", q)
	}
	// The approval read the instruction from the same commit again.
	if got := a.requested(); len(got) != 4 || got[3] != want || len(a.instrAsked) != 2 || a.instrAsked[1] != asked {
		t.Fatalf("Agent calls %v, instruction reads %v", got, a.instrAsked)
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
		"NUL":         "run\x00it",
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
	// The workspace cannot answer, or answers about another commit, or lost the working copy
	// since the settings file was read: nothing is compared.
	for _, code := range []string{"!commit_missing", "!not_found", "other commit"} {
		a.instruction("run.md", code)
		a.mu.Lock()
		a.instrCommit = map[bool]string{true: strings.Repeat("d", 40)}[code == "other commit"]
		a.mu.Unlock()
		if rec, _ := e.repoSettings(id); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), errCodeMemoQueueRepoUnreadable) {
			t.Fatalf("%s: %d %s", code, rec.Code, rec.Body.String())
		}
	}
	a.instruction("run.md", "!commit_missing")
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

// A read that began earlier never replaces one that began later: the loop's read waits on the
// workspace while the member checks the file, and lands last.
func TestMemoRepoRecheckKeepsTheLaterRead(t *testing.T) {
	e, a := repoEnv(t, "")
	id := putRounds(e)
	differs := "version: 1\ncategories:\n  rounds:\n    max_active: 3\n"
	a.file(differs)
	// Not a sync.Once: the member's read comes back through this hook while the first call is
	// still inside it.
	var first atomic.Bool
	a.onServe = func() {
		if first.CompareAndSwap(false, true) {
			a.file("version: 1\ncategories:\n  rounds:\n    max_active: 2\n")
			e.repoSettings(id)
			a.file(differs)
		}
	}
	(&memoRepoRechecker{mgr: e.mgr, every: time.Hour, now: time.Now}).tick(context.Background())
	if got := a.requested(); len(got) != 2 {
		t.Fatalf("Agent calls %v", got)
	}
	if c := e.queueDTO(id).RepoCheck; c == nil || c.State != memoRepoCheckSame {
		t.Fatalf("the member's later read was replaced: %+v", c)
	}
}

// A read computed against settings that changed before it was kept is not kept: the loop took
// the queue's settings, then the member changed them and checked the file, then the loop's read
// began. The loop's read begins later, so only the basis keeps it out.
func TestMemoRepoRecheckDropsAReadOfOldSettings(t *testing.T) {
	e, a := repoEnv(t, "")
	app := putRounds(e) // maxActive 2
	a.file("version: 1\ncategories:\n  rounds:\n    max_active: 3\n")
	calls := 0
	now := func() time.Time {
		// The first call dates the queue snapshot's freshness, the second the start of its read.
		if calls++; calls == 2 {
			put := map[string]any{"instruction": "old", "spendCapUsd": 60, "maxActive": 3, "enabled": true}
			if rec := e.do("a", "PUT", "/api/memo-queues/"+app, put); rec.Code != http.StatusOK {
				t.Errorf("put: %d %s", rec.Code, rec.Body.String())
			}
			e.repoSettings(app)
		}
		return time.Now()
	}
	(&memoRepoRechecker{mgr: e.mgr, every: time.Hour, now: now}).tick(context.Background())
	if got := a.requested(); calls != 2 || len(got) != 2 {
		t.Fatalf("now called %d times, Agent calls %v", calls, got)
	}
	if c := e.queueDTO(app).RepoCheck; c == nil || c.State != memoRepoCheckSame {
		t.Fatalf("the member's read was replaced by one of the old settings: %+v", c)
	}
}

// A workspace whose runtime is not cached is read through a runtime built for the read and never
// cached, whatever the row's state column says: the runtime's own state decides. A stopped one's
// Agent is not asked.
func TestMemoRepoRecheckLeavesTheRuntimeCacheAlone(t *testing.T) {
	e, a := repoEnv(t, "")
	id := putRounds(e)
	a.file("version: 1\ncategories:\n  rounds:\n    max_active: 3\n")
	if err := e.st.SetWorkspaceState(context.Background(), "ws-a", "stopped"); err != nil {
		t.Fatal(err)
	}
	e.mgr.mu.Lock()
	delete(e.mgr.rts, e.members["a"])
	e.mgr.mu.Unlock()
	live := &stubRuntime{endpoint: a.srv.URL, token: "tok", state: "stopped"}
	e.mgr.rtFactory = stubFactory{rt: live}
	rc := &memoRepoRechecker{mgr: e.mgr, every: time.Hour, now: time.Now}

	rc.tick(context.Background())
	if got := a.requested(); len(got) != 0 {
		t.Fatalf("a stopped workspace's Agent was asked: %v", got)
	}
	live.state = ""
	rc.tick(context.Background())
	if c := e.queueDTO(id).RepoCheck; len(a.requested()) != 1 || c == nil || c.State != memoRepoCheckDiffers {
		t.Fatalf("running, row says stopped: %v %+v", a.requested(), c)
	}
	if _, ok := e.mgr.cachedRTFor(e.members["a"]); ok {
		t.Fatal("the loop cached a runtime")
	}
}

// Nothing is kept once memo queues are off, however late the switch turns.
func TestMemoRepoRecordWaitsForTheSwitch(t *testing.T) {
	e, a := repoEnv(t, "")
	id := putRounds(e)
	a.file("version: 1\ncategories:\n  rounds:\n    max_active: 3\n")
	q, _ := e.queueOf(id)
	c, _, _ := e.st.GetCategory(context.Background(), id)
	rc := memoRepoCheckFrom(c, q, &memoRepoSettingsWire{Found: true, Entry: true, Commit: testCommit}, time.Now())
	e.memoQueuesOn(false)
	if err := e.mgr.memoRepoRecord(context.Background(), e.members["a"], rc, id); err != nil {
		t.Fatal(err)
	}
	if q, _ := e.queueOf(id); q.RepoCheck.State != "" {
		t.Fatalf("kept with the switch off: %+v", q.RepoCheck)
	}
	e.memoQueuesOn(true)
	if err := e.mgr.memoRepoRecord(context.Background(), e.members["a"], rc, id); err != nil {
		t.Fatal(err)
	}
	if q, _ := e.queueOf(id); q.RepoCheck.State != memoRepoCheckSame {
		t.Fatalf("not kept with the switch on: %+v", q.RepoCheck)
	}
}

// A path the parser refuses is an error on its line, and nothing is asked of the Agent for it.
func TestMemoRepoInstructionFileControlCharacter(t *testing.T) {
	e, a := repoEnv(t, "")
	id := putRounds(e)
	a.file("version: 1\ncategories:\n  rounds:\n    instruction_file: \"a\\0b.md\"\n")
	rec, out := e.repoSettings(id)
	if rec.Code != http.StatusOK || len(out.Errors) != 1 || out.Errors[0].Key != "instruction_file" || out.Errors[0].Line != 4 {
		t.Fatalf("%d %+v", rec.Code, out)
	}
	if len(a.instrAsked) != 0 {
		t.Fatalf("asked the Agent: %v", a.instrAsked)
	}
}

// Turned off while a read waits on the workspace, the tenant's switch stops the next read and
// every write.
func TestMemoRepoRecheckStopsWhenTheSwitchGoesOff(t *testing.T) {
	e, a := repoEnv(t, "")
	app := putRounds(e)
	lib := e.category("lib", "rounds")
	if rec := e.do("a", "PUT", "/api/memo-queues/"+lib, map[string]any{"instruction": "x", "enabled": true}); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	a.file("version: 1\ncategories:\n  rounds:\n    max_active: 3\n")
	a.onServe = func() { e.memoQueuesOn(false) }
	(&memoRepoRechecker{mgr: e.mgr, every: time.Hour, now: time.Now}).tick(context.Background())
	if got := a.requested(); len(got) != 1 {
		t.Fatalf("Agent calls %v", got)
	}
	for _, id := range []string{app, lib} {
		if q, _ := e.queueOf(id); q.RepoCheck.State != "" {
			t.Fatalf("recorded with the switch off: %+v", q.RepoCheck)
		}
	}
}

// A member who has never had a workspace gets none from the loop.
func TestMemoRepoRecheckCreatesNoWorkspace(t *testing.T) {
	e := newInboxEnv(t)
	e.memoQueuesOn(true)
	e.mgr.memoRepoSettings = true
	putRounds(e)
	(&memoRepoRechecker{mgr: e.mgr, every: time.Hour, now: time.Now}).tick(context.Background())
	if _, ok, err := e.st.GetWorkspaceByMembership(context.Background(), e.members["a"]); err != nil || ok {
		t.Fatalf("workspace created: %v %v", ok, err)
	}
}

// With repository settings turned off, a read kept from before is not shown.
func TestMemoRepoCheckHiddenWhenTurnedOff(t *testing.T) {
	e, a := repoEnv(t, "")
	id := putRounds(e)
	a.file("version: 1\ncategories:\n  rounds:\n    max_active: 3\n")
	e.repoSettings(id)
	if e.queueDTO(id).RepoCheck == nil {
		t.Fatal("no read recorded")
	}
	e.mgr.memoRepoSettings = false
	if c := e.queueDTO(id).RepoCheck; c != nil {
		t.Fatalf("shown with the switch off: %+v", c)
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

// memberQueue gives member who an enabled queue on category rounds of repository repo.
func (e *inboxEnv) memberQueue(who, repo string) string {
	e.t.Helper()
	rec := e.do(who, "POST", "/api/memo-categories", map[string]any{"repo": repo, "name": "rounds"})
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		e.t.Fatalf("category: %d %s", rec.Code, rec.Body.String())
	}
	id := decode[memoCategoryDTO](e.t, rec).ID
	put := map[string]any{"instruction": "old", "spendCapUsd": 60, "maxActive": 2, "enabled": true}
	if rec := e.do(who, "PUT", "/api/memo-queues/"+id, put); rec.Code != http.StatusOK {
		e.t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	return id
}

func (e *inboxEnv) memberRepoCheck(who, categoryID string) store.MemoRepoCheck {
	e.t.Helper()
	qs, err := e.st.ListMemoQueues(context.Background(), e.members[who])
	if err != nil {
		e.t.Fatal(err)
	}
	for _, q := range qs {
		if q.CategoryID == categoryID {
			return q.RepoCheck
		}
	}
	return store.MemoRepoCheck{}
}

// holdReads makes the fake Agent's settings reads wait until the returned release is called, and
// says on started when each one has begun.
func (a *fakeAutorunAgent) holdReads(t *testing.T) (started <-chan struct{}, release func()) {
	hold := make(chan struct{})
	begun := make(chan struct{}, 8)
	var once sync.Once
	release = func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)
	a.onServe = func() {
		begun <- struct{}{}
		<-hold
	}
	return begun, release
}

// A member whose Git host does not answer holds neither another host's members nor the next
// pass, and is not read twice while its read is still out.
func TestMemoRepoRecheckSlowMemberHoldsNoOne(t *testing.T) {
	e, slow := repoEnv(t, "")
	slow.remotes = map[string]string{"app": "slow.example"}
	putRounds(e)
	slow.answer(http.StatusBadGateway, `{"error":{"code":"timeout","message":"x"}}`)
	started, release := slow.holdReads(t)

	fast := e.repoAgent("c", "")
	fast.remotes = map[string]string{"lib": "fast.example"}
	fast.file("version: 1\ncategories:\n  rounds:\n    max_active: 3\n")
	lib := e.memberQueue("c", "lib")

	rc := &memoRepoRechecker{mgr: e.mgr, every: time.Hour, now: time.Now, reads: 2, perHost: 1}
	ctx := context.Background()
	rc.start(ctx)
	<-started
	waitFor(t, "the other host's member to be read", func() bool { return e.memberRepoCheck("c", lib).State == memoRepoCheckDiffers })

	// The slow member is still out: a new pass leaves it alone. Its read fails, so it stays due,
	// and a pass that did not leave it out would read it a second time.
	rc.start(ctx)
	release()
	rc.wg.Wait()
	rc.tick(ctx)
	if got := slow.requested(); len(got) != 2 {
		// One read held, then one by the last tick once the first had finished.
		t.Fatalf("slow member's reads: %v", got)
	}
	if got := fast.requested(); len(got) != 1 {
		t.Fatalf("fast member's reads: %v", got)
	}
}

// Members on one Git host are read one after another when the host's bound is one.
func TestMemoRepoRecheckBoundsAHost(t *testing.T) {
	e, first := repoEnv(t, "")
	first.remotes = map[string]string{"app": "git.example"}
	putRounds(e)
	first.file("version: 1\ncategories:\n  rounds:\n    max_active: 3\n")
	started, release := first.holdReads(t)

	rc := &memoRepoRechecker{mgr: e.mgr, every: time.Hour, now: time.Now, reads: 4, perHost: 1}
	ctx := context.Background()
	rc.start(ctx)
	<-started

	// The second member's queue comes once the first read holds the host, so the next pass
	// starts it while the host is taken.
	second := e.repoAgent("c", "")
	second.remotes = map[string]string{"lib": "GIT.example"}
	second.file("version: 1\ncategories:\n  rounds:\n    max_active: 3\n")
	lib := e.memberQueue("c", "lib")
	rc.start(ctx)
	time.Sleep(200 * time.Millisecond)
	if got := second.requested(); len(got) != 0 {
		t.Fatalf("read while the host's one slot was taken: %v", got)
	}
	release()
	rc.tick(ctx)
	if got := second.requested(); len(got) != 1 || e.memberRepoCheck("c", lib).State != memoRepoCheckDiffers {
		t.Fatalf("not read once the slot was free: %v", got)
	}
}

// acquireRead holds a host to its own bound and every host to the shared one, and forgets a host
// once nobody holds or waits for it.
func TestMemoRepoRecheckAcquireRead(t *testing.T) {
	rc := &memoRepoRechecker{reads: 2, perHost: 1}
	rc.setup()
	ctx := context.Background()
	blocked := func(host string) bool {
		t.Helper()
		c, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		release, ok := rc.acquireRead(c, host)
		if ok {
			release()
		}
		return !ok
	}
	h1, ok := rc.acquireRead(ctx, "h")
	if !ok {
		t.Fatal("first read of h")
	}
	if !blocked("h") {
		t.Fatal("a second read of h was let through")
	}
	g, ok := rc.acquireRead(ctx, "g")
	if !ok {
		t.Fatal("another host was held by h")
	}
	if !blocked("k") {
		t.Fatal("a third read was let through past the shared bound")
	}
	h1()
	if blocked("k") {
		t.Fatal("k still blocked once h was released")
	}
	g()
	rc.mu.Lock()
	n := len(rc.hosts)
	rc.mu.Unlock()
	if n != 0 {
		t.Fatalf("hosts left: %d", n)
	}
}
