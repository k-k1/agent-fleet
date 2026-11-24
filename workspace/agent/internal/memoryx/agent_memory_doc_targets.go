package memoryx

// Lint of document targets (ADR 0130 decision 11): a project memory superseded by a repository
// document keeps pointing at it, so the target is an anchor lint checks on every report, on the
// superseded memory itself. A renamed or deleted file, a vanished heading, a merge that was
// reverted or a merge commit the default branch no longer holds is reported to the member, who
// re-points the target (agentMemDocRetarget) or reverts the supersede (agentMemDocRestore). The
// body never comes back by itself: lint only reports here, it writes no reason and no target.
//
// Every read is by ref from the git-common-dir, pinned to the same commit as the rest of the
// report, and fetches nothing.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorypromote"
)

// Problems a document target can have, most fundamental first: only the first that holds is
// reported, because the later ones follow from it (a reverted merge usually took the heading too).
const (
	// agentMemDocOtherRepo: the target names another repository than the project's origin, so this
	// repository cannot say anything about it.
	agentMemDocOtherRepo = "other-repository"
	// agentMemDocUnreachable: the merge commit is missing, or no longer on the default branch (a
	// rewritten history).
	agentMemDocUnreachable = "unreachable"
	// agentMemDocReverted: a commit on the default branch reverts the merge, and nothing reverted
	// that revert.
	agentMemDocReverted = "reverted"
	// agentMemDocRenamed: the file is gone and the history says where it went (Path).
	agentMemDocRenamed = "renamed"
	// agentMemDocMissing: the file is gone and no rename explains it.
	agentMemDocMissing = "missing"
	// agentMemDocHeading: the file is there but the heading is not.
	agentMemDocHeading = "heading"
)

const agentMemOpRetarget, agentMemOpRestore = "retarget", "restore"

// agentMemDocFinding is one superseded memory whose document target no longer holds. Revision is
// the memory's, for the repair's compare-and-swap.
type agentMemDocFinding struct {
	Memory   string            `json:"memory"`
	Revision int               `json:"revision"`
	Target   agentMemDocTarget `json:"target"`
	Problem  string            `json:"problem"`
	// Path is where a renamed file is now (renamed only); HeadingFound says whether the target's
	// heading is in it at the revision the report read, so a re-point to that revision can keep
	// the heading. The renaming commit itself may not hold the heading: it is evidence, not the
	// commit to re-point to.
	Path         string `json:"path,omitempty"`
	HeadingFound bool   `json:"headingFound,omitempty"`
	// Commit is the evidence: the commit that renamed the file, or the one that reverted the merge.
	Commit string `json:"commit,omitempty"`
}

// agentMemDocReport is the document-target part of the lint report.
type agentMemDocReport struct {
	// Checked counts the memories superseded by a document that were checked.
	Checked  int                  `json:"checked"`
	Findings []agentMemDocFinding `json:"findings"`
}

// agentMemDocRevertMsg is the line `git revert` writes; a revert of a merge adds ", reversing
// changes made to <parent>" after the id, so the id is matched as a prefix of the rest.
const agentMemDocRevertMsg = "This reverts commit "

// agentMemDocRevertDepth bounds how many reverts of reverts are followed; past it the target is
// not judged at all, because stopping early would decide by the parity of the depth. A variable so
// a test can lower it.
var agentMemDocRevertDepth = 16

var errAgentMemDocRevertDeep = errors.New("too many reverts of reverts to tell whether the merge is in effect")

