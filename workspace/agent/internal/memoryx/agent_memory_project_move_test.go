package memoryx

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// moveOrphan makes a second clone with memories in it and then deletes it, so its project is
// orphaned while "demo" (the test env's clone) stays live. It returns the orphaned project's
// caller (usable for saves before the delete) and its id.
func moveOrphan(t *testing.T, home string, names ...string) (orphan string, toID string) {
	t.Helper()
	old := filepath.Join(home, "repos", "old")
	memoryMkdirAll(t, old)
	cmd := exec.Command("git", "init", "--quiet", "-b", "main")
	cmd.Dir = old
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	session.WriteMeta(session.Meta{Name: "claude-old", Dir: old, Kind: "claude"})
	c := agentMemCallerT(t, "claude-old")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	for i, n := range names {
		agentMemSaveN(t, c, n, "project", now.Add(time.Duration(i)*time.Minute))
	}
	orphan = c.Project.ID
	if err := os.RemoveAll(old); err != nil {
		t.Fatal(err)
	}
	return orphan, agentMemCallerT(t, "claude-main").Project.ID
}

// movePreviewItems reads the preview over HTTP, as the Console does, and returns the items for
// names (a name the preview does not have is sent as a made-up item).
func movePreviewItems(t *testing.T, from, to, policy string, names ...string) []agentMemMoveReqItem {
	t.Helper()
	q := "from=" + from + "&to=" + to
	if policy != "" {
		q += "&collision=" + policy
	}
	code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/project-move-preview?"+q, nil)
	var pv agentMemMovePreview
	if code != http.StatusOK || json.Unmarshal(b, &pv) != nil {
		// A refused preview: send made-up items so the apply's own refusal is what is tested.
		pv = agentMemMovePreview{}
	}
	var out []agentMemMoveReqItem
	for _, n := range names {
		it := agentMemMoveReqItem{Name: n, Status: "move", Revision: 1}
		for _, p := range pv.Items {
			if p.Name == n {
				it = agentMemMoveReqItem{Name: p.Name, NewName: p.NewName, Status: p.Status, Revision: p.Revision}
			}
		}
		out = append(out, it)
	}
	return out
}

func moveBody(from, to, policy string, items []agentMemMoveReqItem) string {
	b, _ := json.Marshal(agentMemMoveReq{From: from, To: to, Collision: policy, Items: items})
	return string(b)
}

// moveDoItems posts a move built from items previewed earlier.
func moveDoItems(t *testing.T, from, to, policy string, items []agentMemMoveReqItem) (int, agentMemMoveResult, []byte) {
	t.Helper()
	w := markedDo(t, buildMux(), http.MethodPost, "/agents/memory/entries/project-move?project="+from, moveBody(from, to, policy, items))
	var res agentMemMoveResult
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	return w.Code, res, w.Body.Bytes()
}

// moveDo previews and then applies the named memories, as the Console does.
func moveDo(t *testing.T, from, to, policy string, names ...string) (int, agentMemMoveResult, []byte) {
	t.Helper()
	return moveDoItems(t, from, to, policy, movePreviewItems(t, from, to, policy, names...))
}

func projectNames(t *testing.T, id string) map[string]agentMemEntry {
	t.Helper()
	es, _, err := agentMemLoadDir(agentMemScopeProject, "projects/"+id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]agentMemEntry{}
	for _, e := range es {
		out[e.Name] = e
	}
	return out
}

