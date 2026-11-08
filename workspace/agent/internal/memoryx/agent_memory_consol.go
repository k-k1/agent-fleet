package memoryx

// The store-facing half of the autonomous consolidator (ADR 0130 decisions 6 and 7). The
// consolidator (internal/memconsol) decides what to propose and when; everything that touches the
// store, its lock, its scan and its history stays here. Two rules are enforced in this file and
// not left to the caller:
//
//   - it never writes on its own: PublishConsolidation is reached only from the member-origin
//     approve route, and no af MCP tool calls it (ADR 0113);
//   - a create is refused over a name that was forgotten or ever existed, judged inside the store
//     lock the save holds (agentMemCaller.noRevival), so a check made at queue time cannot go stale.
//
// Reads here count no use: a consolidator that reads a memory must not make it look popular.

import (
	"errors"
	"os"
	"sort"
	"time"
)

const (
	// ConsolidatorAuthorKind is the author kind of an approved proposal. agentMemKindRe admits it,
	// so the change list shows it like any other author and a revert works as for any change.
	ConsolidatorAuthorKind = "consolidator"

	consolSession         = "consolidator"
	consolProposalTrailer = "AF-Consolidation-Proposal"
)

// ConsolScope names one scope of the store: the user scope, or one project by its store id.
type ConsolScope struct {
	Scope     string // user | project
	ProjectID string // for scope project
}

// ConsolScopes lists the user scope and every project directory of the store, in a stable order.
func ConsolScopes() ([]ConsolScope, error) {
	out := []ConsolScope{{Scope: agentMemScopeUser}}
	dir, err := agentMemCheckDir("projects", false)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, d := range ents {
		if d.IsDir() && agentMemValidProjectID(d.Name()) {
			ids = append(ids, d.Name())
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, ConsolScope{Scope: agentMemScopeProject, ProjectID: id})
	}
	return out, nil
}

// ConsolReason is one open doubt a memory carries (lint, feedback or member).
type ConsolReason struct {
	By       string
	Rule     string
	Note     string
	Revision int
}

// ConsolMemory is one memory as the consolidator reads it.
type ConsolMemory struct {
	Name        string
	Description string
	Type        string
	Revision    int
	Body        string
	Pinned      bool
	Reasons     []ConsolReason
}