// agentMemDocSaysRevert says whether msg says, as git revert writes it, that it reverts commit:
// outside a code fence, a line that starts with the sentence and the full id, then "." or, for a
// merge, ", reversing" with the next line "changes made to <parent>.". A quote of git's output in
// a fence, an indented quote or a longer id is no evidence.
func agentMemDocSaysRevert(msg, commit string) bool {
	lines := strings.Split(msg, "\n")
	var fence byte // the fence character while inside a fenced block
	fenceLen := 0
	for i, ln := range lines {
		ln = strings.TrimRight(ln, " \t\r")
		// CommonMark fences: up to three spaces, then three or more of ` or ~. A block closes
		// only on a run of the same character at least as long, with nothing after it.
		t := strings.TrimLeft(ln, " ")
		if len(ln)-len(t) <= 3 && len(t) >= 3 && (t[0] == '`' || t[0] == '~') {
			n := 0
			for n < len(t) && t[n] == t[0] {
				n++
			}
			switch {
			case n < 3:
			case fence == 0 && !(t[0] == '`' && strings.ContainsRune(t[n:], '`')):
				fence, fenceLen = t[0], n
				continue
			case fence == t[0] && n >= fenceLen && n == len(t):
				fence, fenceLen = 0, 0
				continue
			}
		}
		if fence != 0 {
			continue
		}
		rest, ok := strings.CutPrefix(ln, agentMemDocRevertMsg+commit)
		switch {
		case !ok:
		case rest == ".":
			return true
		case rest == ", reversing" && i+1 < len(lines) &&
			agentMemDocRevertParentRe.MatchString(strings.TrimRight(lines[i+1], " \t\r")):
			return true
		}
	}
	return false
}

var agentMemDocRevertParentRe = regexp.MustCompile(`^changes made to [0-9a-f]{40}(?:[0-9a-f]{24})?\.$`)

// agentMemDocRevertOf finds the newest commit in (commit, ref] that reverts commit and is itself in
// effect (no later commit reverts it, and stays so). "" when there is none.
func agentMemDocRevertOf(ctx context.Context, gitDir, commit, ref string, depth int) (string, error) {
	if depth >= agentMemDocRevertDepth {
		return "", errAgentMemDocRevertDeep
	}
	// --grep only narrows the candidates: it also matches the sentence quoted mid-line.
	out, err := agentMemLintGit(ctx, gitDir, nil, "log", "--format=%H", "-F", "--grep="+agentMemDocRevertMsg+commit, commit+".."+ref)
	if err != nil {
		return "", err
	}
	for _, rev := range strings.Fields(string(out)) {
		msg, err := agentMemLintGit(ctx, gitDir, nil, "log", "-1", "--format=%B", rev)
		if err != nil {
			return "", err
		}
		if !agentMemDocSaysRevert(string(msg), commit) {
			continue
		}
		// A revert that was itself reverted (and stayed so) brought the change back; an older
		// revert may still be in effect.
		again, err := agentMemDocRevertOf(ctx, gitDir, rev, ref, depth+1)
		if err != nil {
			return "", err
		}
		if again == "" {
			return rev, nil
		}
	}
	return "", nil
}