// A root is called gone only on evidence. Each "in doubt" row is a false positive that would
// offer to move a live project's memories.
func TestAgentMemoryRootGoneOnlyOnEvidence(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	repos := filepath.Join(home, "repos")
	if agentMemRootGone(filepath.Join(repos, "demo")) {
		t.Error("an existing root is not gone")
	}
	if !agentMemRootGone(filepath.Join(repos, "missing")) {
		t.Error("a missing root under a populated ~/repos is gone")
	}
	if agentMemRootGone("") || agentMemRootGone("relative/path") {
		t.Error("a root that is not an absolute path is unknown")
	}
	if agentMemRootGone(filepath.Join(home, "elsewhere", "app")) || agentMemRootGone(repos) {
		t.Error("a root outside ~/repos is another mount's: unknown")
	}
	// A symlink on the way (a /scratch link left dangling by a stop) is unknown, not gone.
	if err := os.Symlink(filepath.Join(home, "no-such-disk"), filepath.Join(repos, "scratchy")); err != nil {
		t.Fatal(err)
	}
	if agentMemRootGone(filepath.Join(repos, "scratchy")) || agentMemRootGone(filepath.Join(repos, "scratchy", "app")) {
		t.Error("a dangling symlink is unknown")
	}
	// A file where a directory should be is odd, not gone.
	memoryWrite(t, filepath.Join(repos, "afile"), "x")
	if agentMemRootGone(filepath.Join(repos, "afile", "app")) {
		t.Error("a path through a file is unknown")
	}
	// An empty ~/repos looks like an unmounted one.
	empty := t.TempDir()
	t.Setenv("HOME", empty)
	memoryMkdirAll(t, filepath.Join(empty, "repos"))
	if agentMemRootGone(filepath.Join(empty, "repos", "demo")) {
		t.Error("an empty ~/repos must not orphan everything")
	}
	t.Setenv("HOME", filepath.Join(empty, "nope"))
	if agentMemRootGone(filepath.Join(empty, "nope", "repos", "demo")) {
		t.Error("a missing ~/repos must not orphan everything")
	}
}

// The list flags a project whose root is gone, and never one with a working copy or a worktree.
func TestAgentMemoryProjectsListFlagsOrphans(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	main := agentMemCallerT(t, "claude-main")
	agentMemSaveN(t, main, "live-one", "project", time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
	orphan, _ := moveOrphan(t, home, "a", "b")

	out, err := agentMemMemberProjects()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]agentMemProjectRow{}
	for _, r := range out.Projects {
		got[r.Project.ID] = r
	}
	if r := got[orphan]; !r.Orphaned || r.Count != 2 || r.Live {
		t.Errorf("orphan row = %+v", r)
	}
	if r := got[main.Project.ID]; r.Orphaned || r.Count != 1 || !r.Live {
		t.Errorf("live row = %+v", r)
	}
	if !out.Projects[0].Orphaned {
		t.Errorf("orphans sort first: %+v", out.Projects)
	}
	// Over HTTP, without a mark: a read.
	code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/projects", nil)
	if code != http.StatusOK || !strings.Contains(string(b), orphan) {
		t.Fatalf("GET projects: %d %s", code, b)
	}
}

// A move puts the memories in the target and removes them from the source, as two published
// changes each, both undoable through the changes list.
func TestAgentMemoryMoveProject(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	orphan, to := moveOrphan(t, home, "a", "b")

	code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/project-move-preview?from="+orphan+"&to="+to, nil)
	var pv agentMemMovePreview
	if code != http.StatusOK || json.Unmarshal(b, &pv) != nil || pv.Counts["move"] != 2 || pv.NeedsPolicy {
		t.Fatalf("preview: %d %s", code, b)
	}
	if len(projectNames(t, to)) != 0 {
		t.Fatal("the preview wrote")
	}

	code, res, b := moveDo(t, orphan, to, "", "a", "b")
	if code != http.StatusOK || len(res.Results) != 2 || res.Results[0].Result != "moved" {
		t.Fatalf("move: %d %s", code, b)
	}
	if got := projectNames(t, to); len(got) != 2 || got["a"].Body != "b" || got["a"].AuthorSession != "claude-old" || got["a"].Revision != 1 {
		t.Fatalf("target = %+v", got)
	}
	if got := projectNames(t, orphan); len(got) != 0 {
		t.Fatalf("source still holds %+v", got)
	}
	// The target got its project record from the working copy.
	if info := agentMemProjectInfo(to); info.Root == "" {
		t.Errorf("target project.json = %+v", info)
	}

	ch, err := agentMemListChanges(20)
	if err != nil {
		t.Fatal(err)
	}
	var creates, forgets int
	for _, c := range ch.Changes {
		if c.AuthorKind != "member" {
			continue
		}
		switch {
		case c.Op == "create" && c.Project.ID == to:
			creates++
		case c.Op == "forget" && c.Project.ID == orphan:
			forgets++
		}
	}
	if creates != 2 || forgets != 2 {
		t.Fatalf("changes: %d creates in the target, %d forgets in the source: %+v", creates, forgets, ch.Changes)
	}
	body, _ := memoryGitRun("log", "-1", "--format=%B", "--grep=AF-Moved-To")
	if !strings.Contains(body, "AF-Moved-To: projects/"+to+"/") {
		t.Errorf("a forget does not say where the memory went: %q", body)
	}

	// Undo "a" through the changes list: the forget in the source brings it back, the create in
	// the target takes the copy out.
	for _, c := range ch.Changes {
		if c.Name != "a" || !c.Latest {
			continue
		}
		if _, err := agentMemRevert(agentMemRevertReq{Commit: c.Commit}, time.Now()); err != nil {
			t.Fatalf("revert %s %s: %v", c.Op, c.Project.ID, err)
		}
	}
	if len(projectNames(t, orphan)) != 1 || len(projectNames(t, to)) != 1 {
		t.Fatalf("after undo: source %v, target %v", projectNames(t, orphan), projectNames(t, to))
	}
	if _, ok := projectNames(t, orphan)["a"]; !ok {
		t.Fatal("undo did not restore the source copy")
	}
}

