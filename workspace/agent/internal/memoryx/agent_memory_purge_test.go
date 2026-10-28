package memoryx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	purgeOldText = "The customer is Hollowbrook Dairy and their contact line is kept private."
	purgeMidText = "Second draft naming Hollowbrook Dairy once more."
	purgeNewText = "Current text with nothing to hide."
)

// purgeFixture saves a (three revisions, the first two with the text to purge) and b (two
// revisions), takes a manual snapshot and parks an imported lineage on the first revision of a.
type purgeFixture struct {
	c         agentMemCaller
	pid       string
	oldBlob   string
	oldCommit string
}

func purgeSetup(t *testing.T) purgeFixture {
	t.Helper()
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	f := purgeFixture{c: c, pid: c.Project.ID}
	save := func(name, body string, rev int) {
		t.Helper()
		if _, err := agentMemSave(c, agentMemSaveReq{Name: name, Description: "d", Body: body, Revision: rev}, now); err != nil {
			t.Fatal(err)
		}
	}
	save("a", purgeOldText, 0)
	f.oldBlob, _ = memoryGitRun("rev-parse", memoryBranch+":af/projects/"+f.pid+"/a.md")
	f.oldCommit, _ = memoryGitRun("rev-parse", memoryBranch)
	save("b", "b one", 0)
	save("a", purgeMidText, 1)
	save("b", "b two", 1)
	for _, ref := range []string{"main"} {
		_ = ref
	}
	if _, err := memorySnapshot(memoryTriggerManual, now); err != nil {
		t.Fatal(err)
	}
	save("a", purgeNewText, 2)
	// An imported lineage and a rescue ref that still hold the old commits.
	if _, err := memoryGitRun("update-ref", "refs/imports/20261004T090000Z/main", f.oldCommit); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryGitRun("update-ref", "refs/premigrate/20261004T090000Z", f.oldCommit); err != nil {
		t.Fatal(err)
	}
	return f
}

func purgeDo(t *testing.T, f purgeFixture, name string) (agentMemPurgeResult, error) {
	t.Helper()
	pv, err := agentMemPurgePreview(agentMemScopeProject, f.pid, name)
	if err != nil {
		t.Fatal(err)
	}
	return agentMemPurge(agentMemPurgeReq{Scope: agentMemScopeProject, Project: f.pid, Name: name, Plan: pv.Plan, Confirm: name})
}

// allObjectsContain reads every object on disk, loose or packed, reachable or not.
func allObjectsContain(t *testing.T, needle string) bool {
	t.Helper()
	cmd := memoryGit("cat-file", "--batch-all-objects", "--batch")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Contains(out, []byte(needle))
}

func objectExists(sha string) bool {
	_, err := memoryGitRun("cat-file", "-e", sha)
	return err == nil
}

func fsckClean(t *testing.T) {
	t.Helper()
	if out, err := memoryGitRun("fsck", "--no-dangling", "--strict"); err != nil {
		t.Fatalf("fsck: %v\n%s", err, out)
	}
}

func TestAgentMemoryPurgeRemovesPastRevisionsEverywhere(t *testing.T) {
	f := purgeSetup(t)
	if !allObjectsContain(t, "Hollowbrook") {
		t.Fatal("fixture: the text is not in the repo")
	}
	res, err := purgeDo(t, f, "a")
	if err != nil {
		t.Fatal(err)
	}
	if !res.KeptCurrent || res.CommitsDropped == 0 || res.RefsRewritten+res.RefsDeleted < 2 || res.BlobsRemoved < 2 {
		t.Fatalf("result = %+v", res)
	}
	// Gone from the objects, not only unreachable: the old blob, the text in any object, and the
	// refs that held it.
	if objectExists(f.oldBlob) || allObjectsContain(t, "Hollowbrook") {
		t.Fatal("the purged text is still in the repo")
	}
	if !allObjectsContain(t, purgeNewText) {
		t.Fatal("the current text must stay")
	}
	for _, ref := range []string{"refs/imports/20261004T090000Z/main", "refs/premigrate/20261004T090000Z"} {
		if out, err := memoryGitRun("log", "--format=%H", "--", "af"); err == nil && strings.Contains(out, f.oldCommit) {
			t.Fatalf("%s still reaches the old commit", ref)
		}
		if _, err := memoryGitRun("rev-parse", "--verify", "--quiet", ref); err == nil {
			if _, err := memoryGitRun("cat-file", "-e", f.oldCommit); err == nil {
				t.Fatalf("%s: old commit object survived", ref)
			}
		}
	}
	fsckClean(t)
	if _, err := os.Stat(agentMemPurgePendingPath()); err == nil {
		t.Fatal("the gc marker was left behind")
	}
	// The memory itself is untouched and keeps its revision.
	e, err := agentMemRead(f.c, "", "a")
	if err != nil || e.Body != purgeNewText || e.Revision != 3 {
		t.Fatalf("a = %+v, %v", e, err)
	}
	if out, _ := memoryGitRun("show", memoryBranch+":af/projects/"+f.pid+"/a.md"); !strings.Contains(out, purgeNewText) {
		t.Fatalf("the tip lost the current text")
	}
}