// agentMemDocCheckOne checks one target against the pinned revision. have is the set of the
// revision's files; ok is false when nothing is wrong.
func agentMemDocCheckOne(ctx context.Context, repo agentMemLintRepo, remote string, t agentMemDocTarget, have map[string]bool) (agentMemDocFinding, bool, error) {
	f := agentMemDocFinding{Target: t}
	report := func(problem string) (agentMemDocFinding, bool, error) {
		f.Problem = problem
		return f, true, nil
	}
	if t.Repo != remote {
		return report(agentMemDocOtherRepo)
	}
	full, err := agentMemResolveCommit(ctx, repo.gitDir, t.Commit)
	if err != nil {
		return f, false, err
	}
	if full == "" {
		return report(agentMemDocUnreachable)
	}
	if _, err := agentMemLintGit(ctx, repo.gitDir, nil, "merge-base", "--is-ancestor", full, repo.ref); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return report(agentMemDocUnreachable)
		}
		return f, false, err
	}
	rev, err := agentMemDocRevertOf(ctx, repo.gitDir, full, repo.ref, 0)
	if err != nil {
		return f, false, err
	}
	if rev != "" {
		f.Commit = rev
		return report(agentMemDocReverted)
	}
	hasHeading := func(path string) (bool, error) {
		if t.Heading == "" {
			return true, nil
		}
		doc, err := agentMemDocBlob(ctx, repo.gitDir, repo.ref, path)
		if err != nil {
			return false, err
		}
		_, ok := memorypromote.FindHeading(doc, t.Heading)
		return ok, nil
	}
	blob, _, err := agentMemTreeBlob(ctx, repo.gitDir, repo.ref, t.Path)
	if err != nil {
		return f, false, err
	}
	if !blob {
		to, commit, err := agentMemDocFollow(ctx, repo.gitDir, t.Path, full, repo.ref)
		if err != nil {
			return f, false, err
		}
		if to == "" || !have[to] {
			return report(agentMemDocMissing)
		}
		f.Path, f.Commit = to, commit
		if f.HeadingFound, err = hasHeading(f.Path); err != nil {
			return f, false, err
		}
		return report(agentMemDocRenamed)
	}
	found, err := hasHeading(t.Path)
	if err != nil {
		return f, false, err
	}
	if !found {
		return report(agentMemDocHeading)
	}
	return f, false, nil
}

// agentMemDocMaxBytes bounds a document read for its headings; a larger one is not judged, so a
// generated file that replaced a small document never lands whole in the Agent's memory. A
// variable so a test can lower it.
var agentMemDocMaxBytes int64 = 4 << 20

var errAgentMemDocTooLarge = errors.New("the document is too large to read its headings")

// agentMemDocBlob reads path at rev after asking its size.
func agentMemDocBlob(ctx context.Context, gitDir, rev, path string) ([]byte, error) {
	out, err := agentMemLintGit(ctx, gitDir, nil, "cat-file", "-s", rev+":"+path)
	if err != nil {
		return nil, err
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err != nil || n > agentMemDocMaxBytes {
		return nil, errAgentMemDocTooLarge
	}
	return agentMemLintGit(ctx, gitDir, nil, "cat-file", "blob", rev+":"+path)
}

// agentMemDocFollow follows path through the default branch's renames after target, oldest
// first, and answers where it is now and the commit of its last move; "" when it was not moved,
// or was deleted on the way.
// Following one file forward keeps a later file by the same name (a new AGENTS.md moved elsewhere)
// out of its history. When the target's commit came in on a branch, the merge that brought it
// adds the file, which leaves it where it is. A rename whose paths git had to quote cannot be read and is not followed.
func agentMemDocFollow(ctx context.Context, gitDir, path, target, ref string) (to, commit string, err error) {
	// Start where the target entered the default branch: the oldest first-parent commit that
	// descends from it (the target itself on the branch, else the merge that brought it in), from
	// that commit's first parent. A range from the target alone would also hold the default
	// branch's own commits from before that merge, a delete of the same name among them.
	out, err := agentMemLintGit(ctx, gitDir, nil, "rev-list", "--first-parent", "--ancestry-path", "--reverse", target+".."+ref)
	if err != nil {
		return "", "", err
	}
	first, _, _ := strings.Cut(string(out), "\n")
	if first = strings.TrimSpace(first); first == "" {
		return "", "", nil // the target is the branch's head: nothing moved since
	}
	since := first + "^1"
	// The default branch's own history: its first parents, a merge compared with its first parent.
	// A branch's commits are not read one by one, so a delete the merge did not keep is not taken.
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+gitDir, "-c", "core.quotePath=false", "log", "--reverse",
		"--first-parent", "--diff-merges=first-parent", "-M", "--name-status", "--no-color", "--no-ext-diff", "--format=%x01%H", since+".."+ref)
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", err
	}
	if err := cmd.Start(); err != nil {
		return "", "", err
	}
	cur, at, gone := path, "", false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "\x01"); ok {
			at = rest
			continue
		}
		if gone {
			continue
		}
		f := strings.Split(line, "\t")
		switch {
		case len(f) == 3 && strings.HasPrefix(f[0], "R") && f[1] == cur:
			cur, commit = f[2], at
		case len(f) == 2 && f[0] == "D" && f[1] == cur:
			gone = true
		}
	}
	scanErr := sc.Err()
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return "", "", fmt.Errorf("git log: %w", err)
	}
	if scanErr != nil {
		return "", "", scanErr
	}
	if gone || cur == path {
		return "", "", nil
	}
	return cur, commit, nil
}

