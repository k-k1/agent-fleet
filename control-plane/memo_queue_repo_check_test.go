package main

import (
	"context"
	"errors"
	"fmt"
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

// memberQueue gives member who an enabled queue on category name of repository repo.
func (e *inboxEnv) memberQueue(who, repo, name string) string {
	e.t.Helper()
	rec := e.do(who, "POST", "/api/memo-categories", map[string]any{"repo": repo, "name": name})
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

// holder makes a hook wait until release is called, and says on started when each call has
// begun. release runs at cleanup too, so a failed test never leaves a request hanging.
func holder(t *testing.T) (hook func(), started <-chan struct{}, release func()) {
	hold := make(chan struct{})
	begun := make(chan struct{}, 16)
	var once sync.Once
	release = func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)
	return func() {
		begun <- struct{}{}
		<-hold
	}, begun, release
}

func recv(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func (rc *memoRepoRechecker) hostUsers(host string) int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if h := rc.hosts[host]; h != nil {
		return h.users
	}
	return 0
}

// assertIdle checks that every slot was given back and no member is left busy.
func (rc *memoRepoRechecker) assertIdle(t *testing.T) {
	t.Helper()
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.readSlots) != 0 || len(rc.prepSlots) != 0 || len(rc.hosts) != 0 || len(rc.busy) != 0 || rc.prepWaiting != 0 {
		t.Fatalf("left behind: reads %d, prepare %d, hosts %d, busy %d, waiting %d",
			len(rc.readSlots), len(rc.prepSlots), len(rc.hosts), len(rc.busy), rc.prepWaiting)
	}
}

const roundsDiffers = "version: 1\ncategories:\n  rounds:\n    max_active: 3\n"

// hookRuntime runs hook whenever the pass asks whether the workspace runs: the last step of a
// member's preparation, after its queues and workspace were read.
type hookRuntime struct {
	stubRuntime
	hook func()
}

func (h hookRuntime) State(ctx context.Context) string {
	if h.hook != nil {
		h.hook()
	}
	return h.stubRuntime.State(ctx)
}

func (e *inboxEnv) stateHook(who string, hook func()) {
	e.mgr.mu.Lock()
	defer e.mgr.mu.Unlock()
	c := e.mgr.rts[e.members[who]]
	c.rt = hookRuntime{c.rt.(stubRuntime), hook}
	e.mgr.rts[e.members[who]] = c
}

// rechecker is a rechecker of e with the given bounds that already knows hosts, given as
// "who/repo" and the remote a read would have named.
func (e *inboxEnv) rechecker(reads, perHost, prepare int, hosts map[string]string) *memoRepoRechecker {
	rc := &memoRepoRechecker{mgr: e.mgr, every: time.Hour, now: time.Now, reads: reads, perHost: perHost, prepare: prepare}
	rc.setup()
	e.learn(rc, hosts)
	return rc
}

// learn tells rc hosts as reads would have. A pass forgets a member with no enabled queue, so a
// member's host is learnt once its queue exists.
func (e *inboxEnv) learn(rc *memoRepoRechecker, hosts map[string]string) {
	for k, remote := range hosts {
		who, repo, _ := strings.Cut(k, "/")
		rc.learnHost(e.members[who], repo, remote)
	}
}

// A member whose Git host does not answer holds neither another host's members nor the next
// pass, and is not read twice while its read is still out.
func TestMemoRepoRecheckSlowMemberHoldsNoOne(t *testing.T) {
	e, slow := repoEnv(t, "")
	putRounds(e)
	slow.answer(http.StatusBadGateway, `{"error":{"code":"timeout","message":"x"}}`)
	hook, started, release := holder(t)
	slow.onServe = hook

	fast := e.repoAgent("c", "")
	fast.file(roundsDiffers)
	lib := e.memberQueue("c", "lib", "rounds")

	rc := e.rechecker(2, 1, 0, map[string]string{"a/app": "https://slow.example/o/app", "c/lib": "https://fast.example/o/lib"})
	ctx := context.Background()
	rc.start(ctx)
	recv(t, started, "the slow member's read")
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
	rc.assertIdle(t)
}