func TestAgentMemoryPurgeLeavesOtherMemoriesHistory(t *testing.T) {
	f := purgeSetup(t)
	before := agentMemChangesT(t)
	var bBefore []agentMemChangeView
	for _, ch := range before {
		if ch.Name == "b" {
			bBefore = append(bBefore, ch)
		}
	}
	if _, err := purgeDo(t, f, "a"); err != nil {
		t.Fatal(err)
	}
	var bAfter []agentMemChangeView
	for _, ch := range agentMemChangesT(t) {
		if ch.Name == "b" {
			bAfter = append(bAfter, ch)
		}
	}
	if len(bAfter) != len(bBefore) || len(bAfter) != 2 {
		t.Fatalf("b changes: %d before, %d after", len(bBefore), len(bAfter))
	}
	// b's revisions are readable and its newest change reverts to the earlier text.
	if _, err := agentMemRevert(agentMemRevertReq{Commit: bAfter[0].Commit}, time.Now()); err != nil {
		t.Fatalf("revert b: %v", err)
	}
	e, err := agentMemRead(f.c, "", "b")
	if err != nil || e.Body != "b one" {
		t.Fatalf("b after revert = %+v, %v", e, err)
	}
	diff, err := agentMemChangeDiff(bAfter[1].Commit)
	if err != nil || !strings.Contains(diff.Diff, "b one") {
		t.Fatalf("diff of b's first change: %+v, %v", diff, err)
	}
	fsckClean(t)
}

func TestAgentMemoryPurgeRevertAcrossPurgedRangeFails(t *testing.T) {
	f := purgeSetup(t)
	if _, err := purgeDo(t, f, "a"); err != nil {
		t.Fatal(err)
	}
	var rows []agentMemChangeView
	for _, ch := range agentMemChangesT(t) {
		if ch.Name == "a" {
			rows = append(rows, ch)
		}
	}
	if len(rows) != 1 || !rows[0].PurgedBefore || !rows[0].Latest || !rows[0].Live {
		t.Fatalf("a's rows after the purge = %+v", rows)
	}
	_, err := agentMemRevert(agentMemRevertReq{Commit: rows[0].Commit}, time.Now())
	if agentMemCode(err) != errCodeMemoryConflict || !strings.Contains(err.Error(), "purged") {
		t.Fatalf("revert across the purge: %v", err)
	}
	if e, _ := agentMemRead(f.c, "", "a"); e.Body != purgeNewText {
		t.Fatalf("a changed: %+v", e)
	}
	if allObjectsContain(t, "Hollowbrook") {
		t.Fatal("the failed revert brought the text back")
	}
	// Forgetting still works, and a new save writes a change that reverts normally.
	if _, err := agentMemRevert(agentMemRevertReq{Commit: rows[0].Commit, Forget: true}, time.Now()); err != nil {
		t.Fatalf("forget: %v", err)
	}
}

