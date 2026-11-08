package memoryx

// The member's own routes into AF-owned agent memory: read one body (#1985) and save one by hand
// (#1997). The tools' routes need a calling session and the agentMemory switch; the Console has
// neither, so these name the scope and the project id directly and are not gated by the switch,
// like the changes list. The read writes nothing and counts no use. The save writes free text as
// the member, so it answers only a request carrying the CP's member-origin mark (ADR 0113): with
// AGENT_TOKEN alone a session could otherwise write as the member while the switch is off.

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
)

// MemberWriteMaxRequest bounds the body memberorigin.Require buffers on the member write routes:
// the largest of them is a save.
const MemberWriteMaxRequest = agentMemSaveMaxRequest

// HistoryApplyMaxRequest bounds the body memberorigin.Require buffers on import/apply. The body
// names every selected slug, and a bundle may carry thousands of 255-byte ones, so the save limit
// is too small; this is the CP's own cap on a body it will sign (memberOriginMaxBody), above which
// nothing reaches the Agent anyway.
const HistoryApplyMaxRequest = 2 << 20

// agentMemMemberCaller is the Console as a caller for one scope. A project must already hold
// memory: the Console cannot start a project, because it has no working copy to take the
// project's root from.
func agentMemMemberCaller(scope, project string) (agentMemCaller, error) {
	c := agentMemMember
	switch scope {
	case agentMemScopeUser:
		return c, nil
	case agentMemScopeProject:
		if !agentMemValidProjectID(project) {
			return c, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "project is not a project id")
		}
		if _, err := agentMemCheckDir("projects/"+project, false); err != nil {
			if os.IsNotExist(err) {
				return c, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no such project")
			}
			return c, err
		}
		c.Project = agentMemProjectInfo(project)
		return c, nil
	}
	return c, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "scope must be %q or %q", agentMemScopeUser, agentMemScopeProject)
}