// A member waiting for its host holds no shared slot: with two shared slots and one per host, a
// read on host h, a second member waiting for h, and a read on host g all go at once. The second
// member is read once h is free. Repositories no read has named a host for yet share one bucket,
// whatever their names.
func TestMemoRepoRecheckHostWaitHoldsNoSharedSlot(t *testing.T) {
	for _, tc := range []struct {
		name, first, second, bucket string
	}{
		{"known host", "https://h.example/o/app", "H.example:o/lib.git", "h.example"},
		{"no host yet", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, first := repoEnv(t, "")
			putRounds(e)
			first.file(roundsDiffers)
			hook, started, release := holder(t)
			first.onServe = hook
			rc := e.rechecker(2, 1, 0, map[string]string{"a/app": tc.first})
			ctx := context.Background()
			rc.start(ctx)
			recv(t, started, "the first read")

			second := e.repoAgent("b", "")
			second.file(roundsDiffers)
			lib := e.memberQueue("b", "lib", "rounds")
			e.learn(rc, map[string]string{"b/lib": tc.second})
			rc.start(ctx)
			waitFor(t, "the second member to wait for the first one's bucket", func() bool { return rc.hostUsers(tc.bucket) == 2 })

			other := e.repoAgent("c", "")
			other.file(roundsDiffers)
			doc := e.memberQueue("c", "doc", "rounds")
			e.learn(rc, map[string]string{"c/doc": "ssh://g.example:2222/o/doc"})
			rc.start(ctx)
			waitFor(t, "the other host's read", func() bool { return e.memberRepoCheck("c", doc).State == memoRepoCheckDiffers })
			if got := second.requested(); len(got) != 0 {
				t.Fatalf("read while the bucket's one slot was taken: %v", got)
			}

			release()
			rc.wg.Wait()
			if got := second.requested(); len(got) != 1 || e.memberRepoCheck("b", lib).State != memoRepoCheckDiffers {
				t.Fatalf("not read once the bucket was free: %v", got)
			}
			rc.assertIdle(t)
		})
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
	rc.assertIdle(t)
}

// With one prepare slot, a second member waits for it while the first prepares, and both are
// read once that ends.
func TestMemoRepoRecheckBoundsPrepare(t *testing.T) {
	e, a := repoEnv(t, "")
	app := putRounds(e)
	a.file(roundsDiffers)
	c := e.repoAgent("c", "")
	lib := e.memberQueue("c", "lib", "rounds")
	c.file(roundsDiffers)
	hook, started, release := holder(t)
	e.stateHook("a", hook)
	e.stateHook("c", hook)

	rc := e.rechecker(0, 0, 1, nil)
	ctx := context.Background()
	rc.start(ctx)
	recv(t, started, "a member's preparation")
	waitFor(t, "the other member to wait for the slot", func() bool {
		rc.mu.Lock()
		defer rc.mu.Unlock()
		return rc.prepWaiting == 1
	})
	select {
	case <-started:
		t.Fatal("a second member prepared past the bound of one")
	default:
	}
	release()
	rc.wg.Wait()
	if e.memberRepoCheck("a", app).State != memoRepoCheckDiffers || e.memberRepoCheck("c", lib).State != memoRepoCheckDiffers {
		t.Fatal("not both read")
	}
	rc.assertIdle(t)
}

// The prepare slot is given back before the reads: with one slot, a member whose read hangs
// does not keep another member from preparing and being read.
func TestMemoRepoRecheckPrepareEndsBeforeReads(t *testing.T) {
	e, a := repoEnv(t, "")
	putRounds(e)
	a.file(roundsDiffers)
	hook, started, release := holder(t)
	a.onServe = hook
	rc := e.rechecker(0, 0, 1, map[string]string{"a/app": "https://x.example/o/app"})
	ctx := context.Background()
	rc.start(ctx)
	recv(t, started, "the first member's read")

	c := e.repoAgent("c", "")
	c.file(roundsDiffers)
	lib := e.memberQueue("c", "lib", "rounds")
	e.learn(rc, map[string]string{"c/lib": "https://y.example/o/lib"})
	rc.start(ctx)
	waitFor(t, "the second member to be read", func() bool { return e.memberRepoCheck("c", lib).State == memoRepoCheckDiffers })
	release()
	rc.wg.Wait()
	rc.assertIdle(t)
}

// A read teaches the pass its repository's host, from the remote it names. A repository with no
// enabled queue left is forgotten, and so is a member with none.
func TestMemoRepoRecheckLearnsTheHost(t *testing.T) {
	e, a := repoEnv(t, "")
	app := putRounds(e)
	lib := e.memberQueue("a", "lib", "rounds")
	a.remote = "GIT.example:o/x.git"
	a.file(roundsDiffers)
	rc := e.rechecker(0, 0, 0, nil)
	mid := e.members["a"]
	rc.tick(context.Background())
	if h, g := rc.hostOf(mid, "app"), rc.hostOf(mid, "lib"); h != "git.example" || g != "git.example" {
		t.Fatalf("hosts %q %q", h, g)
	}
	off := func(id string) {
		put := map[string]any{"instruction": "old", "spendCapUsd": 60, "maxActive": 2, "enabled": false}
		if rec := e.do("a", "PUT", "/api/memo-queues/"+id, put); rec.Code != http.StatusOK {
			t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
		}
	}
	off(lib)
	rc.tick(context.Background())
	if h, g := rc.hostOf(mid, "app"), rc.hostOf(mid, "lib"); h != "git.example" || g != "" {
		t.Fatalf("after lib's queue went off: %q %q", h, g)
	}
	off(app)
	rc.tick(context.Background())
	rc.mu.Lock()
	n := len(rc.known)
	rc.mu.Unlock()
	if n != 0 {
		t.Fatalf("members still known: %d", n)
	}
	rc.assertIdle(t)
}

