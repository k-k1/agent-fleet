package memoryx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func agentMemIndexNames(t *testing.T, c agentMemCaller, budget int) (agentMemIndex, string) {
	t.Helper()
	idx, err := agentMemListIndex(c, budget)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range idx.Entries {
		names = append(names, e.Name)
	}
	return idx, strings.Join(names, ",")
}

func agentMemSaveN(t *testing.T, c agentMemCaller, name, typ string, at time.Time) {
	t.Helper()
	if _, err := agentMemSave(c, agentMemSaveReq{Name: name, Description: "d " + name, Type: typ, Body: "b"}, at); err != nil {
		t.Fatal(err)
	}
}

// Reads and returned search hits count as uses, and the index ranks the more used first within a
// tier (#1703). Without the counting every project memory is ordered by recency alone.
func TestAgentMemoryUsageRanksIndex(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	agentMemSaveN(t, c, "newest", "project", now.Add(2*time.Hour))
	agentMemSaveN(t, c, "middle", "project", now.Add(time.Hour))
	agentMemSaveN(t, c, "oldest", "project", now)
	if _, names := agentMemIndexNames(t, c, 0); names != "newest,middle,oldest" {
		t.Fatalf("before any use: %s", names)
	}

	mux := buildMux()
	for i := 0; i < 3; i++ {
		if w := smokeDo(t, mux, http.MethodGet, "/agents/memory/entries/read?session=claude-main&name=oldest", "", ""); w.Code != http.StatusOK {
			t.Fatalf("read %d: %s", w.Code, w.Body)
		}
	}
	// A search hit counts too; a miss and a failed read do not.
	smokeDo(t, mux, http.MethodGet, "/agents/memory/entries/search?session=claude-main&q=middle", "", "")
	smokeDo(t, mux, http.MethodGet, "/agents/memory/entries/read?session=claude-main&name=absent", "", "")
	idx, names := agentMemIndexNames(t, c, 0)
	if names != "oldest,middle,newest" {
		t.Fatalf("after uses: %s", names)
	}
	for _, e := range idx.Entries {
		if e.Uses != 0 || e.Pinned {
			t.Errorf("ranking input leaked into the answer: %+v", e)
		}
	}

	// The Console's list is not a use, and forgetting clears the count for a re-created name.
	before := agentMemLoadUsage("projects/" + c.Project.ID)
	smokeDo(t, mux, http.MethodGet, "/agents/memory/entries/list", "", "")
	if after := agentMemLoadUsage("projects/" + c.Project.ID); after["oldest"] != before["oldest"] || before["oldest"] != 3 {
		t.Fatalf("usage before %v after list %v", before, after)
	}
	if _, err := agentMemForget(c, agentMemForgetReq{Name: "oldest", Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	agentMemSaveN(t, c, "oldest", "project", now)
	if _, names := agentMemIndexNames(t, c, 0); names != "middle,newest,oldest" {
		t.Fatalf("a re-created memory inherited its old count: %s", names)
	}
}

// Many concurrent readers lose no use: the count is the sidecar's size, one atomic append each.
func TestAgentMemoryUsageConcurrentReadsLoseNothing(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	agentMemSaveN(t, c, "hot", "project", time.Now())
	rel := "projects/" + c.Project.ID
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				agentMemRecordUse(rel, "hot")
			}
		}()
	}
	wg.Wait()
	if got := agentMemLoadUsage(rel)["hot"]; got != 1000 {
		t.Fatalf("uses = %d, want 1000", got)
	}
	// Bounded: a runaway count stops at the cap.
	for i := 0; i < agentMemUsageCap+100; i++ {
		agentMemRecordUse(rel, "hot")
	}
	if got := agentMemLoadUsage(rel)["hot"]; got != agentMemUsageCap {
		t.Fatalf("uses = %d, want cap %d", got, agentMemUsageCap)
	}
}

// A symlink planted as a sidecar is not written through.
func TestAgentMemoryUsageRefusesSymlink(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	agentMemSaveN(t, c, "victim", "project", time.Now())
	rel := "projects/" + c.Project.ID
	dir, err := agentMemCheckDir(rel+"/"+agentMemUsageDir, true)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "outside")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "victim")); err != nil {
		t.Fatal(err)
	}
	agentMemRecordUse(rel, "victim")
	if b, _ := os.ReadFile(target); string(b) != "x" {
		t.Fatalf("wrote through a symlink: %q", b)
	}
	if got := agentMemLoadUsage(rel)["victim"]; got != 0 {
		t.Fatalf("a symlink counted as %d uses", got)
	}
}

