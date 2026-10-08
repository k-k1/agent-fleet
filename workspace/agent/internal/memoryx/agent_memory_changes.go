package memoryx

// AF-owned agent memory (ADR 0108 decision 8) — the member's after-the-fact view: the list of
// published changes (who, when, what), the diff of one, and the way back from one. Writes are
// published without approval, so this is where a bad memory is found and undone.
//
// A revert never rewrites history. It writes the memory's state from before the change as a
// new change, with a new revision, the member as author and an AF-Revert-Of trailer, through
// the same lock, scan and one-commit step as an agent's write.
//
// The history is not trusted to have been scanned: an import can adopt another environment's
// lineage, and the rules grow. Every value this file returns — trailers, names, project
// info, diffs — is scanned on the way out, and a row or diff that fails is withheld.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
	agentMemOps        = map[string]bool{"create": true, "update": true, "forget": true, "revert": true, "import": true, "pin": true}
)

// agentMemChangeView is one published change as the Console lists it.
type agentMemChangeView struct {
	Commit        string           `json:"commit"`
	At            string           `json:"at"`
	Op            string           `json:"op"` // create | update | forget | revert | import | pin
	Scope         string           `json:"scope"`
	Project       *agentMemProject `json:"project,omitempty"`
	Name          string           `json:"name"`
	AuthorKind    string           `json:"authorKind"`
	AuthorSession string           `json:"authorSession"`
	RevertOf      string           `json:"revertOf,omitempty"`
	// Latest is the newest change of this memory in the list, the only one a revert or forget
	// may start from. Live says the memory exists now. Revertible says the history holds a
	// text on at least one side of the change (a forget of a file never committed has none).
	Latest     bool `json:"latest"`
	Live       bool `json:"live"`
	Revertible bool `json:"revertible"`
}

// agentMemChangesWire is the change list. Withheld counts rows left out because a value in
// them failed the scan or the expected form.
type agentMemChangesWire struct {
	Changes  []agentMemChangeView `json:"changes"`
	Withheld int                  `json:"withheld,omitempty"`
}

func agentMemCleanText(s string) bool { return len(agentMemScanText("", s)) == 0 }

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
// project.json is missing, unreadable, behind a symlink or fails the scan is shown by its id.
func agentMemProjectInfo(id string) *agentMemProject {
	p := &agentMemProject{ID: id, Display: id}
	dir, err := agentMemCheckDir("projects/"+id, false)
	if err != nil {
		return p
	}
	b, ok, err := agentMemReadFile(filepath.Join(dir, "project.json"))
	if err != nil || !ok {
		return p
	}
	var v agentMemProject
	if json.Unmarshal(b, &v) != nil || v.Display == "" || !agentMemCleanText(v.Root+"\n"+v.VCS+"\n"+v.Display) {
		return p
	}
	p.Display, p.Root, p.VCS = v.Display, v.Root, v.VCS
	return p
}

// agentMemAuthorOK holds an author to the forms AF writes, so a trailer from an adopted
// lineage cannot smuggle free text into the list.
func agentMemAuthorOK(kind, sess string) bool {
	kindOK := kind == agentMemUnknown || kind == agentMemMember.Kind || agentMemKindRe.MatchString(kind)
	sessOK := sess == agentMemUnknown || sess == agentMemMember.Session || session.ValidName(sess)
	return kindOK && sessOK
}

// agentMemBlob is one file at a revision. exists=false means the tree does not have it; an
// error means git could not say, which is never read as "absent".
func agentMemBlob(rev, rel string) (data []byte, exists bool, err error) {
	path := agentMemRepoPrefix + "/" + rel
	out, err := memoryGitRun("ls-tree", rev, "--", path)
	if err != nil {
		return nil, false, fmt.Errorf("read agent memory history: %w", err)
	}
	if out == "" {
		return nil, false, nil
	}
	b, err := memoryGit("show", rev+":"+path).Output()
	if err != nil {
		return nil, false, fmt.Errorf("read agent memory history: %w", err)
	}
	return b, true, nil
}