func TestMemoRepoRemoteHost(t *testing.T) {
	for remote, want := range map[string]string{
		"https://GitHub.com/o/r":        "github.com",
		"ssh://h.example:2222/o/r":      "h.example",
		"git://h.example/o/r":           "h.example",
		"H.example:o/r.git":             "h.example",
		"/srv/git/r":                    "",
		"file:///srv/git/r":             "",
		"(remote not shown)":            "",
		"":                              "",
		"https://[::1]:8443/o/r":        "::1",
		"weird path with space:/o/r":    "",
		"https://user@h.example/o/r":    "h.example",
		"ssh://git@SSH.example:22/o/r":  "ssh.example",
		"http://h.example":              "h.example",
		"relative/path:with/colon":      "",
		"https://%zz":                   "",
		"git@h.example:o/r":             "h.example",
		"ssh://h.example/o/r?x#y":       "h.example",
		"HTTPS://Mixed.Example/o/r.git": "mixed.example",
	} {
		if got := memoRepoRemoteHost(remote); got != want {
			t.Errorf("%q: %q, want %q", remote, got, want)
		}
	}
}

// Two due queues of one repository take one read, and both keep it.
func TestMemoRepoRecheckOneReadPerRepository(t *testing.T) {
	e, a := repoEnv(t, "")
	rounds := putRounds(e)
	other := e.memberQueue("a", "app", "other")
	a.file(twoCategories)
	(&memoRepoRechecker{mgr: e.mgr, every: time.Hour, now: time.Now}).tick(context.Background())
	if got := a.requested(); len(got) != 1 {
		t.Fatalf("Agent calls %v", got)
	}
	if n := recorded(e, rounds, other); n != 2 {
		t.Fatalf("%d of two queues recorded", n)
	}
}

const twoCategories = "version: 1\ncategories:\n  rounds:\n    max_active: 3\n  other:\n    max_active: 3\n"

// With the tenant's switch off, the pass asks the Agent nothing.
func TestMemoRepoRecheckSwitchOffAsksNothing(t *testing.T) {
	e, a := repoEnv(t, "")
	putRounds(e)
	a.file(roundsDiffers)
	e.memoQueuesOn(false)
	rc := e.rechecker(0, 0, 0, nil)
	rc.tick(context.Background())
	if got := a.requested(); len(got) != 0 {
		t.Fatalf("Agent calls %v", got)
	}
	rc.assertIdle(t)
}

// Turned off while a member waits for its host's slot, the switch stops that read when the slot
// comes free.
func TestMemoRepoRecheckSwitchOffWhileWaiting(t *testing.T) {
	e, first := repoEnv(t, "")
	putRounds(e)
	first.file(roundsDiffers)
	hook, started, release := holder(t)
	first.onServe = hook
	rc := e.rechecker(0, 1, 0, map[string]string{"a/app": "https://h.example/o/app"})
	ctx := context.Background()
	rc.start(ctx)
	recv(t, started, "the first read")
	second := e.repoAgent("c", "")
	second.file(roundsDiffers)
	e.memberQueue("c", "lib", "rounds")
	e.learn(rc, map[string]string{"c/lib": "https://h.example/o/lib"})
	rc.start(ctx)
	waitFor(t, "the second member to wait for h", func() bool { return rc.hostUsers("h.example") == 2 })
	e.memoQueuesOn(false)
	release()
	rc.wg.Wait()
	if got := second.requested(); len(got) != 0 {
		t.Fatalf("read after the switch went off: %v", got)
	}
	rc.assertIdle(t)
}

// flakySwitchStore fails one GetTenantMemoQueues once fail is set.
type flakySwitchStore struct {
	store.Store
	fail atomic.Bool
}

func (s *flakySwitchStore) GetTenantMemoQueues(ctx context.Context, tenantID string) (bool, error) {
	if s.fail.CompareAndSwap(true, false) {
		return false, errors.New("flaky")
	}
	return s.Store.GetTenantMemoQueues(ctx, tenantID)
}

// recorded counts the categories of member a with a read kept.
func recorded(e *inboxEnv, ids ...string) int {
	n := 0
	for _, id := range ids {
		if e.memberRepoCheck("a", id).State == memoRepoCheckDiffers {
			n++
		}
	}
	return n
}