// agentMemMemberRead is one memory with its body for the member. Unlike the tools' read it is
// not a use: the count says what sessions found worth reading.
func agentMemMemberRead(scope, project, name string) (agentMemEntry, error) {
	if err := agentMemCheckName(name); err != nil {
		return agentMemEntry{}, err
	}
	c, err := agentMemMemberCaller(scope, project)
	if err != nil {
		return agentMemEntry{}, err
	}
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	l, ok, err := agentMemLoadOne(c, scope, name)
	if err != nil {
		return agentMemEntry{}, err
	}
	if !ok {
		return agentMemEntry{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no memory by that name")
	}
	if l.Withheld {
		return agentMemEntry{}, memoryErrf(http.StatusUnprocessableEntity, errCodeMemorySecretDetected,
			"this memory is withheld: its file looks like it contains a secret or cannot be checked; fix or remove the file")
	}
	rel, _ := agentMemScopeDir(scope, c)
	es := []agentMemEntry{l.Entry}
	agentMemSetUses(es, rel)
	return es[0], nil
}

// HandleAgentMemoryMemberRead returns one memory with its body to the Console.
func HandleAgentMemoryMemberRead(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	e, err := agentMemMemberRead(q.Get("scope"), q.Get("project"), q.Get("name"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

// agentMemMemberHit is a search hit with the project it belongs to, since the Console searches
// every scope at once.
type agentMemMemberHit struct {
	agentMemHit
	Project *agentMemProject `json:"project,omitempty"`
}

// agentMemMemberSearchOut is the Console's search answer.
type agentMemMemberSearchOut struct {
	Hits     []agentMemMemberHit `json:"hits"`
	Fallback string              `json:"fallback,omitempty"`
}

// agentMemMemberSearch ranks the memories of the user scope and every project (or the one scope
// named) with the ranking of the tools' search, over names, descriptions and bodies. It counts no
// use and is not gated by the switch. A memory whose file fails the scan is not loaded at all, so
// neither its name nor its text can reach a hit. The kind filter is off: the Console lists every
// memory, whatever kinds it is meant for.
func agentMemMemberSearch(scope, project, query string, limit int) (agentMemMemberSearchOut, error) {
	out := agentMemMemberSearchOut{Hits: []agentMemMemberHit{}}
	terms := agentMemSearchTerms(query)
	if len(terms) == 0 {
		return out, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "query is required")
	}
	var srcs []agentMemSearchSrc
	add := func(scope, project string) error {
		c, err := agentMemMemberCaller(scope, project)
		if err == nil {
			srcs = append(srcs, agentMemSearchSrc{c: c, scope: scope})
		}
		return err
	}
	if scope != "" {
		if err := add(scope, project); err != nil {
			return out, err
		}
	} else {
		if err := add(agentMemScopeUser, ""); err != nil {
			return out, err
		}
		if dir, err := agentMemCheckDir("projects", false); err == nil {
			ents, _ := os.ReadDir(dir)
			for _, d := range ents {
				if d.IsDir() && agentMemValidProjectID(d.Name()) {
					if err := add(agentMemScopeProject, d.Name()); err != nil {
						return out, err
					}
				}
			}
		}
	}
	// The cold scan of every searched scope runs before the read lock, as the tools' search does,
	// so a writer queued behind this read is not held up by it; the ranking below re-reads under
	// the lock and still goes through the scan.
	agentMemWarmSrcs(srcs)
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	found, fallback, err := agentMemRankLocked(srcs, agentMemUnknown, terms, limit, false)
	if err != nil {
		return out, err
	}
	out.Fallback = fallback
	for _, s := range found {
		out.Hits = append(out.Hits, agentMemMemberHit{agentMemHit: agentMemHitOf(s, fallback), Project: s.src.c.Project})
	}
	return out, nil
}

// HandleAgentMemoryMemberSearch is the Console's body search. Read-only.
func HandleAgentMemoryMemberSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := agentMemMemberSearch(q.Get("scope"), q.Get("project"), q.Get("q"), limit)
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// agentMemMemberLatest is the newest published change of one memory, with the same Live and
// Revertible marks the changes list gives its newest row. The list is a window of recent
// changes; this finds the change for a memory outside it, so a forget or restore does not depend
// on the window. It resolves nothing that agentMemRevert does not check again: a revert still
// has to name this commit and pass its own revision check.
func agentMemMemberLatest(scope, project, name string) (agentMemChangeView, error) {
	if err := agentMemCheckName(name); err != nil {
		return agentMemChangeView{}, err
	}
	var rel string
	switch scope {
	case agentMemScopeUser:
		rel = "user"
	case agentMemScopeProject:
		if !agentMemValidProjectID(project) {
			return agentMemChangeView{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "project is not a project id")
		}
		rel = "projects/" + project
	default:
		return agentMemChangeView{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "scope must be %q or %q", agentMemScopeUser, agentMemScopeProject)
	}
	rel += "/" + name + ".md"
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	return agentMemMemberLatestLocked(scope, name, rel)
}

// agentMemMemberLatestLocked is agentMemMemberLatest for a validated scope and name, with
// agentMemMu already held (the forgotten list resolves many memories under one read lock).
func agentMemMemberLatestLocked(scope, name, rel string) (agentMemChangeView, error) {
	notFound := memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no published change for that memory")
	if !memoryHasCommits() {
		return agentMemChangeView{}, notFound
	}
	commit, err := agentMemLatestChange(rel)
	if err != nil {
		return agentMemChangeView{}, err
	}
	if commit == "" {
		return agentMemChangeView{}, notFound
	}
	return agentMemMemberChangeAt(scope, name, rel, commit)
}

// agentMemMemberChangeAt judges and marks the newest change `commit` of one memory (agentMemMu
// held). The forgotten list finds the commits of many memories in one history pass and comes here.
func agentMemMemberChangeAt(scope, name, rel, commit string) (agentMemChangeView, error) {
	at, err := memoryGitRun("log", "-1", "--format=%aI", commit)
	if err != nil {
		return agentMemChangeView{}, err
	}
	body, err := memoryGitRun("log", "-1", "--format=%B", commit)
	if err != nil {
		return agentMemChangeView{}, err
	}
	v, rel2, pid, ok := agentMemChangeFrom(commit, strings.TrimSpace(at), body)
	if !ok {
		return agentMemChangeView{}, memoryErrf(http.StatusUnprocessableEntity, errCodeMemorySecretDetected,
			"the newest change of this memory is withheld: a value in it failed the scan or the expected form")
	}
	// The trailers say nothing about the text. A live file that fails the scan (or cannot be read)
	// is withheld like the list does, and so is a forgotten memory whose earlier text would: the
	// answer then names nothing.
	withheld := memoryErrf(http.StatusUnprocessableEntity, errCodeMemorySecretDetected,
		"this memory is withheld: its text looks like it contains a secret or cannot be checked")
	exists := false
	if dir, derr := agentMemCheckDir(filepath.ToSlash(filepath.Dir(rel2)), false); derr == nil {
		l, ok, lerr := agentMemLoadFile(filepath.Join(dir, name+".md"), scope, name)
		if lerr != nil || l.Withheld {
			return agentMemChangeView{}, withheld
		}
		exists = ok
	} else if !os.IsNotExist(derr) {
		return agentMemChangeView{}, withheld
	}
	if !exists {
		// Judged as a reader would judge the file (agentMemClean: raw bytes and decoded values),
		// and closed on anything that cannot be read or parsed: the earlier text may have entered
		// the history by import or a hand edit, which the save path's scan never saw.
		before, beforeOK, after, afterOK, serr := agentMemSides(commit, rel2)
		if serr != nil {
			return agentMemChangeView{}, withheld
		}
		// Both sides: with the live file gone, the newest change may still have left text behind
		// (a create or update whose file was removed by hand).
		for _, side := range []struct {
			data []byte
			ok   bool
		}{{before, beforeOK}, {after, afterOK}} {
			if !side.ok {
				continue
			}
			e, pok := agentMemParse(side.data)
			if !pok {
				return agentMemChangeView{}, withheld
			}
			e.Name, e.Scope = name, scope
			if !agentMemClean(side.data, e) {
				return agentMemChangeView{}, withheld
			}
		}
	}
	if pid != "" {
		v.Project = agentMemProjectInfo(pid)
	}
	agentMemMarkLatest(&v, filepath.ToSlash(rel))
	return v, nil
}

// HandleAgentMemoryMemberLatest returns the newest change of one memory to the Console.
func HandleAgentMemoryMemberLatest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := agentMemMemberLatest(q.Get("scope"), q.Get("project"), q.Get("name"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

// agentMemMemberSaveReq is agentMemSaveReq plus the project id the Console has no session to
// resolve. The session field is ignored: the author is the member.
type agentMemMemberSaveReq struct {
	agentMemSaveReq
	Project string `json:"project"`
}

// HandleAgentMemoryMemberSave creates or updates a memory as the member, through the tools' own
// save: the same revision check, secret scan and limits.
func HandleAgentMemoryMemberSave(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, agentMemSaveMaxRequest)
	var req agentMemMemberSaveReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	c, err := agentMemMemberCaller(req.Scope, req.Project)
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	req.Session = ""
	res, err := agentMemSave(c, req.agentMemSaveReq, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