// ConsolMemories reads the memories of one scope that agents can see: archived and superseded ones
// are left out, as everywhere an agent reads. A project the store does not know reads as empty.
// Files the store withholds (they fail its scan or do not parse) are not returned.
func ConsolMemories(scope, projectID string) ([]ConsolMemory, error) {
	c, err := agentMemMemberCaller(scope, projectID)
	if err != nil {
		var ue *memoryUserErr
		if errors.As(err, &ue) && ue.Code == errCodeMemoryNotFound {
			return nil, nil
		}
		return nil, err
	}
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	es, _, err := agentMemLoadScope(scope, c)
	if err != nil {
		return nil, err
	}
	var out []ConsolMemory
	for _, e := range es {
		if agentMemExcluded(e) {
			continue
		}
		m := ConsolMemory{Name: e.Name, Description: e.Description, Type: e.Type, Revision: e.Revision,
			Body: e.Body, Pinned: e.Pinned}
		for _, r := range e.Reasons {
			m.Reasons = append(m.Reasons, ConsolReason{By: r.By, Rule: r.Rule, Note: r.Note, Revision: r.Revision})
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ConsolEdit is one exact-text edit of a patch.
type ConsolEdit struct {
	Old, New string
}

// ConsolItem is one approved operation as the store needs it.
type ConsolItem struct {
	Op        string // patch | create
	Scope     string
	ProjectID string
	Name      string
	// Revision is the revision a patch was made against; it must still be the current one.
	Revision int
	Edits    []ConsolEdit
	// Description, Type and Body are the new memory of a create.
	Description string
	Type        string
	Body        string
}

// ConsolPublished is the outcome of PublishConsolidation. Already says an earlier call had
// published this proposal (a retry after a stop), so nothing was written now.
type ConsolPublished struct {
	Commit   string
	Revision int
	Already  bool
}

// PublishConsolidation publishes one approved operation through the store's own patch / save path,
// authored as the consolidator, with the proposal id as a trailer. A retry of a proposal that was
// published but not yet marked decided finds its commit by the trailer and writes nothing. The
// refusals are the review's (ReviewRefusal): stale, forgotten, secret, invalid.
func PublishConsolidation(proposalID string, it ConsolItem, now time.Time) (ConsolPublished, error) {
	invalid := func(msg string) error { return &ReviewRefusal{Kind: ReviewInvalid, Msg: msg} }
	switch {
	case !reviewIDRe.MatchString(proposalID):
		return ConsolPublished{}, invalid("proposal id is malformed")
	case it.Op != "patch" && it.Op != "create":
		return ConsolPublished{}, invalid("op must be patch or create")
	case it.Scope != agentMemScopeProject && it.Scope != agentMemScopeUser:
		return ConsolPublished{}, invalid("scope must be project or user")
	case !agentMemValidName(it.Name):
		return ConsolPublished{}, invalid("name is not a valid memory name")
	case it.Scope == agentMemScopeProject && !agentMemValidProjectID(it.ProjectID):
		return ConsolPublished{}, invalid("project is not a project id")
	}
	if commit, err := trailerCommitBy(consolProposalTrailer, proposalID, ConsolidatorAuthorKind); err != nil {
		return ConsolPublished{}, err
	} else if commit != "" {
		return ConsolPublished{Commit: commit, Already: true}, nil
	}
	c, err := consolCaller(it.Scope, it.ProjectID, proposalID)
	if err != nil {
		return ConsolPublished{}, err
	}
	var res agentMemWriteResult
	switch it.Op {
	case "patch":
		edits := make([]agentMemPatchEdit, len(it.Edits))
		for i := range it.Edits {
			edits[i] = agentMemPatchEdit{Old: it.Edits[i].Old, New: &it.Edits[i].New}
		}
		res, err = agentMemPatch(c, agentMemPatchReq{Session: c.Session, Scope: it.Scope, Name: it.Name,
			Revision: it.Revision, Edits: edits}, now)
	default:
		c.noRevival = true
		res, err = agentMemSave(c, agentMemSaveReq{Session: c.Session, Scope: it.Scope, Name: it.Name,
			Description: it.Description, Type: it.Type, Body: it.Body, Revision: 0}, now)
	}
	if err != nil {
		return ConsolPublished{}, reviewRefusal(err)
	}
	return ConsolPublished{Commit: res.Commit, Revision: res.Revision}, nil
}

// consolCaller is the store caller of an approved proposal: kind consolidator, a fixed author
// session, and the project the proposal names (read from the store: the consolidator has no
// session whose working copy could describe it).
func consolCaller(scope, projectID, proposalID string) (agentMemCaller, error) {
	c := agentMemCaller{Session: consolSession, Kind: ConsolidatorAuthorKind,
		trailers: []string{consolProposalTrailer + ": " + proposalID}}
	if scope != agentMemScopeProject {
		return c, nil
	}
	if _, err := agentMemCheckDir("projects/"+projectID, false); err != nil {
		if os.IsNotExist(err) {
			return c, &ReviewRefusal{Kind: ReviewInvalid, Msg: "the proposal's project is not known to this workspace any more"}
		}
		return c, err
	}
	c.Project = agentMemProjectInfo(projectID)
	return c, nil
}

// ConsolCommitOf is the commit that published the proposal item with this id, "" when none did.
// It is how an approval that was cut off half-way finds what it already wrote.
func ConsolCommitOf(publishID string) (string, error) {
	if !reviewIDRe.MatchString(publishID) {
		return "", nil
	}
	return trailerCommitBy(consolProposalTrailer, publishID, ConsolidatorAuthorKind)
}
