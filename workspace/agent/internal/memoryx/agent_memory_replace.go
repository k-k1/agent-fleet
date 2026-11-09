package memoryx

// The replacement writer of the consolidator (ADR 0130 decision 6, #3696): one new memory and the
// supersede marks of the memories it replaces, published as ONE commit. A supersede or merge is
// approved and reverted as a unit, so the history never holds the half-applied state the earlier
// two-change design would have allowed (a replacement without its marks, or marks that point at
// nothing).
//
// The commit names the new memory in AF-Memory, which is the row the change list shows, and the
// replaced ones in AF-Memory-Also. Every reader that decides "the newest change of a path" asks
// for both (agentMemChangesOf), so a later change to a replaced memory makes the unit no longer
// the newest, and the revert (agentMemRevertGroup) starts only while every side is still as the
// commit left it.

import (
	"bytes"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const (
	agentMemOpReplace   = "replace"
	agentMemAlsoTrailer = "AF-Memory-Also"
	agentMemMaxReplaced = 8
)

// agentMemReplaceSource is one memory a replacement supersedes, with the revision the proposal saw.
type agentMemReplaceSource struct {
	Name     string
	Revision int
}

// agentMemReplaceReq is the new memory and the memories it replaces.
type agentMemReplaceReq struct {
	Scope       string
	Name        string
	Description string
	Type        string
	Body        string
	Sources     []agentMemReplaceSource
}

// agentMemReplace publishes the replacement in one commit. Under the store lock it re-checks
// every side: the new name is free and was never used (no revival), and each replaced memory still
// exists, is visible, and is at the revision the proposal saw. A refusal on any side writes nothing.
func agentMemReplace(c agentMemCaller, req agentMemReplaceReq, now time.Time) (agentMemWriteResult, error) {
	bad := func(msg string) error { return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "%s", msg) }
	save := agentMemSaveReq{Session: c.Session, Scope: req.Scope, Name: req.Name, Description: req.Description,
		Type: req.Type, Body: req.Body}
	if err := agentMemValidate(&save); err != nil {
		return agentMemWriteResult{}, err
	}
	switch seen := map[string]bool{}; {
	case len(req.Sources) == 0:
		return agentMemWriteResult{}, bad("a replacement names at least one memory it replaces")
	case len(req.Sources) > agentMemMaxReplaced:
		return agentMemWriteResult{}, bad("a replacement replaces at most 8 memories")
	default:
		for _, s := range req.Sources {
			switch {
			case !agentMemValidName(s.Name):
				return agentMemWriteResult{}, agentMemBadName()
			case s.Name == save.Name:
				return agentMemWriteResult{}, bad("a replacement cannot replace itself")
			case seen[s.Name]:
				return agentMemWriteResult{}, bad("a memory is named twice")
			case s.Revision < 1:
				return agentMemWriteResult{}, bad("each replaced memory needs the revision the proposal saw")
			}
			seen[s.Name] = true
		}
	}
	rel, err := agentMemScopeDir(save.Scope, c)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	var findings []memorySecretFinding
	findings = append(findings, agentMemScanText("name", save.Name)...)
	findings = append(findings, agentMemScanText("description", save.Description)...)
	findings = append(findings, agentMemScanText("body", save.Body)...)
	if len(findings) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: findings}
	}

	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	dir, err := agentMemCheckDir(rel, true)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	// The new side: absent, and never used. agentMemSave would continue a tombstone's revision, and
	// a memory that is merely superseded still has its file, so the name is taken.
	cur, exists, err := agentMemLoadFile(filepath.Join(dir, save.Name+".md"), save.Scope, save.Name)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	if exists || cur.Withheld {
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "a memory by that name exists")
	}
	if err := agentMemRefuseRevival(rel, save.Name); err != nil {
		return agentMemWriteResult{}, err
	}

	stamp := now.UTC().Format(time.RFC3339)
	repoRel := rel + "/" + save.Name + ".md"
	fresh := agentMemEntry{Name: save.Name, Scope: save.Scope, Description: save.Description, Type: save.Type,
		Kinds: save.Kinds, Revision: 1, AuthorKind: c.Kind, AuthorSession: c.Session, Created: stamp, Updated: stamp,
		Body: save.Body}
	newData := agentMemRender(fresh)
	datas := [][]byte{newData}
	if f := agentMemScanText("memory", string(newData)); len(f) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: f}
	}
	changes, err := agentMemEntryChanges(c, save.Scope, repoRel, newData)
	if err != nil {
		return agentMemWriteResult{}, err
	}

	var also []string
	names := []string{save.Name}
	for _, s := range req.Sources {
		old, ok, err := agentMemLoadFile(filepath.Join(dir, s.Name+".md"), save.Scope, s.Name)
		switch {
		case err != nil:
			return agentMemWriteResult{}, err
		case !ok:
			return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "a memory to replace no longer exists")
		case old.Withheld:
			return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "a memory to replace is withheld")
		case agentMemExcluded(old.Entry):
			return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "a memory to replace is archived or superseded already")
		case old.Entry.Revision != s.Revision:
			return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
				"a memory to replace is at revision %d, not the %d the proposal saw", old.Entry.Revision, s.Revision)
		}
		e := old.Entry // every field the supersede does not name is kept (ADR 0130 decision 3)
		e.SupersededBy = &agentMemSuperseded{Memory: save.Name}
		e.InvalidAt = stamp
		e.Revision++
		e.AuthorKind, e.AuthorSession, e.Updated = c.Kind, c.Session, stamp
		data := agentMemRender(e)
		if f := agentMemScanText("memory", string(data)); len(f) > 0 {
			return agentMemWriteResult{}, &agentMemSecretErr{Findings: f}
		}
		oldRel := rel + "/" + s.Name + ".md"
		changes = append(changes, agentMemChange{Rel: oldRel, Data: data})
		datas = append(datas, data)
		also = append(also, agentMemRepoPrefix+"/"+oldRel)
		names = append(names, s.Name)
	}

	rev, err := agentMemApplyLocked(changes, agentMemOpReplace, repoRel, c, now,
		agentMemAlsoTrailer+": "+strings.Join(also, ","))
	if err != nil {
		return agentMemWriteResult{}, err
	}
	agentMemRemoveTomb(rel, save.Name)
	agentMemClearUsage(rel, save.Name)
	for i, d := range datas {
		agentMemPrimeScan(d, save.Scope, names[i])
	}
	return agentMemWriteResult{Name: save.Name, Scope: save.Scope, Revision: 1, Commit: rev, Created: true}, nil
}

