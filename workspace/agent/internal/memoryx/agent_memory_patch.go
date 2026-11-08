package memoryx

// memory_patch (ADR 0130 decision 4): replace parts of a memory's body by exact text, so fixing
// one line does not re-send the whole memory. It shares agentMemSave's revision check, secret
// scan, author rules and commit; it differs in that it never creates and never touches any field
// but the body, the revision, the author and the update time (and the description when given).

import (
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// agentMemMaxPatchEdits bounds one patch; a rewrite that needs more is a memory_save.
const agentMemMaxPatchEdits = 50

// agentMemPatchEdit replaces the one occurrence of Old in the body with New. New is a pointer
// so that an omitted or null `new` (refused) is not confused with an explicit "" (a deletion).
type agentMemPatchEdit struct {
	Old string  `json:"old"`
	New *string `json:"new"`
}

// agentMemPatchReq is a patch. Revision is the one the caller read and must match the current
// file; 0 is refused because a patch never creates. Description is a pointer so that "not
// given" (keep) is not confused with an empty replacement (refused).
type agentMemPatchReq struct {
	Session     string              `json:"session"`
	Scope       string              `json:"scope"`
	Name        string              `json:"name"`
	Revision    int                 `json:"revision"`
	Edits       []agentMemPatchEdit `json:"edits"`
	Description *string             `json:"description,omitempty"`
}

// agentMemApplyEdits matches every edit against the same original body and applies them
// together. Errors name the edit's position, never its text: an `old` that failed to match has
// not been scanned. A self-overlapping `old` ("aa" in "aaa") counts as matching more than once.
func agentMemApplyEdits(body string, edits []agentMemPatchEdit) (string, error) {
	bad := func(format string, args ...any) error {
		return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, format, args...)
	}
	type span struct{ start, end, edit int }
	spans := make([]span, 0, len(edits))
	for i, ed := range edits {
		if ed.Old == "" {
			return "", bad("edit %d: old must not be empty", i+1)
		}
		if ed.New == nil {
			return "", bad("edit %d: new is required (use \"\" to delete the old text)", i+1)
		}
		first := strings.Index(body, ed.Old)
		if first < 0 {
			return "", bad("edit %d: old text does not occur in the body; read the memory again and copy the text exactly", i+1)
		}
		if next := strings.Index(body[first+1:], ed.Old); next >= 0 {
			return "", bad("edit %d: old text occurs more than once in the body; include more surrounding text so it matches once", i+1)
		}
		spans = append(spans, span{first, first + len(ed.Old), i})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return "", bad("edits %d and %d overlap in the body; merge them into one edit", spans[i-1].edit+1, spans[i].edit+1)
		}
	}
	var b strings.Builder
	at := 0
	for _, s := range spans {
		b.WriteString(body[at:s.start])
		b.WriteString(*edits[s.edit].New)
		at = s.end
	}
	b.WriteString(body[at:])
	return b.String(), nil
}