// A pin sits ahead of everything, survives an agent's update, shows in history as a "pin" change
// by the member, and unpinning puts the order back.
func TestAgentMemoryPinRanksFirstAndSurvivesUpdate(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	agentMemSaveN(t, c, "fb", "feedback", now.Add(3*time.Hour))
	agentMemSaveN(t, c, "ref", "reference", now)
	if _, names := agentMemIndexNames(t, c, 0); names != "fb,ref" {
		t.Fatalf("unpinned: %s", names)
	}
	res, err := agentMemPin(agentMemPinReq{Scope: "project", Project: c.Project.ID, Name: "ref", Pinned: true}, now.Add(4*time.Hour))
	if err != nil || res.Commit == "" || res.Revision != 1 {
		t.Fatalf("pin = %+v, %v", res, err)
	}
	if _, names := agentMemIndexNames(t, c, 0); names != "ref,fb" {
		t.Fatalf("pinned: %s", names)
	}
	msg, _ := memoryGitRun("log", "-1", "--format=%B", memoryBranch)
	for _, want := range []string{"AF-Op: pin", "AF-Author-Kind: member"} {
		if !strings.Contains(msg, want) {
			t.Errorf("pin commit lacks %q:\n%s", want, msg)
		}
	}
	// Pinning again is a no-op: no new commit.
	again, err := agentMemPin(agentMemPinReq{Scope: "project", Project: c.Project.ID, Name: "ref", Pinned: true}, now)
	if err != nil || again.Commit != "" {
		t.Fatalf("repeat pin = %+v, %v", again, err)
	}
	// The agent updates the memory with the revision it read; the pin is carried forward.
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "ref", Description: "d2", Type: "reference", Body: "b2", Revision: 1}, now.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, names := agentMemIndexNames(t, c, 0); names != "ref,fb" {
		t.Fatalf("pin lost on update: %s", names)
	}
	// Reverting that update keeps the pin; reverting the pin itself unpins.
	if _, err := agentMemPin(agentMemPinReq{Scope: "project", Project: c.Project.ID, Name: "ref", Pinned: false}, now.Add(6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, names := agentMemIndexNames(t, c, 0); names != "fb,ref" {
		t.Fatalf("unpinned: %s", names)
	}
	if _, err := agentMemPin(agentMemPinReq{Scope: "project", Project: c.Project.ID, Name: "nope", Pinned: true}, now); agentMemCode(err) != errCodeMemoryNotFound {
		t.Fatalf("pin of a missing memory: %v", err)
	}
	if _, err := agentMemPin(agentMemPinReq{Scope: "project", Project: "../x", Name: "ref", Pinned: true}, now); agentMemCode(err) != errCodeMemoryBadRequest {
		t.Fatalf("pin with a path as project: %v", err)
	}
}

func TestAgentMemoryPinRevertRestoresPreviousPin(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	agentMemSaveN(t, c, "a", "project", now)
	res, err := agentMemPin(agentMemPinReq{Scope: "project", Project: c.Project.ID, Name: "a", Pinned: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: res.Commit}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	e, err := agentMemRead(c, "", "a")
	if err != nil || e.Pinned {
		t.Fatalf("after reverting the pin: %+v, %v", e, err)
	}
}

// Pins that alone exceed the budget do not break it: the ones that fit are described, the rest
// fall to the names-only tail and are counted.
func TestAgentMemoryPinsOverBudgetStayWithinIt(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	long := strings.Repeat("x", 70)
	const n = 60
	for i := 0; i < n; i++ {
		name := "pin-" + strings.Repeat("0", 3-len(itoa(i))) + itoa(i)
		if _, err := agentMemSave(c, agentMemSaveReq{Name: name, Description: long, Type: "project", Body: "b"}, now); err != nil {
			t.Fatal(err)
		}
		if _, err := agentMemPin(agentMemPinReq{Scope: "project", Project: c.Project.ID, Name: name, Pinned: true}, now); err != nil {
			t.Fatal(err)
		}
	}
	agentMemSaveN(t, c, "free", "feedback", now)
	idx, _ := agentMemIndexNames(t, c, agentMemIndexBudgetMin)
	size := 0
	for _, e := range idx.Entries {
		size += len(agentMemIndexLine(e))
		if e.Name == "free" {
			t.Fatal("an unpinned memory was described ahead of a pin")
		}
	}
	if size > agentMemIndexBudgetMin || len(idx.Entries) == 0 || len(idx.Entries) == n {
		t.Fatalf("described %d entries, %d bytes (budget %d)", len(idx.Entries), size, agentMemIndexBudgetMin)
	}
	if idx.PinnedOmitted != n-len(idx.Entries) {
		t.Fatalf("pinnedOmitted = %d, want %d", idx.PinnedOmitted, n-len(idx.Entries))
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// The Console's list carries every scope with its pin and count, and the pin route works on it.
func TestAgentMemoryListAndPinRoutes(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	agentMemSaveN(t, c, "proj-one", "project", now)
	if _, err := agentMemSave(c, agentMemSaveReq{Scope: "user", Name: "user-one", Description: "d", Type: "user", Body: "b"}, now); err != nil {
		t.Fatal(err)
	}
	mux := buildMux()
	body, _ := json.Marshal(agentMemPinReq{Scope: "user", Name: "user-one", Pinned: true})
	if w := smokeDo(t, mux, http.MethodPost, "/agents/memory/entries/pin", "", string(body)); w.Code != http.StatusOK {
		t.Fatalf("pin: %d %s", w.Code, w.Body)
	}
	smokeDo(t, mux, http.MethodGet, "/agents/memory/entries/read?session=claude-main&name=proj-one", "", "")
	w := smokeDo(t, mux, http.MethodGet, "/agents/memory/entries/list", "", "")
	var out agentMemListAllOut
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Entries) != 2 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if e := out.Entries[0]; e.Name != "user-one" || !e.Pinned || e.Scope != "user" {
		t.Errorf("first = %+v", e)
	}
	if e := out.Entries[1]; e.Name != "proj-one" || e.Uses != 1 || e.Project == nil || e.Project.ID != c.Project.ID {
		t.Errorf("second = %+v", e)
	}
}
