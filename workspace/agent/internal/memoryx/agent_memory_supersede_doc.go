package memoryx

// The member's supersede of a project memory by a repository document (ADR 0130 decision 11):
// once the pull request carrying a promoted text is merged, the memory points at the document
// instead of repeating it. The target is the repository's identity, a normalised path, the
// heading and the merge commit, and the commit is checked to be on the default branch with the
// file and heading in it. Like decision 6's supersede it is a change to one memory, so the change
// list, its diff and revert work as for any other change; the memory leaves agents' view and its
// history keeps it.
//
// The memory must still be at the revision the proposal quoted: if it changed while the pull
// request waited, the supersede is refused, so merging an old text never hides a newer one.
// Nothing is fetched; a merge the working copy has not fetched yet is refused with that reason.

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorypromote"
)

const agentMemOpSupersede = "supersede"

// agentMemSupersedeDocReq names the memory, the revision the proposal quoted, and the document.
type agentMemSupersedeDocReq struct {
	Project  string `json:"project"`
	Name     string `json:"name"`
	Revision int    `json:"revision"`
	Path     string `json:"path"`
	Heading  string `json:"heading"`
	Commit   string `json:"commit"`
}

// agentMemSupersedeDocCheck verifies the document target against the repository of the project
// and answers it with the merge commit's full id. Read-only, outside the store lock.
func agentMemSupersedeDocCheck(p *agentMemProject, path, heading, commit string) (agentMemDocTarget, error) {
	conflict := func(format string, a ...any) error {
		return memoryErrf(http.StatusConflict, errCodeMemoryConflict, format, a...)
	}
	if p == nil || p.Remote == "" {
		return agentMemDocTarget{}, conflict("the project has no origin URL to name its repository by")
	}
	repo, ref, why := agentMemLintRepoFor(p)
	if repo == nil {
		return agentMemDocTarget{}, conflict("the repository could not be read: %s", why)
	}
	lr := repo.(agentMemLintRepo)
	if !agentMemPromoteDefaultKnown(repo, ref) {
		return agentMemDocTarget{}, conflict("origin/HEAD does not name the default branch, so %s is only a guess (`git remote set-head origin --auto` sets it)", ref)
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentMemLintGitTimeout)
	defer cancel()
	out, err := agentMemLintGit(ctx, lr.gitDir, nil, "rev-parse", "--verify", "-q", "--end-of-options", commit+"^{commit}")
	full := strings.TrimSpace(string(out))
	if err != nil || !agentMemCommitIDRe.MatchString(full) {
		return agentMemDocTarget{}, conflict("commit %s is not in the repository; fetch the default branch first", commit)
	}
	if _, err := agentMemLintGit(ctx, lr.gitDir, nil, "merge-base", "--is-ancestor", full, lr.ref); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return agentMemDocTarget{}, conflict("commit %s is not on %s", commit, ref)
		}
		return agentMemDocTarget{}, err
	}
	// The heading must be in the file both at the merge and now: a target already renamed or
	// deleted on the default branch is a broken link from the start.
	for _, at := range []struct{ commit, name string }{{full, "commit " + commit}, {lr.ref, ref}} {
		blob, _, err := agentMemTreeBlob(ctx, lr.gitDir, at.commit, path)
		if err != nil {
			return agentMemDocTarget{}, err
		}
		if !blob {
			return agentMemDocTarget{}, conflict("%s is not a file at %s", path, at.name)
		}
		doc, err := agentMemLintGit(ctx, lr.gitDir, nil, "cat-file", "blob", at.commit+":"+path)
		if err != nil {
			return agentMemDocTarget{}, err
		}
		if _, ok := memorypromote.FindHeading(doc, heading); !ok {
			return agentMemDocTarget{}, conflict("%s has no heading %q at %s", path, heading, at.name)
		}
	}
	return agentMemDocTarget{Repo: p.Remote, Path: path, Heading: heading, Commit: full}, nil
}

// agentMemSupersedeDocument sets superseded_by to a document on one project memory.
func agentMemSupersedeDocument(req agentMemSupersedeDocReq, now time.Time) (agentMemWriteResult, error) {
	bad := func(format string, a ...any) error {
		return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, format, a...)
	}
	conflict := func(format string, a ...any) error {
		return memoryErrf(http.StatusConflict, errCodeMemoryConflict, format, a...)
	}
	req.Name, req.Path, req.Heading, req.Commit = strings.TrimSpace(req.Name), strings.TrimSpace(req.Path),
		strings.TrimSpace(req.Heading), strings.ToLower(strings.TrimSpace(req.Commit))
	switch {
	case agentMemCheckName(req.Name) != nil:
		return agentMemWriteResult{}, bad("name is not a memory name")
	case req.Revision < 1:
		return agentMemWriteResult{}, bad("revision must be the one the proposal quoted")
	case !agentMemDocPathOK(req.Path):
		return agentMemWriteResult{}, bad("path must be a normalised relative path")
	case !agentMemSectionOK(req.Heading):
		return agentMemWriteResult{}, bad("heading must be one line of at most %d characters, without the leading #", memorypromote.MaxSection)
	case !agentMemCommitIDRe.MatchString(req.Commit):
		return agentMemWriteResult{}, bad("commit must be a hex commit id")
	}
	c, err := agentMemMemberCaller(agentMemScopeProject, req.Project)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	target, err := agentMemSupersedeDocCheck(c.Project, req.Path, req.Heading, req.Commit)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	rel, err := agentMemScopeDir(agentMemScopeProject, c)
	if err != nil {
		return agentMemWriteResult{}, err
	}

	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	cur, exists, err := agentMemLoadOne(c, agentMemScopeProject, req.Name)
	switch {
	case err != nil:
		return agentMemWriteResult{}, err
	case !exists:
		return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no memory by that name")
	case cur.Withheld:
		return agentMemWriteResult{}, conflict("this memory is withheld; fix or remove the file first")
	case agentMemExcluded(cur.Entry):
		return agentMemWriteResult{}, conflict("this memory is archived or superseded already")
	case cur.Entry.Revision != req.Revision:
		return agentMemWriteResult{}, conflict("this memory is at revision %d, not the %d the proposal quoted; propose it again so the newer text is not hidden",
			cur.Entry.Revision, req.Revision)
	}

	stamp := now.UTC().Format(time.RFC3339)
	e := cur.Entry // every field the supersede does not name is kept (ADR 0130 decision 3)
	e.SupersededBy = &agentMemSuperseded{Document: &target}
	e.InvalidAt = stamp
	e.Revision++
	e.AuthorKind, e.AuthorSession, e.Updated = c.Kind, c.Session, stamp
	data := agentMemRender(e)
	if f := agentMemScanText("memory", string(data)); len(f) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: f}
	}
	repoRel := rel + "/" + req.Name + ".md"
	rev, err := agentMemApplyLocked([]agentMemChange{{Rel: repoRel, Data: data}}, agentMemOpSupersede, repoRel, c, now)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	agentMemPrimeScan(data, agentMemScopeProject, req.Name)
	return agentMemWriteResult{Name: req.Name, Scope: agentMemScopeProject, Revision: e.Revision, Commit: rev}, nil
}

// HandleAgentMemorySupersedeDocument supersedes a project memory by a merged document. It is the
// member's decision, so it answers only a request the CP marked as theirs (ADR 0113); no MCP tool
// or session reaches it.
func HandleAgentMemorySupersedeDocument(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	var req agentMemSupersedeDocReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	res, err := agentMemSupersedeDocument(req, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