// agentMemParent is a commit's first parent, or "" for a root commit. A git failure is an
// error: read as "no parent", it would turn an update into a create and its revert into a delete.
func agentMemParent(commit string) (string, error) {
	out, err := memoryGitRun("rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return "", fmt.Errorf("read agent memory history: %w", err)
	}
	f := strings.Fields(out)
	if len(f) == 0 || f[0] != commit {
		return "", fmt.Errorf("read agent memory history: unexpected rev-list output")
	}
	if len(f) == 1 {
		return "", nil
	}
	return f[1], nil
}

// agentMemSides returns the memory's text before and after a commit.
func agentMemSides(commit, rel string) (before []byte, beforeOK bool, after []byte, afterOK bool, err error) {
	if after, afterOK, err = agentMemBlob(commit, rel); err != nil {
		return
	}
	parent, err := agentMemParent(commit)
	if err != nil || parent == "" {
		return
	}
	before, beforeOK, err = agentMemBlob(parent, rel)
	return
}

// agentMemListChanges returns the newest published changes first.
func agentMemListChanges(limit int) (agentMemChangesWire, error) {
	if limit <= 0 {
		limit = agentMemChangesDefault
	}
	limit = min(limit, agentMemChangesMax)
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	out := agentMemChangesWire{Changes: []agentMemChangeView{}}
	if !memoryHasCommits() {
		return out, nil
	}
	log, err := memoryGitRun("log", memoryBranch, "-n", strconv.Itoa(limit),
		"-F", "--grep=AF-Trigger: "+memoryTriggerAgentMemory,
		"--format=%H"+memoryFldSep+"%aI"+memoryFldSep+"%B"+memoryRecSep)
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	projects := map[string]*agentMemProject{}
	for _, rec := range strings.Split(log, memoryRecSep) {
		f := strings.SplitN(strings.TrimSpace(rec), memoryFldSep, 3)
		if len(f) < 3 {
			continue
		}
		v, rel, pid, ok := agentMemChangeFrom(f[0], f[1], f[2])
		// The newest change of a path is decided before any row is withheld, the same way
		// agentMemLatestChange decides it for a revert: otherwise the row before a withheld
		// one would offer a revert the Agent always refuses.
		first := rel != "" && !seen[rel]
		if rel != "" {
			seen[rel] = true
		}
		if !ok {
			out.Withheld++
			continue
		}
		if pid != "" {
			if projects[pid] == nil {
				projects[pid] = agentMemProjectInfo(pid)
			}
			v.Project = projects[pid]
		}
		if first {
			v.Latest = true
			_, live, _ := agentMemReadFile(filepath.Join(agentMemDir(), filepath.FromSlash(rel)))
			v.Live = live
			_, beforeOK, _, afterOK, err := agentMemSides(v.Commit, rel)
			v.Revertible = err == nil && (beforeOK || afterOK)
		}
		out.Changes = append(out.Changes, v)
	}
	return out, nil
}

// agentMemChangeFrom builds a row from one commit, or ok=false when anything in it is not in the
// form AF writes or fails the scan. The trigger is checked again here: --grep matches a line
// anywhere in the message.
func agentMemChangeFrom(commit, at, body string) (v agentMemChangeView, rel, pid string, ok bool) {
	tr := agentMemTrailers(body)
	if tr["AF-Trigger"] != memoryTriggerAgentMemory || !agentMemCommitRe.MatchString(commit) {
		return v, "", "", false
	}
	rel, scope, pid, name, ok := agentMemParseRepoPath(tr["AF-Memory"])
	if !ok {
		return v, "", "", false
	}
	// From here on the path counts for "newest change", whether or not the row is shown.
	if !agentMemOps[tr["AF-Op"]] || !agentMemAuthorOK(tr["AF-Author-Kind"], tr["AF-Author-Session"]) {
		return v, rel, "", false
	}
	if r := tr["AF-Revert-Of"]; r != "" && !agentMemCommitRe.MatchString(r) {
		return v, rel, "", false
	}
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		return v, rel, "", false
	}
	if !agentMemCleanText(name + "\n" + pid + "\n" + tr["AF-Author-Kind"] + "\n" + tr["AF-Author-Session"]) {
		return v, rel, "", false
	}
	v = agentMemChangeView{
		Commit: commit, At: at, Op: tr["AF-Op"], Scope: scope, Name: name,
		AuthorKind: tr["AF-Author-Kind"], AuthorSession: tr["AF-Author-Session"], RevertOf: tr["AF-Revert-Of"],
	}
	return v, rel, pid, true
}