// Merging into a project that already holds a name: nothing is overwritten, and the member
// chooses. No policy is an error, never a guess.
func TestAgentMemoryMoveCollisions(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	main := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	// "a" differs from the orphan's; "same" is identical to it (agentMemSaveN writes the same text).
	if _, err := agentMemSave(main, agentMemSaveReq{Name: "a", Description: "target's own", Type: "project", Body: "keep me"}, now); err != nil {
		t.Fatal(err)
	}
	agentMemSaveN(t, main, "same", "project", now)
	orphan, to := moveOrphan(t, home, "a", "same", "free")

	_, b := memberDo(t, http.MethodGet, "/agents/memory/entries/project-move-preview?from="+orphan+"&to="+to, nil)
	var pv agentMemMovePreview
	if json.Unmarshal(b, &pv) != nil || !pv.NeedsPolicy || pv.Counts["collision"] != 1 || pv.Counts["same"] != 1 || pv.Counts["move"] != 1 {
		t.Fatalf("preview without a policy: %s", b)
	}
	if code, _, b := moveDo(t, orphan, to, "", "a", "same", "free"); code != http.StatusBadRequest {
		t.Fatalf("a move with a collision and no policy: %d %s", code, b)
	}
	if len(projectNames(t, orphan)) != 3 {
		t.Fatal("a refused move changed the source")
	}

	// skip: the colliding memory stays in the source, the others go.
	code, res, b := moveDo(t, orphan, to, "skip", "a", "same", "free")
	if code != http.StatusOK {
		t.Fatalf("skip: %d %s", code, b)
	}
	got := map[string]string{}
	for _, r := range res.Results {
		got[r.Name] = r.Result
	}
	if got["a"] != "skipped" || got["same"] != "merged" || got["free"] != "moved" {
		t.Fatalf("skip results = %v", got)
	}
	if src := projectNames(t, orphan); len(src) != 1 || src["a"].Description != "d a" {
		t.Fatalf("source after skip = %v", src)
	}
	if dst := projectNames(t, to); dst["a"].Body != "keep me" || len(dst) != 3 {
		t.Fatalf("the target's own memory was overwritten or the move missed: %v", dst)
	}

	// rename: the remaining collision moves under a new name.
	code, res, b = moveDo(t, orphan, to, "rename", "a")
	if code != http.StatusOK || len(res.Results) != 1 || res.Results[0].Result != "renamed" || res.Results[0].NewName != "a-moved" {
		t.Fatalf("rename: %d %s", code, b)
	}
	dst := projectNames(t, to)
	if dst["a"].Body != "keep me" || dst["a-moved"].Description != "d a" || dst["a-moved"].Name != "a-moved" {
		t.Fatalf("after rename: %v", dst)
	}
	if len(projectNames(t, orphan)) != 0 {
		t.Fatal("the renamed memory is still in the source")
	}
}

