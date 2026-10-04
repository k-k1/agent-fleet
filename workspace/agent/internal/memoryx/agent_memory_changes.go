package memoryx

// AF-owned agent memory (ADR 0108 decision 8) — the member's after-the-fact view: the list of
// published changes (who, when, what) and the way back from one of them. Writes are published
// without approval, so this is where a bad memory is found and undone.
//
// A revert never rewrites history. It writes the memory's state from before the change as a
// new change, with a new revision, the member as author and an AF-Revert-Of trailer, through
// the same lock, scan and one-commit step as an agent's write.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	agentMemChangesDefault = 100
	agentMemChangesMax     = 300
)

// agentMemMember is the author of a change made from the Console. The CP's relay header is
// only a hint an agent could also send, so this records the route the change came by, not a
// verified person.
var agentMemMember = agentMemCaller{Session: "console", Kind: "member"}

var (
	agentMemRepoPathRe = regexp.MustCompile(`^af/(user|projects/[a-z0-9._-]{1,80})/([a-z0-9][a-z0-9-]{0,63})\.md$`)
	agentMemCommitRe   = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

// agentMemChangeView is one published change as the Console lists it.
type agentMemChangeView struct {
	Commit        string           `json:"commit"`
	At            string           `json:"at"`
	Op            string           `json:"op"` // create | update | forget | revert
	Scope         string           `json:"scope"`
	Project       *agentMemProject `json:"project,omitempty"`
	Name          string           `json:"name"`
	AuthorKind    string           `json:"authorKind"`
	AuthorSession string           `json:"authorSession"`
	RevertOf      string           `json:"revertOf,omitempty"`
	// Latest is the newest change of this memory in the list; only that one can be reverted
	// or used to forget the memory. Live says the memory exists now.
	Latest bool `json:"latest"`
	Live   bool `json:"live"`
}

// agentMemChangesWire is the change list.
type agentMemChangesWire struct {
	Changes []agentMemChangeView `json:"changes"`
}

// agentMemParseRepoPath splits af/<scope dir>/<name>.md.
func agentMemParseRepoPath(p string) (rel, scope, projectID, name string, ok bool) {
	m := agentMemRepoPathRe.FindStringSubmatch(p)
	if m == nil {
		return "", "", "", "", false
	}
	rel = strings.TrimPrefix(p, agentMemRepoPrefix+"/")
	if m[1] == "user" {
		return rel, agentMemScopeUser, "", m[2], true
	}
	return rel, agentMemScopeProject, strings.TrimPrefix(m[1], "projects/"), m[2], true
}

// agentMemTrailers reads the trailer lines of a commit message.
func agentMemTrailers(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		if k, v, ok := strings.Cut(line, ": "); ok && strings.HasPrefix(k, "AF-") {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// agentMemProjectInfo reads what a project id stands for, for display. A project whose
// project.json is missing or unreadable is shown by its id.
func agentMemProjectInfo(id string) *agentMemProject {
	p := &agentMemProject{ID: id, Display: id}
	b, ok, err := agentMemReadFile(filepath.Join(agentMemDir(), "projects", id, "project.json"))
	if err != nil || !ok {
		return p
	}
	var v agentMemProject
	if json.Unmarshal(b, &v) == nil && v.Display != "" && len(agentMemScanText("", v.Display)) == 0 {
		p.Display, p.Root, p.VCS = v.Display, v.Root, v.VCS
	}
	return p
}

// agentMemListChanges returns the newest published changes first.
func agentMemListChanges(limit int) ([]agentMemChangeView, error) {
	if limit <= 0 {
		limit = agentMemChangesDefault
	}
	limit = min(limit, agentMemChangesMax)
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	out := []agentMemChangeView{}
	if !memoryHasCommits() {
		return out, nil
	}
	log, err := memoryGitRun("log", memoryBranch, "-n", strconv.Itoa(limit),
		"--grep=^AF-Trigger: "+memoryTriggerAgentMemory+"$",
		"--format=%H"+memoryFldSep+"%aI"+memoryFldSep+"%B"+memoryRecSep)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	projects := map[string]*agentMemProject{}
	for _, rec := range strings.Split(log, memoryRecSep) {
		f := strings.SplitN(strings.TrimSpace(rec), memoryFldSep, 3)
		if len(f) < 3 {
			continue
		}
		tr := agentMemTrailers(f[2])
		rel, scope, pid, name, ok := agentMemParseRepoPath(tr["AF-Memory"])
		if !ok {
			continue
		}
		v := agentMemChangeView{
			Commit: f[0], At: f[1], Op: tr["AF-Op"], Scope: scope, Name: name,
			AuthorKind: tr["AF-Author-Kind"], AuthorSession: tr["AF-Author-Session"], RevertOf: tr["AF-Revert-Of"],
		}
		if pid != "" {
			if projects[pid] == nil {
				projects[pid] = agentMemProjectInfo(pid)
			}
			v.Project = projects[pid]
		}
		if !seen[rel] {
			seen[rel] = true
			v.Latest = true
			_, live, _ := agentMemReadFile(filepath.Join(agentMemDir(), filepath.FromSlash(rel)))
			v.Live = live
		}
		out = append(out, v)
	}
	return out, nil
}

// agentMemRevertReq undoes one published change, or (Forget) removes the memory as that change
// left it. Ack lets a body that fails the secret scan through; it covers this request only.
type agentMemRevertReq struct {
	Commit string `json:"commit"`
	Forget bool   `json:"forget"`
	Ack    bool   `json:"ack"`
}

// agentMemRevert applies a revert or a forget from the Console. It refuses unless the memory
// is still exactly as the change left it: reverting anything older would silently drop the
// changes made since.
func agentMemRevert(req agentMemRevertReq, now time.Time) (agentMemWriteResult, error) {
	if !agentMemCommitRe.MatchString(req.Commit) {
		return agentMemWriteResult{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRev, "commit must be a commit id")
	}
	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	commit, err := memoryGitRun("rev-parse", "--verify", "--quiet", req.Commit+"^{commit}")
	if err != nil || commit == "" {
		return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryBadRev, "no such change")
	}
	if _, err := memoryGitRun("merge-base", "--is-ancestor", commit, memoryBranch); err != nil {
		return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryBadRev, "no such change in the memory history")
	}
	body, err := memoryGitRun("log", "-1", "--format=%B", commit)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	tr := agentMemTrailers(body)
	rel, scope, _, name, ok := agentMemParseRepoPath(tr["AF-Memory"])
	if tr["AF-Trigger"] != memoryTriggerAgentMemory || !ok {
		return agentMemWriteResult{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRev, "that commit is not an agent memory change")
	}
	scopeDir := filepath.ToSlash(filepath.Dir(rel))

	after, afterOK := agentMemShow(commit, rel)
	before, beforeOK := agentMemShow(commit+"^", rel)
	dir, err := agentMemCheckDir(scopeDir, true)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	abs := filepath.Join(dir, name+".md")
	live, liveOK, err := agentMemReadFile(abs)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	if liveOK != afterOK || !bytes.Equal(live, after) {
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"the memory has changed since this change; only its newest change can be reverted")
	}
	liveRev := 0
	if liveOK {
		if e, ok := agentMemParse(live); ok {
			liveRev = e.Revision
		}
	}
	tomb, err := agentMemTombRevision(scopeDir, name)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	trailer := "AF-Revert-Of: " + commit

	if req.Forget || !beforeOK {
		// Forgetting the memory, or undoing its creation: either way it goes.
		if !liveOK {
			return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "the memory is already gone")
		}
		if err := agentMemWriteTomb(scopeDir, name, max(liveRev, tomb)); err != nil {
			return agentMemWriteResult{}, err
		}
		op, extra := "forget", []string{}
		if !req.Forget {
			op, extra = "revert", []string{trailer}
		}
		rev, err := agentMemApplyLocked([]agentMemChange{{Rel: rel, Delete: true}}, op, rel, agentMemMember, now, extra...)
		if err != nil {
			return agentMemWriteResult{}, err
		}
		return agentMemWriteResult{Name: name, Scope: scope, Revision: liveRev, Commit: rev, Deleted: true}, nil
	}

	e, ok := agentMemParse(before)
	if !ok {
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "the earlier version cannot be read as a memory")
	}
	e.Name, e.Scope = name, scope
	e.Revision = max(liveRev, tomb) + 1
	e.AuthorKind, e.AuthorSession = agentMemMember.Kind, agentMemMember.Session
	e.Updated = now.UTC().Format(time.RFC3339)
	data := agentMemRender(e)
	if !req.Ack {
		findings := append(agentMemScanText("memory", agentMemPublished(e)), agentMemScanText("memory", string(data))...)
		if len(findings) > 0 {
			return agentMemWriteResult{}, &agentMemSecretErr{Findings: findings}
		}
	}
	rev, err := agentMemApplyLocked([]agentMemChange{{Rel: rel, Data: data}}, "revert", rel, agentMemMember, now, trailer)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	if !liveOK {
		agentMemRemoveTomb(scopeDir, name)
	}
	return agentMemWriteResult{Name: name, Scope: scope, Revision: e.Revision, Commit: rev, Created: !liveOK}, nil
}

// agentMemShow is one file at a commit; ok=false when the commit does not have it.
func agentMemShow(rev, rel string) ([]byte, bool) {
	b, err := memoryGit("show", rev+":"+agentMemRepoPrefix+"/"+rel).Output()
	if err != nil {
		return nil, false
	}
	return b, true
}