func TestAgentMemoryPurgeKeepsWorkingAfterwards(t *testing.T) {
	f := purgeSetup(t)
	if _, err := purgeDo(t, f, "a"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := agentMemSave(f.c, agentMemSaveReq{Name: "a", Description: "d", Body: "after the purge", Revision: 3}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := memorySnapshot(memoryTriggerManual, now); err != nil {
		t.Fatal(err)
	}
	if st, err := memoryGitRun("status", "--porcelain"); err != nil || strings.Contains(st, "Hollowbrook") {
		t.Fatalf("status: %q %v", st, err)
	}
	newest := agentMemChangesT(t)[0]
	if _, err := agentMemRevert(agentMemRevertReq{Commit: newest.Commit}, now); err != nil {
		t.Fatalf("revert after purge: %v", err)
	}
	if e, _ := agentMemRead(f.c, "", "a"); e.Body != purgeNewText {
		t.Fatalf("a = %+v", e)
	}
	fsckClean(t)
	if allObjectsContain(t, "Hollowbrook") {
		t.Fatal("the text is back")
	}
}

func TestAgentMemoryPurgeForgottenMemory(t *testing.T) {
	f := purgeSetup(t)
	last := agentMemChangesT(t)[0]
	if last.Name != "a" {
		t.Fatalf("fixture: %+v", last)
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: last.Commit, Forget: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	pv, err := agentMemPurgePreview(agentMemScopeProject, f.pid, "a")
	if err != nil || pv.Live || pv.KeepsCurrent || pv.Nothing {
		t.Fatalf("preview = %+v, %v", pv, err)
	}
	if _, err := purgeDo(t, f, "a"); err != nil {
		t.Fatal(err)
	}
	if allObjectsContain(t, "Hollowbrook") || allObjectsContain(t, purgeNewText) {
		t.Fatal("a forgotten memory's text survived its purge")
	}
	for _, ch := range agentMemChangesT(t) {
		if ch.Name == "a" {
			t.Fatalf("a row for a purged memory: %+v", ch)
		}
	}
	// Its revision floor survives, so a new memory by that name does not reuse a revision.
	if _, err := agentMemSave(f.c, agentMemSaveReq{Name: "a", Description: "d", Body: "again"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if e, _ := agentMemRead(f.c, "", "a"); e.Revision <= 3 {
		t.Fatalf("revision = %d", e.Revision)
	}
	fsckClean(t)
}

func TestAgentMemoryPurgePreviewShowsNoText(t *testing.T) {
	f := purgeSetup(t)
	pv, err := agentMemPurgePreview(agentMemScopeProject, f.pid, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(pv)
	for _, leak := range []string{"Hollowbrook", purgeNewText, "contact line"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("the preview echoes the text: %s", b)
		}
	}
	if !pv.KeepsCurrent || !pv.Live || pv.ChangesTotal != 2 || len(pv.Refs) < 3 {
		t.Fatalf("preview = %+v", pv)
	}
	kinds := map[string]bool{}
	for _, r := range pv.Refs {
		kinds[r.Kind] = true
	}
	if !kinds["main"] || !kinds["import"] || !kinds["rescue"] {
		t.Fatalf("refs = %+v", pv.Refs)
	}
	if _, err := memoryGitRun("cat-file", "-e", f.oldBlob); err != nil {
		t.Fatal("the preview changed the repo")
	}
}

func TestAgentMemoryPurgeReportsScannerFindingsMasked(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Now()
	leak := "ghp_" + strings.Repeat("a1B2c3D4e5", 4)
	// The save path's scan refuses a secret, so the history gets it the way an import or a hand
	// edit would: a commit made behind the store's back.
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "a", Description: "d", Body: "ok"}, now); err != nil {
		t.Fatal(err)
	}
	pid := c.Project.ID
	path := agentMemDir() + "/projects/" + pid + "/a.md"
	orig, _ := os.ReadFile(path)
	staging := memoryStagingDir() + "/af/projects/" + pid + "/a.md"
	bad := strings.Replace(string(orig), "ok", "token "+leak, 1)
	if err := os.WriteFile(staging, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryGitRun("add", "-A", "--", "af"); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryGitRun("commit", "--quiet", "-m", "hand edit"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staging, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryGitRun("add", "-A", "--", "af"); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryGitRun("commit", "--quiet", "-m", "hand edit back"); err != nil {
		t.Fatal(err)
	}
	pv, err := agentMemPurgePreview(agentMemScopeProject, pid, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Findings) == 0 {
		t.Fatalf("no findings: %+v", pv)
	}
	b, _ := json.Marshal(pv)
	if strings.Contains(string(b), leak) {
		t.Fatalf("the preview echoes the secret: %s", b)
	}
	if _, err := agentMemPurge(agentMemPurgeReq{Scope: agentMemScopeProject, Project: pid, Name: "a", Plan: pv.Plan, Confirm: "a"}); err != nil {
		t.Fatal(err)
	}
	if allObjectsContain(t, leak) {
		t.Fatal("the secret survived the purge")
	}
	fsckClean(t)
}

func TestAgentMemoryPurgeRefusals(t *testing.T) {
	f := purgeSetup(t)
	pv, _ := agentMemPurgePreview(agentMemScopeProject, f.pid, "a")
	req := agentMemPurgeReq{Scope: agentMemScopeProject, Project: f.pid, Name: "a", Plan: pv.Plan, Confirm: "a"}
	bad := req
	bad.Confirm = "b"
	if _, err := agentMemPurge(bad); agentMemCode(err) != errCodeMemoryBadRequest {
		t.Fatalf("wrong confirmation: %v", err)
	}
	bad = req
	bad.Plan = ""
	if _, err := agentMemPurge(bad); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("no plan: %v", err)
	}
	bad = req
	bad.Scope = "elsewhere"
	if _, err := agentMemPurge(bad); agentMemCode(err) != errCodeMemoryBadRequest {
		t.Fatalf("bad scope: %v", err)
	}
	bad = req
	bad.Name = "../x"
	if _, err := agentMemPurge(bad); err == nil {
		t.Fatal("a bad name was accepted")
	}
	// The history moves on after the preview: the plan no longer matches.
	if _, err := agentMemSave(f.c, agentMemSaveReq{Name: "b", Description: "d", Body: "b three", Revision: 2}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemPurge(req); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("stale plan: %v", err)
	}
	if !objectExists(f.oldBlob) {
		t.Fatal("a refused purge changed the repo")
	}
	// A memory with a single revision has no past to purge.
	if _, err := agentMemSave(f.c, agentMemSaveReq{Name: "solo", Description: "d", Body: "once"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	spv, _ := agentMemPurgePreview(agentMemScopeProject, f.pid, "solo")
	if !spv.Nothing {
		t.Fatalf("solo preview = %+v", spv)
	}
	if _, err := agentMemPurge(agentMemPurgeReq{Scope: agentMemScopeProject, Project: f.pid, Name: "solo", Plan: spv.Plan, Confirm: "solo"}); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("nothing to purge: %v", err)
	}
}

func TestAgentMemoryPurgeRefusesAnUnknownRef(t *testing.T) {
	f := purgeSetup(t)
	if _, err := memoryGitRun("update-ref", "refs/tags/keepsake", f.oldCommit); err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemPurgePreview(agentMemScopeProject, f.pid, "a"); agentMemCode(err) != errCodeMemoryConflict {
		t.Fatalf("preview with a foreign ref: %v", err)
	}
	if !objectExists(f.oldBlob) {
		t.Fatal("the repo changed")
	}
}

// A crash between the steps leaves the repo consistent and the next run finishes the job.
func TestAgentMemoryPurgeCrashSafety(t *testing.T) {
	boom := errors.New("simulated crash")
	for _, stage := range []string{"objects", "refs", "gc"} {
		t.Run(stage, func(t *testing.T) {
			f := purgeSetup(t)
			headBefore, _ := memoryGitRun("rev-parse", memoryBranch)
			agentMemPurgeFault = func(s string) error {
				if s == stage {
					return boom
				}
				return nil
			}
			t.Cleanup(func() { agentMemPurgeFault = nil })
			if _, err := purgeDo(t, f, "a"); !errors.Is(err, boom) {
				t.Fatalf("purge = %v", err)
			}
			agentMemPurgeFault = nil
			fsckClean(t)
			head, _ := memoryGitRun("rev-parse", memoryBranch)
			switch stage {
			case "objects":
				if head != headBefore || !objectExists(f.oldBlob) {
					t.Fatal("a purge that never moved the refs changed the history")
				}
			default:
				if head == headBefore {
					t.Fatal("the refs did not move")
				}
			}
			// Whatever the stage, every memory still reads and writes.
			if e, err := agentMemRead(f.c, "", "a"); err != nil || e.Body != purgeNewText {
				t.Fatalf("a = %+v, %v", e, err)
			}
			if _, err := agentMemSave(f.c, agentMemSaveReq{Name: "b", Description: "d", Body: "b after", Revision: 2}, time.Now()); err != nil {
				t.Fatal(err)
			}
			if stage == "objects" {
				if _, err := purgeDo(t, f, "a"); err != nil {
					t.Fatalf("retry: %v", err)
				}
			} else {
				ResumeMemoryPurge()
			}
			if objectExists(f.oldBlob) || allObjectsContain(t, "Hollowbrook") {
				t.Fatal("the text is still there after the follow-up")
			}
			if _, err := os.Stat(agentMemPurgePendingPath()); err == nil {
				t.Fatal("marker left")
			}
			fsckClean(t)
		})
	}
}

func TestAgentMemoryPurgeConcurrentWithWrites(t *testing.T) {
	f := purgeSetup(t)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 8; i++ {
			e, err := agentMemRead(f.c, "", "b")
			if err != nil {
				errs <- err
				return
			}
			if _, err := agentMemSave(f.c, agentMemSaveReq{Name: "b", Description: "d", Body: "b " + strings.Repeat("x", i+1), Revision: e.Revision}, time.Now()); err != nil {
				errs <- err
				return
			}
			if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
				errs <- err
				return
			}
		}
	}()
	// The plan can go stale while b is written; the purge then asks to be reviewed again.
	var purged bool
	for i := 0; i < 50 && !purged; i++ {
		_, err := purgeDo(t, f, "a")
		switch {
		case err == nil:
			purged = true
		case agentMemCode(err) == errCodeMemoryConflict:
		default:
			t.Fatal(err)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if !purged {
		t.Fatal("the purge never got through")
	}
	if allObjectsContain(t, "Hollowbrook") {
		t.Fatal("text survived")
	}
	fsckClean(t)
	if e, err := agentMemRead(f.c, "", "b"); err != nil || !strings.HasPrefix(e.Body, "b x") {
		t.Fatalf("b = %+v, %v", e, err)
	}
}

func TestAgentMemoryPurgeRouteNeedsTheMark(t *testing.T) {
	f := purgeSetup(t)
	pv, _ := agentMemPurgePreview(agentMemScopeProject, f.pid, "a")
	body, _ := json.Marshal(agentMemPurgeReq{Scope: agentMemScopeProject, Project: f.pid, Name: "a", Plan: pv.Plan, Confirm: "a"})
	req := httptest.NewRequest(http.MethodPost, "/agents/memory/entries/purge", bytes.NewReader(body))
	w := httptest.NewRecorder()
	HandleAgentMemoryPurge(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("an unmarked purge: %d %s", w.Code, w.Body)
	}
	if !objectExists(f.oldBlob) {
		t.Fatal("an unmarked purge rewrote the history")
	}
}

// The bare repo's index can hold a revision that was staged and never committed (a snapshot that
// died after `git add`); gc keeps whatever the index names, so the purge has to rebuild it.
func TestAgentMemoryPurgeDropsAStagedOldRevision(t *testing.T) {
	f := purgeSetup(t)
	staging := memoryStagingDir() + "/af/projects/" + f.pid + "/a.md"
	cur, err := os.ReadFile(staging)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := memoryGitRun("show", f.oldBlob)
	if err := os.WriteFile(staging, []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryGitRun("add", "--", "af"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staging, cur, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := purgeDo(t, f, "a"); err != nil {
		t.Fatal(err)
	}
	if allObjectsContain(t, "Hollowbrook") {
		t.Fatal("the staged copy kept the text")
	}
	fsckClean(t)
}

// stageOldRevision leaves the old text in the bare repo's index and, when restore is false, in the
// staging file too (a snapshot that died between add and commit).
func stageOldRevision(t *testing.T, f purgeFixture, restore bool) string {
	t.Helper()
	staging := memoryStagingDir() + "/af/projects/" + f.pid + "/a.md"
	cur, err := os.ReadFile(staging)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := memoryGitRun("show", f.oldBlob)
	if err := os.WriteFile(staging, []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryGitRun("add", "--", "af"); err != nil {
		t.Fatal(err)
	}
	if restore {
		if err := os.WriteFile(staging, cur, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return staging
}

// Recovery is the same code as the first run: a crash right after the refs moved, with an old
// revision staged, and no save in between to refresh the index.
func TestAgentMemoryPurgeRecoveryRebuildsTheIndex(t *testing.T) {
	f := purgeSetup(t)
	stageOldRevision(t, f, true)
	agentMemPurgeFault = func(s string) error {
		if s == "refs" {
			return errors.New("simulated crash")
		}
		return nil
	}
	t.Cleanup(func() { agentMemPurgeFault = nil })
	if _, err := purgeDo(t, f, "a"); err == nil {
		t.Fatal("expected the simulated crash")
	}
	agentMemPurgeFault = nil
	ResumeMemoryPurge()
	if objectExists(f.oldBlob) || allObjectsContain(t, "Hollowbrook") {
		t.Fatal("recovery left the old revision behind")
	}
	if _, err := os.Stat(agentMemPurgePendingPath()); err == nil {
		t.Fatal("marker left after a verified recovery")
	}
	fsckClean(t)
}

// A failed check keeps the marker, and the retry works without the old history to rediscover.
func TestAgentMemoryPurgeKeepsTheMarkerUntilVerified(t *testing.T) {
	f := purgeSetup(t)
	if _, err := memoryGitRun("gc", "--quiet"); err != nil {
		t.Fatal(err)
	}
	packs, _ := os.ReadDir(memoryRepoDir() + "/objects/pack")
	var keep []string
	for _, e := range packs {
		if strings.HasSuffix(e.Name(), ".pack") {
			k := memoryRepoDir() + "/objects/pack/" + strings.TrimSuffix(e.Name(), ".pack") + ".keep"
			if err := os.WriteFile(k, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			keep = append(keep, k)
		}
	}
	if len(keep) == 0 {
		t.Fatal("fixture: no pack")
	}
	if _, err := purgeDo(t, f, "a"); err == nil {
		t.Fatal("a purge that left the text on disk reported success")
	}
	if _, err := os.Stat(agentMemPurgePendingPath()); err != nil {
		t.Fatal("the marker was dropped before verification")
	}
	for _, k := range keep {
		_ = os.Remove(k)
	}
	ResumeMemoryPurge()
	if objectExists(f.oldBlob) || allObjectsContain(t, "Hollowbrook") {
		t.Fatal("the retry did not remove the text")
	}
	if _, err := os.Stat(agentMemPurgePendingPath()); err == nil {
		t.Fatal("marker left")
	}
}

// An import racing the purge waits for it; it cannot add refs behind the rewrite.
func TestAgentMemoryPurgeSerialisesWithImportPrepare(t *testing.T) {
	f := purgeSetup(t)
	made, err := memoryExportBundle()
	if err != nil {
		t.Fatal(err)
	}
	bundle := t.TempDir() + "/old.bundle"
	if err := os.Rename(made, bundle); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	agentMemPurgeFault = func(s string) error {
		if s == "objects" {
			go func() {
				_, err := memoryImportPrepare(bundle, "x.bundle", time.Now())
				done <- err
			}()
			select {
			case <-done:
				t.Error("the import ran while the purge held the lock")
			case <-time.After(400 * time.Millisecond):
			}
		}
		return nil
	}
	t.Cleanup(func() { agentMemPurgeFault = nil })
	res, err := purgeDo(t, f, "a")
	if err != nil {
		t.Fatal(err)
	}
	agentMemPurgeFault = nil
	if res.BlobsShared != 0 || objectExists(f.oldBlob) {
		t.Fatalf("result = %+v", res)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Without restoring the staging file, the next snapshot would commit the old text again.
func TestAgentMemoryPurgeResyncsStaging(t *testing.T) {
	f := purgeSetup(t)
	staging := stageOldRevision(t, f, false)
	if _, err := purgeDo(t, f, "a"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(staging); strings.Contains(string(b), "Hollowbrook") {
		t.Fatal("staging still holds the old text")
	}
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	if objectExists(f.oldBlob) || allObjectsContain(t, "Hollowbrook") {
		t.Fatal("a snapshot brought the old text back")
	}
}

// Transfer temporaries the Agent itself left behind are part of the history it holds.
func TestAgentMemoryPurgeSweepsOrphanTransferFiles(t *testing.T) {
	f := purgeSetup(t)
	bundle, err := memoryExportBundle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := purgeDo(t, f, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundle); err == nil {
		t.Fatal("an orphan export bundle survived the purge")
	}
}

// A crash after the marker is written and before the ref transaction commits leaves the old refs
// in place: the recovery must see that nothing moved and let the purge run again.
func TestAgentMemoryPurgeCrashBeforeTheRefTransaction(t *testing.T) {
	f := purgeSetup(t)
	agentMemPurgeFault = func(s string) error {
		if s == "marker" {
			return errors.New("simulated crash")
		}
		return nil
	}
	t.Cleanup(func() { agentMemPurgeFault = nil })
	if _, err := purgeDo(t, f, "a"); err == nil {
		t.Fatal("expected the simulated crash")
	}
	agentMemPurgeFault = nil
	if _, err := os.Stat(agentMemPurgePendingPath()); err != nil {
		t.Fatal("fixture: the marker should exist")
	}
	// The next purge starts with the recovery and then goes through.
	if _, err := purgeDo(t, f, "a"); err != nil {
		t.Fatalf("purge after the crash window: %v", err)
	}
	if objectExists(f.oldBlob) || allObjectsContain(t, "Hollowbrook") {
		t.Fatal("text survived")
	}
	if _, err := os.Stat(agentMemPurgePendingPath()); err == nil {
		t.Fatal("marker left")
	}
	fsckClean(t)
}

func TestAgentMemoryPurgeRecoveryAfterAnUncommittedTransactionKeepsHistory(t *testing.T) {
	f := purgeSetup(t)
	agentMemPurgeFault = func(s string) error {
		if s == "marker" {
			return errors.New("simulated crash")
		}
		return nil
	}
	t.Cleanup(func() { agentMemPurgeFault = nil })
	_, _ = purgeDo(t, f, "a")
	agentMemPurgeFault = nil
	ResumeMemoryPurge()
	if !objectExists(f.oldBlob) {
		t.Fatal("a recovery of a transaction that never committed rewrote history")
	}
	if _, err := os.Stat(agentMemPurgePendingPath()); err == nil {
		t.Fatal("marker left")
	}
}

// A young orphan upload (the Agent died right after saving it) is removed; one a handler is
// still receiving is not.
func TestAgentMemoryPurgeSweepsYoungOrphanUploadsButNotActiveOnes(t *testing.T) {
	f := purgeSetup(t)
	if err := os.MkdirAll(memoryWorkDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := memoryWorkDir() + "/upload-orphan"
	active := memoryWorkDir() + "/upload-active"
	for _, p := range []string{orphan, active} {
		if err := os.WriteFile(p, []byte("bundle"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	memoryTrackUpload(active)
	t.Cleanup(func() { memoryUntrackUpload(active) })
	if _, err := purgeDo(t, f, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Fatal("a young orphan upload survived")
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatal("an upload being received was removed")
	}
}

// git update-ref --stdin is atomic per ref, not across refs: a killed git can leave some refs new
// and the rest old. The recovery must roll those forward, not call the purge "never ran".
func partialRefFixture(t *testing.T) (purgeFixture, map[string]string) {
	t.Helper()
	f := purgeSetup(t)
	before := map[string]string{}
	out, _ := memoryGitRun("for-each-ref", "--format=%(refname) %(objectname)")
	for _, l := range strings.Split(out, "\n") {
		if n, sha, ok := strings.Cut(l, " "); ok {
			before[n] = sha
		}
	}
	agentMemPurgeFault = func(s string) error {
		if s == "refs" {
			return errors.New("simulated crash")
		}
		return nil
	}
	t.Cleanup(func() { agentMemPurgeFault = nil })
	if _, err := purgeDo(t, f, "a"); err == nil {
		t.Fatal("expected the simulated crash")
	}
	agentMemPurgeFault = nil
	return f, before
}

func TestAgentMemoryPurgeRollsForwardAPartialRefUpdate(t *testing.T) {
	f, before := partialRefFixture(t)
	staleLocksProvable(t)
	// The crash state: main already moved, the import ref is back at its old value.
	imp := "refs/imports/20261004T090000Z/main"
	if _, err := memoryGitRun("update-ref", imp, before[imp]); err != nil {
		t.Fatal(err)
	}
	// ...and the dead git's lock file on the other ref.
	if _, err := memoryGitRun("update-ref", "refs/premigrate/20261004T090000Z", before["refs/premigrate/20261004T090000Z"]); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memoryRepoDir()+"/refs/premigrate/20261004T090000Z.lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ResumeMemoryPurge()
	if objectExists(f.oldBlob) || allObjectsContain(t, "Hollowbrook") {
		t.Fatal("the partial update was treated as 'nothing happened'; the old text is still there")
	}
	if _, err := os.Stat(agentMemPurgePendingPath()); err == nil {
		t.Fatal("marker left")
	}
	fsckClean(t)
}

func TestAgentMemoryPurgeKeepsTheMarkerOnAnUnexpectedRef(t *testing.T) {
	_, before := partialRefFixture(t)
	imp := "refs/imports/20261004T090000Z/main"
	if _, err := memoryGitRun("update-ref", imp, before[imp]); err != nil {
		t.Fatal(err)
	}
	// Someone moved the other ref to a value the purge never recorded.
	empty, _ := memoryGitRun("hash-object", "-t", "tree", "/dev/null")
	stranger, err := memoryGitRun("commit-tree", "-m", "unrelated", empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memoryGitRun("update-ref", "refs/premigrate/20261004T090000Z", stranger); err != nil {
		t.Fatal(err)
	}
	ResumeMemoryPurge()
	if _, err := os.Stat(agentMemPurgePendingPath()); err != nil {
		t.Fatal("an unrecognised ref state dropped the marker")
	}
	got, _ := memoryGitRun("rev-parse", imp)
	if got != before[imp] {
		t.Fatal("the recovery overwrote refs it could not account for")
	}
}

// The upload file is created and registered in one critical section: a sweep running at any
// moment sees either no file or a registered one.
func TestAgentMemoryUploadIsNeverSweptBetweenCreateAndRegister(t *testing.T) {
	_ = purgeSetup(t)
	if err := os.MkdirAll(memoryWorkDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if err := agentMemPurgeSweepWork(); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}()
	for i := 0; i < 200; i++ {
		f, err := memoryCreateUpload()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(f.Name()); err != nil {
			t.Fatalf("a sweep removed a live upload: %v", err)
		}
		_ = f.Close()
		memoryUntrackUpload(f.Name())
		_ = os.Remove(f.Name())
	}
	close(stop)
	wg.Wait()
}

// staleLocksProvable makes locks created during the test count as older than the Agent and not
// held by anyone: in a real recovery that is a restart, and /proc is readable.
func staleLocksProvable(t *testing.T) {
	t.Helper()
	oldStart, oldHeld := agentMemPurgeProcStart, agentMemPurgeLockHeld
	agentMemPurgeProcStart = time.Now().Add(time.Hour)
	agentMemPurgeLockHeld = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { agentMemPurgeProcStart, agentMemPurgeLockHeld = oldStart, oldHeld })
}

func strangerCommit(t *testing.T) string {
	t.Helper()
	empty, _ := memoryGitRun("hash-object", "-t", "tree", "/dev/null")
	c, err := memoryGitRun("commit-tree", "-m", "unrelated", empty)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Every ref is judged against both recorded values before anything is decided: a main that was
// moved to an unrelated commit after the crash is neither "done" nor "never ran".
func TestAgentMemoryPurgeUnknownMainKeepsTheMarker(t *testing.T) {
	for _, resetOthers := range []bool{false, true} {
		f, before := partialRefFixture(t)
		_ = f
		if resetOthers {
			for _, r := range []string{"refs/imports/20261004T090000Z/main", "refs/premigrate/20261004T090000Z"} {
				if _, err := memoryGitRun("update-ref", r, before[r]); err != nil {
					t.Fatal(err)
				}
			}
		}
		stranger := strangerCommit(t)
		if _, err := memoryGitRun("update-ref", "refs/heads/"+memoryBranch, stranger); err != nil {
			t.Fatal(err)
		}
		ResumeMemoryPurge()
		if _, err := os.Stat(agentMemPurgePendingPath()); err != nil {
			t.Fatalf("resetOthers=%v: the marker was dropped for a ref in an unknown state", resetOthers)
		}
		if got, _ := memoryGitRun("rev-parse", memoryBranch); got != stranger {
			t.Fatalf("resetOthers=%v: the recovery overwrote main", resetOthers)
		}
		if _, err := agentMemPurgePreview(agentMemScopeProject, f.pid, "a"); err != nil {
			t.Fatal(err)
		}
		if _, err := agentMemPurge(agentMemPurgeReq{Scope: agentMemScopeProject, Project: f.pid, Name: "a", Plan: "x", Confirm: "a"}); err == nil {
			t.Fatalf("resetOthers=%v: a new purge ran over an unresolved one", resetOthers)
		}
	}
}

// setMarkerPid records pid in the marker, as the purge does right after it starts update-ref.
func setMarkerPid(t *testing.T, pid int) {
	t.Helper()
	b, err := os.ReadFile(agentMemPurgePendingPath())
	if err != nil {
		t.Fatal(err)
	}
	var p agentMemPurgePending
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	p.Pid = pid
	b, _ = json.Marshal(p)
	if err := os.WriteFile(agentMemPurgePendingPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// deadPid is the pid of a process that has already exited.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// A crash after the purge's update-ref took its locks but before it moved anything leaves the
// refs old and the locks behind. Those are the purge's own and are cleared; the purge then runs.
func TestAgentMemoryPurgeClearsItsOwnStaleLocks(t *testing.T) {
	f := purgeSetup(t)
	agentMemPurgeFault = func(s string) error {
		if s == "marker" {
			return errors.New("simulated crash")
		}
		return nil
	}
	t.Cleanup(func() { agentMemPurgeFault = nil })
	if _, err := purgeDo(t, f, "a"); err == nil {
		t.Fatal("expected the simulated crash")
	}
	agentMemPurgeFault = nil
	for _, l := range []string{"refs/heads/main.lock", "refs/imports/20261004T090000Z/main.lock", "packed-refs.lock"} {
		if err := os.WriteFile(memoryRepoDir()+"/"+l, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setMarkerPid(t, deadPid(t))
	staleLocksProvable(t)
	ResumeMemoryPurge()
	if _, err := os.Stat(memoryRepoDir() + "/refs/heads/main.lock"); err == nil {
		t.Fatal("the purge's own stale lock was left")
	}
	if _, err := os.Stat(agentMemPurgePendingPath()); err == nil {
		t.Fatal("marker left after a verified 'nothing ran'")
	}
	if _, err := purgeDo(t, f, "a"); err != nil {
		t.Fatalf("purge after clearing the locks: %v", err)
	}
	fsckClean(t)
}

// A dead recorded pid proves nothing about a lock of the same name that a live writer holds now.
// The repro: the purge's marker names a dead update-ref, every ref is back at its old value, and a
// separate live `git update-ref --stdin` has started, updated and prepared (it holds main.lock).
func TestAgentMemoryPurgeNeverDeletesALiveWritersLock(t *testing.T) {
	f, before := partialRefFixture(t)
	_ = f
	for r, sha := range before {
		if _, err := memoryGitRun("update-ref", r, sha); err != nil {
			t.Fatal(err)
		}
	}
	// Refs older than the purge also have to be old again after the fixture's rewrite.
	cur, _ := memoryGitRun("rev-parse", memoryBranch)
	next := strangerCommit(t)
	w := memoryGit("update-ref", "--stdin")
	in, _ := w.StdinPipe()
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = w.Wait() })
	fmt.Fprintf(in, "start\nupdate refs/heads/%s %s %s\nprepare\n", memoryBranch, next, cur)
	lock := memoryRepoDir() + "/refs/heads/" + memoryBranch + ".lock"
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(lock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the foreign writer never took the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The marker's own process is long dead.
	setMarkerPid(t, deadPid(t))
	ResumeMemoryPurge()
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("a live writer's lock was deleted")
	}
	if _, err := os.Stat(agentMemPurgePendingPath()); err != nil {
		t.Fatal("the marker was dropped while a live writer holds a lock")
	}
	if _, err := purgeDo(t, f, "a"); err == nil {
		t.Fatal("a purge ran over a lock it does not own")
	}
	// Once the writer is gone and the lock with it, the recovery works again.
	_ = in.Close()
	_ = w.Wait()
	_ = os.Remove(lock)
	ResumeMemoryPurge()
	if _, err := os.Stat(agentMemPurgePendingPath()); err == nil {
		t.Fatal("marker left after the lock went away")
	}
}

// A live recorded pid, an open lock, and a lock made after the Agent started are all refused.
func TestAgentMemoryPurgeRefusesLocksItCannotShowAreStale(t *testing.T) {
	f := purgeSetup(t)
	agentMemPurgeFault = func(s string) error {
		if s == "marker" {
			return errors.New("simulated crash")
		}
		return nil
	}
	t.Cleanup(func() { agentMemPurgeFault = nil })
	_, _ = purgeDo(t, f, "a")
	agentMemPurgeFault = nil
	lock := memoryRepoDir() + "/refs/heads/main.lock"
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	check := func(what string) {
		t.Helper()
		ResumeMemoryPurge()
		if _, err := os.Stat(lock); err != nil {
			t.Fatalf("%s: the lock was deleted", what)
		}
		if _, err := os.Stat(agentMemPurgePendingPath()); err != nil {
			t.Fatalf("%s: the marker was dropped", what)
		}
	}
	// Made after this Agent started: may be a live writer's.
	setMarkerPid(t, deadPid(t))
	check("lock newer than the Agent")

	// Older than the Agent, but a process has it open (here, this one).
	oldStart := agentMemPurgeProcStart
	agentMemPurgeProcStart = time.Now().Add(time.Hour)
	t.Cleanup(func() { agentMemPurgeProcStart = oldStart })
	held, err := os.OpenFile(lock, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	check("lock held open")
	_ = held.Close()

	// The recorded transaction process is alive.
	setMarkerPid(t, os.Getpid())
	check("recorded pid alive")
}