// agentMemAlsoRels reads the AF-Memory-Also trailer of a commit message: the other memories the
// change wrote, as store-relative paths. ok=false when the trailer is present but any entry is not
// a memory path of the primary's directory, so a row carrying one is withheld rather than guessed at.
func agentMemAlsoRels(trailers map[string]string, primaryRel string) (rels []string, ok bool) {
	raw := trailers[agentMemAlsoTrailer]
	if raw == "" {
		return nil, true
	}
	dir := filepath.ToSlash(filepath.Dir(primaryRel))
	for _, p := range strings.Split(raw, ",") {
		rel, _, _, _, pok := agentMemParseRepoPath(strings.TrimSpace(p))
		if !pok || filepath.ToSlash(filepath.Dir(rel)) != dir || rel == primaryRel {
			return nil, false
		}
		rels = append(rels, rel)
	}
	return rels, true
}

// agentMemRevertGroup undoes a change that wrote several memories, as one revert commit: the new
// memory goes (or comes back, when the change being undone was itself a revert) and each replaced
// memory returns to its text before the change. Like a single-memory revert it starts only while
// every side is exactly as the commit left it and the commit is the newest change of every side,
// so nothing written since is dropped. A side without both texts in the history (purged, or never
// committed) refuses the whole revert: a half restored unit is the state this writer exists to avoid.
// The caller holds agentMemMu and memorySnapshotMu.
func agentMemRevertGroup(req agentMemRevertReq, commit string, rels []string, now time.Time) (agentMemWriteResult, error) {
	conflict := func(format string, a ...any) error {
		return memoryErrf(http.StatusConflict, errCodeMemoryConflict, format, a...)
	}
	if req.Forget {
		return agentMemWriteResult{}, conflict("this change wrote several memories; revert it as a whole instead of forgetting one")
	}
	msg, err := memoryGitRun("log", "-1", "--format=%B", commit)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	if agentMemTrailers(msg)[agentMemPurgedTrailer] != "" {
		return agentMemWriteResult{}, conflict("the earlier revisions of a memory of this change were purged from the history; there is nothing to go back to")
	}
	type side struct {
		rel, name         string
		before, after     []byte
		beforeOK, afterOK bool
		live              []byte
		liveOK            bool
		floor             int
	}
	scopeDir := filepath.ToSlash(filepath.Dir(rels[0]))
	_, scope, _, _, _ := agentMemParseRepoPath(agentMemRepoPrefix + "/" + rels[0])
	dir, err := agentMemCheckDir(scopeDir, true)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	sides := make([]side, len(rels))
	for i, r := range rels {
		sd := side{rel: r}
		_, _, _, sd.name, _ = agentMemParseRepoPath(agentMemRepoPrefix + "/" + r)
		latest, err := agentMemLatestChange(r)
		if err != nil {
			return agentMemWriteResult{}, err
		}
		if latest != commit {
			return agentMemWriteResult{}, conflict("a memory of this change has changed since; only a change that is the newest of every memory it wrote can be reverted")
		}
		if sd.before, sd.beforeOK, sd.after, sd.afterOK, err = agentMemSides(commit, r); err != nil {
			return agentMemWriteResult{}, err
		}
		if i > 0 && !(sd.beforeOK && sd.afterOK) {
			return agentMemWriteResult{}, conflict("the history does not hold both texts of a memory of this change")
		}
		if !sd.beforeOK && !sd.afterOK {
			return agentMemWriteResult{}, conflict("there is no earlier text to bring back: the history never held this memory's text")
		}
		if sd.live, sd.liveOK, err = agentMemReadFile(filepath.Join(dir, sd.name+".md")); err != nil {
			return agentMemWriteResult{}, err
		}
		if sd.liveOK != sd.afterOK || !bytes.Equal(sd.live, sd.after) {
			return agentMemWriteResult{}, conflict("a memory of this change has changed since; only a change that is the newest of every memory it wrote can be reverted")
		}
		if i == 0 && req.ExpectRevision > 0 && (!sd.liveOK || agentMemRevOf(sd.live, sd.liveOK) != req.ExpectRevision) {
			return agentMemWriteResult{}, conflict("the memory is no longer at revision %d; reload the list and try again", req.ExpectRevision)
		}
		tomb, err := agentMemTombRevision(scopeDir, sd.name)
		if err != nil {
			return agentMemWriteResult{}, err
		}
		sd.floor = max(agentMemRevOf(sd.live, sd.liveOK), tomb, agentMemRevOf(sd.after, sd.afterOK), agentMemRevOf(sd.before, sd.beforeOK))
		sides[i] = sd
	}

	changes := make([]agentMemChange, 0, len(sides))
	for _, sd := range sides {
		if !sd.beforeOK {
			changes = append(changes, agentMemChange{Rel: sd.rel, Delete: true})
			continue
		}
		e, ok := agentMemParse(sd.before)
		if !ok {
			return agentMemWriteResult{}, conflict("the earlier version of a memory of this change cannot be read as a memory")
		}
		e.Name, e.Scope = sd.name, scope
		e.Revision = sd.floor + 1
		e.AuthorKind, e.AuthorSession = agentMemMember.Kind, agentMemMember.Session
		e.Updated = now.UTC().Format(time.RFC3339)
		data := agentMemRender(e)
		if !req.Ack {
			findings := append(agentMemScanText("memory", agentMemPublished(e)), agentMemScanText("memory", string(data))...)
			if len(findings) > 0 {
				return agentMemWriteResult{}, &agentMemSecretErr{Findings: findings}
			}
		}
		changes = append(changes, agentMemChange{Rel: sd.rel, Data: data})
	}
	// Undoing a creation leaves a tombstone, as a single revert does: the name is not reused.
	primary := sides[0]
	var base string
	if !primary.beforeOK {
		if base, err = forgetIntent(scopeDir, primary.name); err != nil {
			return agentMemWriteResult{}, err
		}
		if err := agentMemWriteTomb(scopeDir, primary.name, primary.floor); err != nil {
			return agentMemWriteResult{}, err
		}
	}
	also := make([]string, 0, len(rels)-1)
	for _, r := range rels[1:] {
		also = append(also, agentMemRepoPrefix+"/"+r)
	}
	rev, err := agentMemApplyLocked(changes, "revert", rels[0], agentMemMember, now,
		"AF-Revert-Of: "+commit, agentMemAlsoTrailer+": "+strings.Join(also, ","))
	if err != nil {
		return agentMemWriteResult{}, err
	}
	res := agentMemWriteResult{Name: primary.name, Scope: scope, Commit: rev}
	if primary.beforeOK {
		res.Revision = primary.floor + 1
		res.Created = !primary.liveOK
		if !primary.liveOK {
			agentMemRemoveTomb(scopeDir, primary.name)
			agentMemClearUsage(scopeDir, primary.name)
		}
	} else {
		res.Revision, res.Deleted = agentMemRevOf(primary.live, primary.liveOK), true
		agentMemClearUsage(scopeDir, primary.name)
		forgetDone(scopeDir, primary.name, base)
	}
	return res, nil
}

// agentMemRefuseGroupForget refuses to forget a memory whose newest change wrote several memories
// (a replacement): a forget would end that unit's revert for good. The unit goes back as a whole.
// The caller holds agentMemMu.
func agentMemRefuseGroupForget(repoRel string) error {
	if !memoryHasCommits() {
		return nil
	}
	latest, err := agentMemLatestChange(repoRel)
	if err != nil || latest == "" {
		return err
	}
	body, err := memoryGitRun("log", "-1", "--format=%B", latest)
	if err != nil {
		return err
	}
	tr := agentMemTrailers(body)
	if tr[agentMemAlsoTrailer] != "" {
		return memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"this memory was written together with others by its newest change; revert that change as a whole instead of forgetting one of them")
	}
	return nil
}
