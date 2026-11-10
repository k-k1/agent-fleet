package memoryx

// The retire writer of the consolidator (ADR 0130 decision 6, #3788): one existing memory leaves
// every agent's sight because the thing it is about is finished. The file stays; the change sets
// status archived, status_by consolidator and invalid_at, and records the one-line reason as a
// reason of the same author, so the stub that memory_read answers says why. It is one commit with
// one path, so the generic revert puts the file back as it was and a later write to the memory
// ends that revert's window, exactly as for any other change.

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	agentMemOpRetire = "retire"
	// agentMemRetireRule is the rule of the reason a retire leaves.
	agentMemRetireRule = "retired"
)

// agentMemRetire archives one memory if it is still at the revision the proposal saw. Under the
// store lock it re-checks everything the approval screen showed: the memory exists, is readable,
// is visible to agents, is not pinned (a pin is the member's word) and has not moved.
func agentMemRetire(c agentMemCaller, scope, name string, revision int, reason string, now time.Time) (agentMemWriteResult, error) {
	bad := func(msg string) error { return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "%s", msg) }
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "":
		return agentMemWriteResult{}, bad("a retire needs a one-line reason")
	case agentMemDocBadChars.MatchString(reason):
		return agentMemWriteResult{}, bad("the reason must be one line")
	case revision < 1:
		return agentMemWriteResult{}, bad("a retire needs the revision the proposal saw")
	}
	reason = agentMemCutRunes(reason, agentMemMaxReasonNote)
	if f := agentMemScanText("reason", reason); len(f) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: f}
	}
	rel, err := agentMemScopeDir(scope, c)
	if err != nil {
		return agentMemWriteResult{}, err
	}

	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	conflict := func(format string, a ...any) error {
		return memoryErrf(http.StatusConflict, errCodeMemoryConflict, format, a...)
	}
	cur, exists, err := agentMemLoadOne(c, scope, name)
	switch {
	case err != nil:
		return agentMemWriteResult{}, err
	case !exists:
		return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "the memory to retire no longer exists")
	case cur.Withheld:
		return agentMemWriteResult{}, conflict("the memory to retire is withheld")
	case agentMemExcluded(cur.Entry):
		return agentMemWriteResult{}, conflict("the memory to retire is archived or superseded already")
	case cur.Entry.Pinned:
		return agentMemWriteResult{}, conflict("the memory to retire is pinned")
	case cur.Entry.Revision != revision:
		return agentMemWriteResult{}, conflict("the memory to retire is at revision %d, not the %d the proposal saw", cur.Entry.Revision, revision)
	}

	stamp := now.UTC().Format(time.RFC3339)
	e := cur.Entry // every field the retire does not name is kept (ADR 0130 decision 3)
	e.Revision++
	e.Status, e.StatusBy, e.InvalidAt = agentMemStatusArchived, agentMemStatusByConsol, stamp
	// The doubts already on the memory stay, oldest first dropped when the list is full: the new
	// reason is the one the stub has to show.
	kept := e.Reasons
	if over := len(kept) + 1 - agentMemMaxReasons; over > 0 {
		kept = kept[over:]
	}
	e.Reasons = append(append([]agentMemReason(nil), kept...),
		agentMemReason{By: agentMemReasonByConsol, Rule: agentMemRetireRule, Note: reason, Revision: e.Revision})
	e.AuthorKind, e.AuthorSession, e.Updated = c.Kind, c.Session, stamp
	data := agentMemRender(e)
	if f := agentMemScanText("memory", string(data)); len(f) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: f}
	}
	repoRel := rel + "/" + name + ".md"
	rev, err := agentMemApplyLocked([]agentMemChange{{Rel: repoRel, Data: data}}, agentMemOpRetire, repoRel, c, now)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	agentMemPrimeScan(data, scope, name)
	return agentMemWriteResult{Name: name, Scope: scope, Revision: e.Revision, Commit: rev}, nil
}

// ReviewRetireReasonOK says whether a retire's reason is acceptable text: one non-empty line. The
// store re-checks it (and the secret scan) when it writes.
func ReviewRetireReasonOK(reason string) bool {
	r := strings.TrimSpace(reason)
	return r != "" && !agentMemDocBadChars.MatchString(r) && utf8.ValidString(r)
}