// A switch that fails to answer once skips that queue only, as each queue asks again: the
// next repository is read, and so is the same repository for its next queue, whether the
// failure came before its read or after it.
func TestMemoRepoRecheckSwitchErrorSkipsOneQueue(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sameRepo  bool
		afterRead bool
	}{
		{"next repository", false, false},
		{"same repository, before the read", true, false},
		{"same repository, after the read", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, a := repoEnv(t, "")
			flaky := &flakySwitchStore{Store: e.mgr.store}
			e.mgr.store = flaky
			first := putRounds(e)
			var second string
			if tc.sameRepo {
				second = e.memberQueue("a", "app", "other")
				a.file(twoCategories)
			} else {
				second = e.memberQueue("a", "lib", "rounds")
				a.file(roundsDiffers)
			}
			if tc.afterRead {
				a.onServe = func() { flaky.fail.Store(true) }
			} else {
				// The workspace's state is the last thing asked before the first queue's check.
				e.stateHook("a", func() { flaky.fail.Store(true) })
			}
			rc := e.rechecker(0, 0, 0, nil)
			rc.tick(context.Background())
			if got := a.requested(); len(got) != 1 {
				t.Fatalf("Agent calls %v", got)
			}
			if n := recorded(e, first, second); n != 1 {
				t.Fatalf("%d of two queues recorded", n)
			}
			rc.assertIdle(t)
		})
	}
}

// staleListStore answers ListEnabledMemoQueues as it stood before any read was kept.
type staleListStore struct {
	store.Store
}

func (s staleListStore) ListEnabledMemoQueues(ctx context.Context) ([]store.MemoQueue, error) {
	qs, err := s.Store.ListEnabledMemoQueues(ctx)
	for i := range qs {
		qs[i].RepoCheck = store.MemoRepoCheck{}
	}
	return qs, err
}

// A pass decides what is due from the member's queues as they are once it has the member, not
// from the list it started from: that list can predate the read an earlier pass kept just
// before it ended.
func TestMemoRepoRecheckDecidesFromFreshQueues(t *testing.T) {
	e, a := repoEnv(t, "")
	id := putRounds(e)
	a.file(roundsDiffers)
	rc := e.rechecker(0, 0, 0, nil)
	rc.tick(context.Background())
	if c := e.memberRepoCheck("a", id); c.State != memoRepoCheckDiffers {
		t.Fatalf("first read: %+v", c)
	}
	e.mgr.store = staleListStore{e.mgr.store}
	rc.tick(context.Background())
	if got := a.requested(); len(got) != 1 {
		t.Fatalf("a queue read moments ago was read again: %v", got)
	}
}

// The queues are read in their own order, as the serial loop took them: a later queue of a
// repository already read does not hold back an earlier queue of another one. With A (app), B
// (lib), C (app) and C's instruction file hanging, B is kept by then.
func TestMemoRepoRecheckKeepsTheQueueOrder(t *testing.T) {
	e, a := repoEnv(t, "")
	qa := putRounds(e)
	qb := e.memberQueue("a", "lib", "rounds")
	qc := e.memberQueue("a", "app", "other")
	for i, id := range []string{qa, qb, qc} {
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_queue SET created_at=? WHERE category_id=?`,
			fmt.Sprintf("2026-01-01T00:00:0%dZ", i+1), id); err != nil {
			t.Fatal(err)
		}
	}
	a.file("version: 1\ncategories:\n  rounds:\n    max_active: 3\n  other:\n    instruction_file: o.md\n")
	a.instruction("o.md", "new")
	hook, started, release := holder(t)
	a.onInstr = hook
	rc := e.rechecker(0, 0, 0, nil)
	rc.start(context.Background())
	recv(t, started, "C's instruction read")
	if e.memberRepoCheck("a", qa).State == "" || e.memberRepoCheck("a", qb).State == "" {
		t.Fatalf("A %+v, B %+v while C waits", e.memberRepoCheck("a", qa), e.memberRepoCheck("a", qb))
	}
	release()
	rc.wg.Wait()
	if e.memberRepoCheck("a", qc).State == "" {
		t.Fatal("C not kept")
	}
	if got := a.requested(); len(got) != 3 {
		// app, lib, and C's instruction: app is read once for A and C.
		t.Fatalf("Agent calls %v", got)
	}
	rc.assertIdle(t)
}

// A repository whose read failed is not read again for its next queue in the same pass.
func TestMemoRepoRecheckFailedReadOnce(t *testing.T) {
	e, a := repoEnv(t, "")
	putRounds(e)
	e.memberQueue("a", "app", "other")
	a.answer(http.StatusBadGateway, `{"error":{"code":"fetch_failed","message":"x"}}`)
	rc := e.rechecker(0, 0, 0, nil)
	rc.tick(context.Background())
	if got := a.requested(); len(got) != 1 {
		t.Fatalf("Agent calls %v", got)
	}
	rc.assertIdle(t)
}