// agentMemDiffWire is one change's diff, or the masked findings that withheld it.
type agentMemDiffWire struct {
	Diff     string                `json:"diff"`
	Withheld bool                  `json:"withheld,omitempty"`
	Findings []memorySecretFinding `json:"findings,omitempty"`
}

// agentMemChangeDiff is the diff of one change, limited to the memory it changed and scanned
// before it is returned (the generic memory diff returns history as it is).
func agentMemChangeDiff(commitArg string) (agentMemDiffWire, error) {
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	commit, rel, err := agentMemResolveChange(commitArg)
	if err != nil {
		return agentMemDiffWire{}, err
	}
	base, err := agentMemParent(commit)
	if err != nil {
		return agentMemDiffWire{}, err
	}
	if base == "" {
		if base, err = memoryGitRun("hash-object", "-t", "tree", "/dev/null"); err != nil {
			return agentMemDiffWire{}, err
		}
	}
	diff, err := memoryGitRun("diff", "--no-color", base, commit, "--", agentMemRepoPrefix+"/"+rel)
	if err != nil {
		return agentMemDiffWire{}, err
	}
	// The raw diff, and both sides decoded the way a reader would see them: a JSON escape in
	// the file hides a value from a scan of the raw text alone.
	findings := agentMemScanText("diff", diff)
	before, beforeOK, after, afterOK, err := agentMemSides(commit, rel)
	if err != nil {
		return agentMemDiffWire{}, err
	}
	for _, side := range []struct {
		b  []byte
		ok bool
	}{{before, beforeOK}, {after, afterOK}} {
		if !side.ok {
			continue
		}
		if e, ok := agentMemParse(side.b); ok {
			findings = append(findings, agentMemScanText("diff", agentMemPublished(e))...)
		}
	}
	if len(findings) > 0 {
		return agentMemDiffWire{Withheld: true, Findings: findings}, nil
	}
	return agentMemDiffWire{Diff: diff}, nil
}

// agentMemResolveChange checks that commitArg names an agent-memory change on main and returns
// it with the memory path it changed.
func agentMemResolveChange(commitArg string) (commit, rel string, err error) {
	if !agentMemCommitRe.MatchString(commitArg) {
		return "", "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadRev, "commit must be a commit id")
	}
	if !memoryHasCommits() {
		return "", "", memoryErrf(http.StatusNotFound, errCodeMemoryBadRev, "no such change")
	}
	commit, err = memoryGitRun("rev-parse", "--verify", "--quiet", commitArg+"^{commit}")
	if err != nil || commit == "" {
		return "", "", memoryErrf(http.StatusNotFound, errCodeMemoryBadRev, "no such change")
	}
	if _, err := memoryGitRun("merge-base", "--is-ancestor", commit, memoryBranch); err != nil {
		return "", "", memoryErrf(http.StatusNotFound, errCodeMemoryBadRev, "no such change in the memory history")
	}
	body, err := memoryGitRun("log", "-1", "--format=%B", commit)
	if err != nil {
		return "", "", err
	}
	tr := agentMemTrailers(body)
	rel, _, pid, name, ok := agentMemParseRepoPath(tr["AF-Memory"])
	if tr["AF-Trigger"] != memoryTriggerAgentMemory || !ok {
		return "", "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadRev, "that commit is not an agent memory change")
	}
	// The path is about to reach a response and a new commit message: refuse, without the
	// value, a change whose name or project id the list would have withheld.
	if !agentMemCleanText(name + "\n" + pid) {
		return "", "", memoryErrf(http.StatusUnprocessableEntity, errCodeMemorySecretDetected,
			"this change is withheld: its memory name or project id looks like a secret")
	}
	return commit, rel, nil
}