// A move that stops between the two changes of a memory loses nothing, and running it again
// finishes without a duplicate. Without the create-first order a failure here would delete the
// only copy.
func TestAgentMemoryMoveStoppedHalfwayResumes(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	orphan, to := moveOrphan(t, home, "a", "b", "c")
	calls := 0
	old := agentMemMoveStep
	agentMemMoveStep = func(step, name string) error {
		calls++
		if calls == 2 {
			return errors.New("injected stop")
		}
		return nil
	}
	t.Cleanup(func() { agentMemMoveStep = old })

	code, _, b := moveDo(t, orphan, to, "", "a", "b", "c")
	if code != http.StatusInternalServerError || !strings.Contains(string(b), "nothing was lost") {
		t.Fatalf("stopped move: %d %s", code, b)
	}
	src, dst := projectNames(t, orphan), projectNames(t, to)
	if len(src) != 2 || len(dst) != 2 {
		t.Fatalf("after the stop: source %v, target %v", src, dst)
	}
	if _, ok := src["b"]; !ok {
		t.Fatalf("the memory in flight left the source: %v", src)
	}
	if _, ok := dst["b"]; !ok {
		t.Fatalf("the memory in flight is not in the target: %v", dst)
	}

	agentMemMoveStep = old
	_, b = memberDo(t, http.MethodGet, "/agents/memory/entries/project-move-preview?from="+orphan+"&to="+to, nil)
	var pv agentMemMovePreview
	if json.Unmarshal(b, &pv) != nil || pv.Counts["same"] != 1 || pv.Counts["move"] != 1 || pv.NeedsPolicy {
		t.Fatalf("resume preview: %s", b)
	}
	code, res, b := moveDo(t, orphan, to, "", "b", "c")
	if code != http.StatusOK || res.Results[0].Result != "merged" || res.Results[1].Result != "moved" {
		t.Fatalf("resume: %d %s", code, b)
	}
	if src, dst := projectNames(t, orphan), projectNames(t, to); len(src) != 0 || len(dst) != 3 {
		t.Fatalf("after resume: source %v, target %v", src, dst)
	}
}