// agentMemPatch applies a patch as one revision and one commit.
func agentMemPatch(c agentMemCaller, req agentMemPatchReq, now time.Time) (agentMemWriteResult, error) {
	bad := func(msg string) error { return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "%s", msg) }
	req.Name = strings.TrimSpace(req.Name)
	switch {
	case !agentMemValidName(req.Name):
		return agentMemWriteResult{}, agentMemBadName()
	case req.Revision < 0:
		return agentMemWriteResult{}, bad("revision must not be negative")
	case req.Revision == 0:
		return agentMemWriteResult{}, bad("memory_patch needs the revision you read (1 or more); it never creates a memory, use memory_save for that")
	case len(req.Edits) == 0 && req.Description == nil:
		return agentMemWriteResult{}, bad("give at least one edit or a new description")
	case len(req.Edits) > agentMemMaxPatchEdits:
		return agentMemWriteResult{}, bad("at most 50 edits in one patch")
	}
	if req.Description != nil {
		d := strings.TrimSpace(*req.Description)
		req.Description = &d
		switch {
		case d == "":
			return agentMemWriteResult{}, bad("description must not be empty; omit it to keep the current one")
		case strings.ContainsAny(d, "\r\n"):
			return agentMemWriteResult{}, bad("description must be one line")
		case utf8.RuneCountInString(d) > agentMemMaxDescription:
			return agentMemWriteResult{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "description is longer than %d characters", agentMemMaxDescription)
		}
	}
	req.Scope = agentMemDefaultScope(req.Scope, c)
	rel, err := agentMemScopeDir(req.Scope, c)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	// The inserted text is scanned as the caller wrote it, before any lock is taken; the whole
	// rendered file is scanned again below, which also covers a secret an edit assembles from
	// the text around it.
	var findings []memorySecretFinding
	if req.Description != nil {
		findings = append(findings, agentMemScanText("description", *req.Description)...)
	}
	for _, ed := range req.Edits {
		if ed.New != nil {
			findings = append(findings, agentMemScanText("body", *ed.New)...)
		}
	}
	if len(findings) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: findings}
	}

	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	cur, exists, err := agentMemLoadOne(c, req.Scope, req.Name)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	// A memory an outside client may not read is as good as missing to it.
	if exists && !cur.Withheld && c.Kind == agentMemExternalKind && !agentMemAppliesTo(cur.Entry, c.Kind) {
		exists = false
	}
	switch {
	case !exists:
		return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound,
			"no memory by that name in this scope; memory_patch never creates one, use memory_save")
	case cur.Withheld:
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"this file is withheld until your user fixes it; it cannot be patched")
	case req.Revision != cur.Entry.Revision:
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"the memory is at revision %d, not %d; read it again and redo the patch against the current text", cur.Entry.Revision, req.Revision)
	}

	body, err := agentMemApplyEdits(cur.Entry.Body, req.Edits)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	// The result is stored as the edits made it: only the emptiness check looks through
	// whitespace, so an indentation the caller inserted, or one an imported body already had,
	// survives (render still normalises the trailing newlines).
	switch {
	case strings.TrimSpace(body) == "":
		return agentMemWriteResult{}, bad("the patch would leave the body empty; forget the memory instead")
	case len(body) > agentMemMaxBody:
		return agentMemWriteResult{}, memoryErrf(http.StatusRequestEntityTooLarge, errCodeMemoryTooLarge, "body would be larger than %d bytes", agentMemMaxBody)
	}

	stamp := now.UTC().Format(time.RFC3339)
	e := cur.Entry // every field the patch does not name is kept (ADR 0130 decision 3)
	e.Body, e.Revision = body, cur.Entry.Revision+1
	e.AuthorKind, e.AuthorSession, e.Updated = c.Kind, c.Session, stamp
	if req.Description != nil {
		e.Description = *req.Description
	}
	data := agentMemRender(e)
	repoRel := rel + "/" + req.Name + ".md"
	if f := agentMemScanText("memory", string(data)); len(f) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: f}
	}
	changes, err := agentMemEntryChanges(c, req.Scope, repoRel, data)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	rev, err := agentMemApplyLocked(changes, "update", repoRel, c, now)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	agentMemPrimeScan(data, req.Scope, req.Name)
	return agentMemWriteResult{Name: e.Name, Scope: e.Scope, Revision: e.Revision, Commit: rev,
		Warnings: agentMemSaveWarnings(agentMemSaveReq{Description: e.Description, Body: e.Body})}, nil
}

// agentMemEntryChanges is the file change of a memory write plus, for a project write, the
// project's own record. The member and an external client name a project that already holds
// memory and have no working copy to describe it, so the project's record is left as the
// sessions wrote it, unless the external client started the project from a working copy.
func agentMemEntryChanges(c agentMemCaller, scope, repoRel string, data []byte) ([]agentMemChange, error) {
	changes := []agentMemChange{{Rel: repoRel, Data: data}}
	if c.Project != nil && scope == agentMemScopeProject && (c.startProject || c.Kind != agentMemMember.Kind && c.Kind != agentMemExternalKind) {
		extra, err := agentMemProjectInfoChange(*c.Project)
		if err != nil {
			return nil, err
		}
		changes = append(changes, extra...)
	}
	return changes, nil
}

// agentMemNearNames lists the existing memories of scope that a new memory named name probably
// duplicates, best first: name words (3 characters or more) found in another memory's name or
// description, ranked with memory_search's field weights, and near only when at least half of
// the words (and two, unless the name has one) are found. The caller holds agentMemMu.
func agentMemNearNames(c agentMemCaller, scope, name string) []string {
	var terms []string
	seen := map[string]bool{}
	for _, t := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
		if len(t) >= 3 && !seen[t] {
			seen[t] = true
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		return nil
	}
	need := max(min(2, len(terms)), (len(terms)+1)/2)
	es, _, err := agentMemLoadScope(scope, c)
	if err != nil {
		return nil
	}
	var near []agentMemScored
	for _, e := range es {
		if e.Name == name || !agentMemAppliesTo(e, c.Kind) {
			continue
		}
		e.Body = "" // the ranking of a name hint is name and description only
		if s := agentMemScore(e, terms); len(s.matched) >= need {
			near = append(near, s)
		}
	}
	sort.SliceStable(near, func(i, j int) bool {
		if near[i].score != near[j].score {
			return near[i].score > near[j].score
		}
		return near[i].e.Name < near[j].e.Name
	})
	out := make([]string, 0, 3)
	for _, s := range near {
		if len(out) == 3 {
			break
		}
		out = append(out, s.e.Name)
	}
	return out
}

// agentMemNearHint is the hint a create answers with when agentMemNearNames found something.
func agentMemNearHint(near []string) string {
	return "similar memories already exist (" + strings.Join(near, ", ") + "): prefer memory_patch on " + near[0] +
		" (read it for its revision) over a second memory about the same thing; this one is saved either way"
}
