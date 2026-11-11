package memoryx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// museNote writes a muse project-store note into the seeded working copy.
func (e *autoEnv) museNote(name, desc, body string) {
	e.t.Helper()
	memoryWrite(e.t, filepath.Join(e.clone, ".agents", "memory", name+".md"),
		fmt.Sprintf("---\nname: %s\ndescription: %s\ntype: project\n---\n\n%s\n", name, desc, body))
}

func (e *autoEnv) museStore(parts ...string) string {
	return filepath.Join(append([]string{e.home, ".local", "share", "muse", "memory"}, parts...)...)
}

func museRun(t *testing.T) *AutoSyncRun {
	t.Helper()
	st := autoStatus()
	if st.Run == nil {
		t.Fatal("no run recorded")
	}
	return st.Run
}

func museProject(run *AutoSyncRun, pid string) *AutoSyncProject {
	for i := range run.Projects {
		if run.Projects[i].Agent == autoAgentMuse && run.Projects[i].ID == pid {
			return &run.Projects[i]
		}
	}
	return nil
}

// Switching on carries muse's project and personal-project notes across with no manual step. The
// personal store is not written: it is listed as waiting for the member's choice.
func TestAutoSyncOnImportsMuseAndLeavesThePersonalStoreForConfirmation(t *testing.T) {
	e := newAutoEnv(t)
	e.museNote("tabs", "use tabs", "tabs please")
	memoryWrite(t, e.museStore("projects", pdir(e.clone), "pp.md"), "personal project note")
	memoryWrite(t, e.museStore("personal", "likes-go.md"), "I like Go")

	pv, err := autoPreview("on")
	if err != nil {
		t.Fatal(err)
	}
	if pv.Apply != 2 || pv.MusePersonal != 1 {
		t.Fatalf("preview = %+v, want 2 to apply and 1 personal note waiting", pv)
	}

	e.flip(true)
	if !eq(afNames(t, e.pid), []string{"pp", "tabs"}) {
		t.Fatalf("AF after import: %v", afNames(t, e.pid))
	}
	run := museRun(t)
	if !run.Complete || run.Error != "" || run.MusePersonal != 1 {
		t.Fatalf("run = %+v", run)
	}
	if p := museProject(run, e.pid); p == nil || p.Counts["imported"] != 2 {
		t.Fatalf("muse project counts = %+v", p)
	}
	var row *AutoSyncRow
	for i := range run.Rows {
		if run.Rows[i].Status == autoStatusNeedsConfirmation {
			row = &run.Rows[i]
		}
	}
	if row == nil || row.Agent != autoAgentMuse || row.Reason != museImportChooseUser {
		t.Fatalf("rows = %+v", run.Rows)
	}
	// Nothing reached the user scope.
	agentMemMu.RLock()
	es, _, err := agentMemLoadDir(agentMemScopeUser, "user")
	agentMemMu.RUnlock()
	if err == nil && len(es) != 0 {
		t.Fatalf("the personal store was imported silently: %v", es)
	}
}

// A second switch-on changes nothing, and a memory the member made agents forget stays gone.
func TestAutoSyncMuseIsIdempotentAndNeverResurrects(t *testing.T) {
	e := newAutoEnv(t)
	e.museNote("keep", "keep me", "kept")
	e.museNote("doomed", "forget me", "doomed")
	e.flip(true)
	if _, err := agentMemForget(e.c, agentMemForgetReq{Name: "doomed", Revision: 1}, e.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	e.flip(false)
	e.flip(true)
	if !eq(afNames(t, e.pid), []string{"keep"}) {
		t.Fatalf("AF after the second switch-on: %v", afNames(t, e.pid))
	}
	p := museProject(museRun(t), e.pid)
	if p == nil || p.Counts["imported"] != 0 || p.Counts["unchanged"] != 1 || p.Counts["forgotten"] != 1 {
		t.Fatalf("second run counts = %+v", p)
	}
}

// A memory AF holds its own version of is reported per item and left alone; a secret is skipped
// and its text never reaches the record.
func TestAutoSyncMuseReportsDivergedAndSecretPerItem(t *testing.T) {
	e := newAutoEnv(t)
	e.save("mine", "written in AF", "project", "af body")
	e.museNote("mine", "from muse", "muse body")
	memoryWrite(t, filepath.Join(e.clone, ".agents", "memory", "leak.md"), "token ghp_"+strings.Repeat("a1B2c3", 6)+"\n")
	e.flip(true)

	run := museRun(t)
	if got, _ := agentMemRead(e.c, "", "mine"); got.Body != "af body" {
		t.Fatalf("the AF copy was replaced: %+v", got)
	}
	if afNames(t, e.pid)[0] != "mine" || len(afNames(t, e.pid)) != 1 {
		t.Fatalf("AF = %v", afNames(t, e.pid))
	}
	var div, sec *AutoSyncRow
	for i := range run.Rows {
		switch run.Rows[i].Status {
		case museImportDiverged:
			div = &run.Rows[i]
		case claudeImportSecret:
			sec = &run.Rows[i]
		}
	}
	if div == nil || div.Agent != autoAgentMuse || div.Reason != "other_source" || div.Name != "mine" {
		t.Fatalf("diverged row = %+v in %+v", div, run.Rows)
	}
	if sec == nil || len(sec.Rules) == 0 || sec.Name != "leak" {
		t.Fatalf("secret row = %+v", sec)
	}
	raw, _ := os.ReadFile(autoStatePath())
	if strings.Contains(string(raw), "ghp_") || strings.Contains(string(raw), "muse body") {
		t.Error("the state file carries muse text")
	}
}

// muse trouble never fails the switch: a folder that maps to no project is counted, an oversized
// one is skipped with a row, and the run still completes and reaches `on`.
func TestAutoSyncMuseTroubleDoesNotFailTheSwitch(t *testing.T) {
	e := newAutoEnv(t)
	e.file("claude-one", "from claude", "project", "claude body")
	memoryWrite(t, e.museStore("projects", "nowhere-0123456789abcdef", "x.md"), "orphan")
	big := filepath.Join(e.clone, ".agents", "memory")
	for i := 0; i <= agentMemImportMaxFiles; i++ {
		memoryWrite(t, filepath.Join(big, fmt.Sprintf("n%04d.md", i)), "note")
	}
	e.flip(true)
	st := autoStatus()
	if st.Phase != autoPhaseOn || !st.Run.Complete || st.Run.Error != "" || st.Run.MuseUnmapped != 1 {
		t.Fatalf("status = %+v run = %+v", st, st.Run)
	}
	if !eq(afNames(t, e.pid), []string{"claude-one"}) {
		t.Fatalf("AF = %v", afNames(t, e.pid))
	}
	found := false
	for _, r := range st.Run.Rows {
		found = found || r.Agent == autoAgentMuse && r.Reason == "too_many_files"
	}
	if !found {
		t.Errorf("no too_many_files row: %+v", st.Run.Rows)
	}
}