// A rename interrupted after its create is finished by the next run under the same name, not
// copied a second time.
func TestAgentMemoryMoveRenameResumesWithoutDuplicate(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	main := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(main, agentMemSaveReq{Name: "a", Description: "mine", Type: "project", Body: "mine"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	orphan, to := moveOrphan(t, home, "a")
	old := agentMemMoveStep
	agentMemMoveStep = func(string, string) error { return errors.New("injected stop") }
	t.Cleanup(func() { agentMemMoveStep = old })
	if code, _, b := moveDo(t, orphan, to, "rename", "a"); code != http.StatusInternalServerError {
		t.Fatalf("stopped rename: %d %s", code, b)
	}
	agentMemMoveStep = old
	code, res, b := moveDo(t, orphan, to, "rename", "a")
	if code != http.StatusOK || res.Results[0].Result != "merged" || res.Results[0].NewName != "a-moved" {
		t.Fatalf("resume: %d %s", code, b)
	}
	if dst := projectNames(t, to); len(dst) != 2 || dst["a-moved"].Name == "" || dst["a"].Body != "mine" {
		t.Fatalf("target = %v", dst)
	}
}

// What changed since the preview stays where it is, and a project with a working copy is never
// a source.
func TestAgentMemoryMoveRefusals(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	main := agentMemCallerT(t, "claude-main")
	agentMemSaveN(t, main, "live-one", "project", time.Now())
	orphan, to := moveOrphan(t, home, "a", "b")

	// A live project cannot be moved out of.
	if code, _, b := moveDo(t, to, orphan, "", "live-one"); code != http.StatusConflict {
		t.Fatalf("moving a live project: %d %s", code, b)
	}
	if code, _, b := moveDo(t, orphan, orphan, "", "a"); code != http.StatusBadRequest {
		t.Fatalf("into itself: %d %s", code, b)
	}
	if code, _, b := moveDo(t, orphan, "no-such-0123456789ab", "", "a"); code != http.StatusNotFound {
		t.Fatalf("unknown target: %d %s", code, b)
	}
	if code, _, b := moveDo(t, orphan, to, "overwrite", "a"); code != http.StatusBadRequest {
		t.Fatalf("unknown policy: %d %s", code, b)
	}
	if code, _, b := moveDoItems(t, orphan, to, "", nil); code != http.StatusBadRequest {
		t.Fatalf("no names: %d %s", code, b)
	}
	if code, _, b := moveDo(t, orphan, to, "", "../x"); code != http.StatusBadRequest {
		t.Fatalf("bad name: %d %s", code, b)
	}

	// A name that is not in the source (changed or already moved) is reported, not an error.
	code, res, b := moveDo(t, orphan, to, "", "ghost", "a")
	if code != http.StatusOK || res.Results[0].Result != "skipped" || res.Results[1].Result != "moved" {
		t.Fatalf("ghost: %d %s", code, b)
	}

	// A memory updated between plan and write is skipped by the per-memory check.
	plan := func() (agentMemMovePlan, agentMemMoveItem) {
		live, complete := agentMemLiveProjects()
		agentMemMu.RLock()
		defer agentMemMu.RUnlock()
		p, err := agentMemMovePlanLocked(orphan, to, "", live, complete)
		if err != nil {
			t.Fatal(err)
		}
		return p, p.Items[0]
	}
	p, it := plan()
	oc := agentMemCallerT(t, "claude-main")
	oc.Project = agentMemProjectInfo(orphan)
	if _, err := agentMemSave(oc, agentMemSaveReq{Name: it.Name, Description: "newer", Type: "project", Body: "newer", Revision: it.Revision}, time.Now()); err != nil {
		t.Fatal(err)
	}
	r, err := agentMemMoveOne(p, it, time.Now())
	if err != nil || r.Result != "skipped" || r.Reason != "changed" {
		t.Fatalf("stale item: %+v %v", r, err)
	}
	if _, ok := projectNames(t, orphan)["b"]; !ok {
		t.Fatal("the changed memory left the source")
	}
}

// A file that fails the scan is neither listed nor moved, and the preview counts it.
func TestAgentMemoryMoveLeavesWithheldFilesAlone(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	orphan, to := moveOrphan(t, home, "a", "leaky")
	f := filepath.Join(agentMemDir(), "projects", orphan, "leaky.md")
	raw, _ := os.ReadFile(f)
	secret := agentMemFakeAWS()
	if err := os.WriteFile(f, []byte(strings.Replace(string(raw), "\nb\n", "\n"+secret+"\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/project-move-preview?from="+orphan+"&to="+to, nil)
	var pv agentMemMovePreview
	if code != http.StatusOK || json.Unmarshal(b, &pv) != nil || pv.Withheld != 1 || len(pv.Items) != 1 || strings.Contains(string(b), secret) {
		t.Fatalf("preview: %d %s", code, b)
	}
	if code, _, b := moveDo(t, orphan, to, "", "a", "leaky"); code != http.StatusOK || strings.Contains(string(b), secret) {
		t.Fatalf("move: %d %s", code, b)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatal("the withheld file was removed")
	}
	if _, ok := projectNames(t, to)["leaky"]; ok {
		t.Fatal("the withheld file was moved")
	}
}

// A name that appears in the target after the plan was made is never overwritten.
func TestAgentMemoryMoveNeverOverwritesALateArrival(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	orphan, to := moveOrphan(t, home, "a")
	live, complete := agentMemLiveProjects()
	agentMemMu.RLock()
	p, err := agentMemMovePlanLocked(orphan, to, "", live, complete)
	agentMemMu.RUnlock()
	if err != nil || len(p.Items) != 1 || p.Items[0].Status != "move" {
		t.Fatalf("plan: %+v %v", p, err)
	}
	main := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(main, agentMemSaveReq{Name: "a", Description: "late", Type: "project", Body: "late"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	r, err := agentMemMoveOne(p, p.Items[0], time.Now())
	if err != nil || r.Result != "skipped" || r.Reason != "exists" {
		t.Fatalf("late arrival: %+v %v", r, err)
	}
	if projectNames(t, to)["a"].Body != "late" || len(projectNames(t, orphan)) != 1 {
		t.Fatal("the target's memory was overwritten or the source lost its copy")
	}
}

// The apply is bound to the preview over HTTP: a memory updated after the preview is not moved
// on the strength of the old screen. Without the revision in the request the apply planned from
// the newest text and moved a revision nobody had seen.
func TestAgentMemoryMoveApplyIsBoundToThePreview(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	orphan, to := moveOrphan(t, home, "a")
	items := movePreviewItems(t, orphan, to, "", "a")
	oc := agentMemCallerT(t, "claude-main")
	oc.Project = agentMemProjectInfo(orphan)
	if _, err := agentMemSave(oc, agentMemSaveReq{Name: "a", Description: "newer", Type: "project", Body: "unseen", Revision: 1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	code, res, b := moveDoItems(t, orphan, to, "", items)
	if code != http.StatusOK || res.Results[0].Result != "skipped" || res.Results[0].Reason != "changed" {
		t.Fatalf("apply of a stale preview: %d %s", code, b)
	}
	if src := projectNames(t, orphan); src["a"].Body != "unseen" || len(projectNames(t, to)) != 0 {
		t.Fatalf("source %v, target %v", src, projectNames(t, to))
	}
}

// A rename target the member confirmed is the one used: a name that was taken after the preview
// does not make the apply pick another one.
func TestAgentMemoryMoveRenameIsBoundToThePreview(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	main := agentMemCallerT(t, "claude-main")
	if _, err := agentMemSave(main, agentMemSaveReq{Name: "a", Description: "mine", Type: "project", Body: "mine"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	orphan, to := moveOrphan(t, home, "a")
	items := movePreviewItems(t, orphan, to, "rename", "a")
	if items[0].NewName != "a-moved" {
		t.Fatalf("preview = %+v", items)
	}
	if _, err := agentMemSave(main, agentMemSaveReq{Name: "a-moved", Description: "late", Type: "project", Body: "late"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	code, res, b := moveDoItems(t, orphan, to, "rename", items)
	if code != http.StatusOK || res.Results[0].Result != "skipped" {
		t.Fatalf("apply: %d %s", code, b)
	}
	if _, ok := projectNames(t, to)["a-moved-2"]; ok || len(projectNames(t, orphan)) != 1 {
		t.Fatal("the apply moved under a name the member did not confirm")
	}
}

// The pin and the import provenance are part of what makes two copies the same. A pinned source
// beside an unpinned target copy with the same text is a collision, and its rename keeps the pin.
func TestAgentMemoryMovePinAndProvenanceAreNotLost(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	main := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	agentMemSaveN(t, main, "a", "project", now)
	orphan, to := moveOrphan(t, home, "a")
	if _, err := agentMemPin(agentMemPinReq{Scope: "project", Project: orphan, Name: "a", Pinned: true}, now); err != nil {
		t.Fatal(err)
	}
	_, b := memberDo(t, http.MethodGet, "/agents/memory/entries/project-move-preview?from="+orphan+"&to="+to, nil)
	var pv agentMemMovePreview
	if json.Unmarshal(b, &pv) != nil || pv.Counts["collision"] != 1 || pv.Counts["same"] != 0 {
		t.Fatalf("a pinned source is not the same as an unpinned copy: %s", b)
	}
	code, res, b := moveDo(t, orphan, to, "rename", "a")
	if code != http.StatusOK || res.Results[0].Result != "renamed" {
		t.Fatalf("rename: %d %s", code, b)
	}
	if dst := projectNames(t, to); !dst["a-moved"].Pinned || dst["a"].Pinned {
		t.Fatalf("pin after the move: %+v", dst)
	}

	// Provenance: the same text with another import source is a collision too.
	if !agentMemMoveSame(agentMemEntry{Body: "x", Source: "s", SourceHash: "h"}, agentMemEntry{Body: "x", Source: "s", SourceHash: "h"}) ||
		agentMemMoveSame(agentMemEntry{Body: "x", Source: "s", SourceHash: "h"}, agentMemEntry{Body: "x"}) {
		t.Fatal("import provenance must take part in the comparison")
	}
}

// A live working copy nested below ~/repos, with its git metadata elsewhere, keeps its project
// from being called orphaned. The search used to read only the first level, saw "group" as a
// different project and let the move take the live project's memories out.
func TestAgentMemoryMoveRefusesAProjectWithANestedLiveWorktree(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	old := filepath.Join(home, "repos", "old")
	memoryMkdirAll(t, old)
	memoryMkdirAll(t, filepath.Join(home, "git-metadata"))
	git(old, "init", "--quiet", "-b", "main", "--separate-git-dir="+filepath.Join(home, "git-metadata", "old"))
	git(old, "commit", "--quiet", "--allow-empty", "-m", "init")
	session.WriteMeta(session.Meta{Name: "claude-old", Dir: old, Kind: "claude"})
	c := agentMemCallerT(t, "claude-old")
	agentMemSaveN(t, c, "a", "project", time.Now())
	nested := filepath.Join(home, "repos", "group", "live")
	memoryMkdirAll(t, filepath.Dir(nested))
	git(old, "worktree", "add", "--quiet", "-b", "nested", nested)
	if err := os.RemoveAll(old); err != nil {
		t.Fatal(err)
	}
	if p := agentMemProjectFor(nested); p == nil || p.ID != c.Project.ID {
		t.Fatalf("setup: the nested worktree is not the same project: %+v", p)
	}
	_, to := c.Project.ID, agentMemCallerT(t, "claude-main").Project.ID
	if code, _, b := moveDo(t, c.Project.ID, to, "", "a"); code != http.StatusConflict {
		t.Fatalf("moving out of a project with a live nested worktree: %d %s", code, b)
	}
	out, _ := agentMemMemberProjects()
	for _, r := range out.Projects {
		if r.Project.ID == c.Project.ID && r.Orphaned {
			t.Fatal("listed as orphaned")
		}
	}
}

// A move that breaks off answers with what was done: the finished memory, the half-done one with
// its create commit, and the ones not tried.
func TestAgentMemoryMoveStoppedAnswerCarriesTheResults(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	orphan, to := moveOrphan(t, home, "a", "b", "c")
	calls := 0
	old := agentMemMoveStep
	agentMemMoveStep = func(string, string) error {
		if calls++; calls == 2 {
			return errors.New("injected stop")
		}
		return nil
	}
	t.Cleanup(func() { agentMemMoveStep = old })
	code, res, b := moveDo(t, orphan, to, "", "a", "b", "c")
	if code != http.StatusInternalServerError || res.Stopped != "b" || len(res.Pending) != 1 || res.Pending[0] != "c" {
		t.Fatalf("stopped answer: %d %s", code, b)
	}
	if len(res.Results) != 2 || res.Results[0].Result != "moved" || res.Results[1].Result != "partial" || res.Results[1].Commit == "" {
		t.Fatalf("results = %+v", res.Results)
	}
	if !strings.Contains(string(b), `"error"`) || !strings.Contains(string(b), "nothing was lost") {
		t.Fatalf("the error is missing: %s", b)
	}
}

// nestedWorktreeFixture makes a project "old" (git metadata outside ~/repos) with a memory, adds a
// live worktree of it at repos/<rel>, removes old and returns the orphan candidate's id and the
// target's id.
func nestedWorktreeFixture(t *testing.T, home, rel string) (orphan, to string) {
	t.Helper()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	old := filepath.Join(home, "repos", "old")
	memoryMkdirAll(t, old)
	memoryMkdirAll(t, filepath.Join(home, "git-metadata"))
	git(old, "init", "--quiet", "-b", "main", "--separate-git-dir="+filepath.Join(home, "git-metadata", "old"))
	git(old, "commit", "--quiet", "--allow-empty", "-m", "init")
	session.WriteMeta(session.Meta{Name: "claude-old", Dir: old, Kind: "claude"})
	c := agentMemCallerT(t, "claude-old")
	agentMemSaveN(t, c, "a", "project", time.Now())
	nested := filepath.Join(home, "repos", filepath.FromSlash(rel))
	memoryMkdirAll(t, filepath.Dir(nested))
	git(old, "worktree", "add", "--quiet", "-b", "nested", nested)
	if err := os.RemoveAll(old); err != nil {
		t.Fatal(err)
	}
	if p := agentMemProjectFor(nested); p == nil || p.ID != c.Project.ID {
		t.Fatalf("setup: the nested worktree is not the same project: %+v", p)
	}
	return c.Project.ID, agentMemCallerT(t, "claude-main").Project.ID
}

func assertNotOrphaned(t *testing.T, orphan, to string) {
	t.Helper()
	if code, _, b := moveDo(t, orphan, to, "", "a"); code != http.StatusConflict {
		t.Fatalf("moving out of a project that may have a live working copy: %d %s", code, b)
	}
	out, _ := agentMemMemberProjects()
	for _, r := range out.Projects {
		if r.Project.ID == orphan && r.Orphaned {
			t.Fatal("listed as orphaned")
		}
	}
	if len(projectNames(t, orphan)) != 1 {
		t.Fatal("the memory left the source")
	}
}

// A live worktree placed inside another live repository is found: the search goes into working
// copies too. Stopping at the first .git hid it.
func TestAgentMemoryMoveRefusesAWorktreeInsideAnotherRepo(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	orphan, to := nestedWorktreeFixture(t, home, "demo/live")
	live, _ := agentMemLiveProjects()
	if live[orphan] == nil {
		t.Fatal("the worktree inside demo was not found")
	}
	assertNotOrphaned(t, orphan, to)
}

// A directory that cannot be read leaves the answer incomplete, so nothing is orphaned: an
// unreadable subtree may hold the live working copy.
func TestAgentMemoryMoveUnreadableDirectoryMakesTheSearchIncomplete(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	home, _, _ := agentMemTestEnv(t)
	orphan, to := nestedWorktreeFixture(t, home, "group/live")
	group := filepath.Join(home, "repos", "group")
	if err := os.Chmod(group, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(group, 0o755) })
	if _, err := os.ReadDir(group); err == nil {
		t.Skip("directory permissions are not enforced here")
	}
	if _, complete := agentMemLiveProjects(); complete {
		t.Fatal("an unreadable directory must make the search incomplete")
	}
	assertNotOrphaned(t, orphan, to)
}

// The budget and the time of the search are limits: past either, nothing is orphaned.
func TestAgentMemoryLiveSearchLimits(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	memoryMkdirAll(t, filepath.Join(home, "repos", "a", "b", "c"))
	oldB, oldT := agentMemLiveBudget, agentMemLiveTimeout
	t.Cleanup(func() { agentMemLiveBudget, agentMemLiveTimeout = oldB, oldT })
	if _, complete := agentMemLiveProjects(); !complete {
		t.Fatal("a small tree is searched completely")
	}
	agentMemLiveBudget = 2
	if _, complete := agentMemLiveProjects(); complete {
		t.Fatal("a search past its budget must say so")
	}
	agentMemLiveBudget, agentMemLiveTimeout = oldB, time.Nanosecond
	if _, complete := agentMemLiveProjects(); complete {
		t.Fatal("a search past its time must say so")
	}
}

// A directory is not ruled out by its name, except the dependency stores of agentMemLiveSkip:
// build/ is a fine repository name, and a worktree can sit inside another directory called build.
func TestAgentMemoryMoveDoesNotSkipDirectoriesByName(t *testing.T) {
	for _, rel := range []string{"build", "demo/build/live", "dist/x"} {
		t.Run(rel, func(t *testing.T) {
			home, _, _ := agentMemTestEnv(t)
			orphan, to := nestedWorktreeFixture(t, home, rel)
			live, _ := agentMemLiveProjects()
			if live[orphan] == nil {
				t.Fatalf("the working copy at %s was not found", rel)
			}
			assertNotOrphaned(t, orphan, to)
		})
	}
}