// agentMemDocAny says whether any of es is superseded by a document.
func agentMemDocAny(es []agentMemEntry) bool {
	for _, e := range es {
		if e.SupersededBy != nil && e.SupersededBy.Document != nil {
			return true
		}
	}
	return false
}

// agentMemDocLint checks the document target of every memory es holds that is superseded by one.
// It answers nil when there is none; a note when the targets could not be checked.
func agentMemDocLint(ctx context.Context, p *agentMemProject, repo agentMemLintRepo, es []agentMemEntry) (*agentMemDocReport, string) {
	var docs []agentMemEntry
	for _, e := range es {
		if e.SupersededBy != nil && e.SupersededBy.Document != nil {
			docs = append(docs, e)
		}
	}
	if len(docs) == 0 {
		return nil, ""
	}
	out := &agentMemDocReport{Checked: len(docs), Findings: []agentMemDocFinding{}}
	paths, err := repo.Paths()
	if err != nil {
		return nil, "document targets: " + err.Error()
	}
	have := make(map[string]bool, len(paths))
	for _, p := range paths {
		have[p] = true
	}
	remote := ""
	if p != nil {
		remote = p.Remote
	}
	var notes []string
	for _, e := range docs {
		cctx, cancel := context.WithTimeout(ctx, agentMemLintGitTimeout)
		f, bad, err := agentMemDocCheckOne(cctx, repo, remote, *e.SupersededBy.Document, have)
		cancel()
		if errors.Is(err, errAgentMemDocRevertDeep) || errors.Is(err, errAgentMemDocTooLarge) {
			// Not judged: it does not count as checked, so the report never reads as clean.
			out.Checked--
			notes = append(notes, "document target of "+e.Name+": "+err.Error())
			continue
		}
		if err != nil {
			return nil, "document targets: the repository could not be read"
		}
		if bad {
			f.Memory, f.Revision = e.Name, e.Revision
			out.Findings = append(out.Findings, f)
		}
	}
	return out, strings.Join(notes, "; ")
}

// ---- repairs ---------------------------------------------------------------------------------

// agentMemDocRepairReq names a memory superseded by a document, at the revision the member saw.
// The retarget also names the new path, heading and a commit on the default branch holding them.
type agentMemDocRepairReq struct {
	Project  string `json:"project"`
	Name     string `json:"name"`
	Revision int    `json:"revision"`
	Path     string `json:"path,omitempty"`
	Heading  string `json:"heading,omitempty"`
	Commit   string `json:"commit,omitempty"`
}