// agentMemLatestChange is the newest agent-memory commit on main for one memory path, decided
// by the same trailer parsing the list uses: git's --grep only narrows the candidates, since it
// also matches a quoted line in a message body and misses a line with trailing blanks.
func agentMemLatestChange(rel string) (string, error) {
	path := agentMemRepoPrefix + "/" + rel
	log, err := memoryGitRun("log", memoryBranch, "-F", "--grep=AF-Memory: "+path,
		"--format=%H"+memoryFldSep+"%B"+memoryRecSep)
	if err != nil {
		return "", err
	}
	for _, rec := range strings.Split(log, memoryRecSep) {
		f := strings.SplitN(strings.TrimSpace(rec), memoryFldSep, 2)
		if len(f) < 2 {
			continue
		}
		tr := agentMemTrailers(f[1])
		if tr["AF-Trigger"] == memoryTriggerAgentMemory && tr["AF-Memory"] == path {
			return f[0], nil
		}
	}
	return "", nil
}

// agentMemRevertReq undoes one published change, or (Forget) removes the memory as that change
// left it. Ack lets a body that fails the secret scan through; it covers this request only.
type agentMemRevertReq struct {
	Commit string `json:"commit"`
	Forget bool   `json:"forget"`
	Ack    bool   `json:"ack"`
}

func agentMemRevOf(b []byte, ok bool) int {
	if !ok {
		return 0
	}
	if e, ok := agentMemParse(b); ok {
		return e.Revision
	}
	return 0
}

// agentMemRevert applies a revert or a forget from the Console. It starts only from the newest
// change of the memory, and only while the memory is still exactly as that change left it:
// reverting anything older would silently drop what came after.
func agentMemRevert(req agentMemRevertReq, now time.Time) (agentMemWriteResult, error) {
	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	commit, rel, err := agentMemResolveChange(req.Commit)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	_, scope, _, name, _ := agentMemParseRepoPath(agentMemRepoPrefix + "/" + rel)
	// Compared by commit, not only by content: after create → forget → create → forget, the
	// first forget and the live store agree that the memory is absent, yet reverting it would
	// bring back the older text.
	latest, err := agentMemLatestChange(rel)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	if latest != commit {
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"the memory has changed since this change; only its newest change can be reverted")
	}
	scopeDir := filepath.ToSlash(filepath.Dir(rel))
	before, beforeOK, after, afterOK, err := agentMemSides(commit, rel)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	dir, err := agentMemCheckDir(scopeDir, true)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	live, liveOK, err := agentMemReadFile(filepath.Join(dir, name+".md"))
	if err != nil {
		return agentMemWriteResult{}, err
	}
	if liveOK != afterOK || !bytes.Equal(live, after) {
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"the memory has changed since this change; only its newest change can be reverted")
	}
	tomb, err := agentMemTombRevision(scopeDir, name)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	// Every revision this memory is known to have had is a floor: the store's own record (the
	// live file, the tombstone) can be lost with its directory, the history cannot.
	floor := max(agentMemRevOf(live, liveOK), tomb, agentMemRevOf(after, afterOK), agentMemRevOf(before, beforeOK))
	trailer := "AF-Revert-Of: " + commit

	if req.Forget || !beforeOK {
		// Forgetting the memory, or undoing its creation: either way it goes.
		if !liveOK {
			if !beforeOK {
				return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
					"there is no earlier text to bring back: the history never held this memory's text")
			}
			return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "the memory is already gone")
		}
		if err := agentMemWriteTomb(scopeDir, name, floor); err != nil {
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
		return agentMemWriteResult{Name: name, Scope: scope, Revision: agentMemRevOf(live, liveOK), Commit: rev, Deleted: true}, nil
	}

	e, ok := agentMemParse(before)
	if !ok {
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "the earlier version cannot be read as a memory")
	}
	e.Name, e.Scope = name, scope
	// The pin comes back as `before` had it. Only the newest change can be reverted, so live is
	// that change's result, and a change that moved the pin is a pin change alone.
	e.Revision = floor + 1
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