// agentMemDocRepair loads the memory under the store lock, checks it is still superseded by a
// document at the revision given, lets mut change it and publishes the change as op.
func agentMemDocRepair(c agentMemCaller, req agentMemDocRepairReq, op string, now time.Time, mut func(*agentMemEntry)) (agentMemWriteResult, error) {
	conflict := func(format string, a ...any) error {
		return memoryErrf(http.StatusConflict, errCodeMemoryConflict, format, a...)
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
	case cur.Entry.SupersededBy == nil || cur.Entry.SupersededBy.Document == nil:
		return agentMemWriteResult{}, conflict("this memory is not superseded by a document")
	case cur.Entry.Revision != req.Revision:
		return agentMemWriteResult{}, conflict("this memory is at revision %d, not the %d the report showed; run the report again",
			cur.Entry.Revision, req.Revision)
	}
	stamp := now.UTC().Format(time.RFC3339)
	e := cur.Entry // every field the repair does not name is kept (ADR 0130 decision 3)
	mut(&e)
	e.Revision++
	e.AuthorKind, e.AuthorSession, e.Updated = c.Kind, c.Session, stamp
	data := agentMemRender(e)
	if f := append(agentMemScanText("memory", string(data)), agentMemScanText("memory", agentMemPublished(e))...); len(f) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: f}
	}
	repoRel := rel + "/" + req.Name + ".md"
	rev, err := agentMemApplyLocked([]agentMemChange{{Rel: repoRel, Data: data}}, op, repoRel, c, now)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	agentMemPrimeScan(data, agentMemScopeProject, req.Name)
	return agentMemWriteResult{Name: req.Name, Scope: agentMemScopeProject, Revision: e.Revision, Commit: rev}, nil
}

// agentMemDocRepairCheck validates the fields every repair names.
func agentMemDocRepairCheck(req *agentMemDocRepairReq) (agentMemCaller, error) {
	req.Name = strings.TrimSpace(req.Name)
	if agentMemCheckName(req.Name) != nil {
		return agentMemCaller{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "name is not a memory name")
	}
	if req.Revision < 1 {
		return agentMemCaller{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "revision must be the one the report showed")
	}
	return agentMemMemberCaller(agentMemScopeProject, req.Project)
}

// agentMemDocRetarget points a memory's document target at a new path, heading and commit, each
// checked as the supersede checks them. The memory stays superseded; the earlier target is in the
// change's diff, and reverting the change restores it.
func agentMemDocRetarget(ctx context.Context, req agentMemDocRepairReq, now time.Time) (agentMemWriteResult, error) {
	bad := func(format string, a ...any) error {
		return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, format, a...)
	}
	req.Path, req.Heading, req.Commit = strings.TrimSpace(req.Path), strings.TrimSpace(req.Heading), strings.ToLower(strings.TrimSpace(req.Commit))
	switch {
	case !agentMemDocPathOK(req.Path):
		return agentMemWriteResult{}, bad("path must be a normalised relative path")
	case !agentMemSectionOK(req.Heading):
		return agentMemWriteResult{}, bad("heading must be one line of at most %d characters, without the leading #", memorypromote.MaxSection)
	case !agentMemCommitIDRe.MatchString(req.Commit):
		return agentMemWriteResult{}, bad("commit must be a hex commit id")
	}
	c, err := agentMemDocRepairCheck(&req)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	target, err := agentMemSupersedeDocCheck(ctx, c.Project, req.Path, req.Heading, req.Commit)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	return agentMemDocRepair(c, req, agentMemOpRetarget, now, func(e *agentMemEntry) {
		e.SupersededBy = &agentMemSuperseded{Document: &target}
	})
}

// agentMemDocRestore reverts the supersede by a document: the memory is back in agents' view with
// the body it kept. Unlike a revert of the supersede change it works after a re-point too, and it
// is itself a change the member can revert.
func agentMemDocRestore(req agentMemDocRepairReq, now time.Time) (agentMemWriteResult, error) {
	c, err := agentMemDocRepairCheck(&req)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	return agentMemDocRepair(c, req, agentMemOpRestore, now, func(e *agentMemEntry) {
		e.SupersededBy, e.InvalidAt = nil, ""
	})
}

// HandleAgentMemoryDocumentRetarget re-points a document target. The member's decision, so it
// answers only a request the CP marked as theirs (ADR 0113).
func HandleAgentMemoryDocumentRetarget(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	var req agentMemDocRepairReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	res, err := agentMemDocRetarget(r.Context(), req, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// HandleAgentMemoryDocumentRestore reverts a supersede by a document. Member-origin only.
func HandleAgentMemoryDocumentRestore(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	var req agentMemDocRepairReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	res, err := agentMemDocRestore(req, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
