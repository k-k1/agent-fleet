package memoryx

// AF-owned agent memory for a caller outside Agent Fleet (#1988): the member's own agent on
// another machine, reaching the store through the Control Plane's /mcp. Such a caller has no
// session, so it names the project itself, and its writes are recorded with author kind
// "external". The routes are gated by the externalMemory setting (off / read / write), which is
// separate from the in-workspace agentMemory switch: turning one on says nothing about the other.

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// The values of the externalMemory setting. Anything else reads as off.
const (
	ExternalMemoryOff   = "off"
	ExternalMemoryRead  = "read"
	ExternalMemoryWrite = "write"
)

// errCodeMemoryExternalOff is the refusal while the setting does not allow the call. The CP
// relays the message to the external client, so it names the Console setting to change.
const errCodeMemoryExternalOff = "memory_external_disabled"

// ExternalMemoryMode is the member's externalMemory setting, wired by uiprefs. A nil hook reads
// as off.
var ExternalMemoryMode func() string

// agentMemExternalKind is the author kind recorded for a write that came from outside.
const agentMemExternalKind = "external"

// agentMemExternalClientRe is what a client name may contain when it is recorded. The name is
// self-declared by the MCP client, so it is information and never an identity: it is cut to a
// short, plain alphabet before it reaches a commit trailer.
var agentMemExternalClientRe = regexp.MustCompile(`[^A-Za-z0-9 ._/@+-]`)

func agentMemExternalClient(s string) string {
	s = strings.TrimSpace(agentMemExternalClientRe.ReplaceAllString(s, ""))
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.TrimSpace(s)
}

// agentMemExternalAllow answers 403 and returns false unless the setting admits the call.
func agentMemExternalAllow(w http.ResponseWriter, write bool) bool {
	mode := ExternalMemoryOff
	if ExternalMemoryMode != nil {
		mode = ExternalMemoryMode()
	}
	ok := mode == ExternalMemoryWrite || (!write && mode == ExternalMemoryRead)
	if ok {
		return true
	}
	msg := "AF memory is not available to agents outside Agent Fleet: the member has to turn it on in Settings > Agent memory"
	if write && mode == ExternalMemoryRead {
		msg = "AF memory is read-only for agents outside Agent Fleet: the member has to allow writing in Settings > Agent memory"
	}
	httpx.WriteErr(w, http.StatusForbidden, errCodeMemoryExternalOff, msg)
	return false
}

// agentMemExternalCaller is the outside caller for one project. An empty project is the user
// scope only. A project must already hold memory, as for the Console: there is no working copy
// to take a new project's root from.
func agentMemExternalCaller(project string) (agentMemCaller, error) {
	c := agentMemCaller{Session: agentMemUnknown, Kind: agentMemExternalKind}
	if project == "" {
		return c, nil
	}
	m, err := agentMemMemberCaller(agentMemScopeProject, project)
	if err != nil {
		return c, err
	}
	c.Project = m.Project
	return c, nil
}

// agentMemExternalResolve names the project a call is about. `repository` is a git remote URL
// (what `git remote get-url origin` prints) and is matched, after the same normalisation the
// projects' Remote went through, against the projects that hold memory, so an outside model needs
// no opaque id. `project` stays accepted; when both are given they must name the same project.
// A remote that matches no project, or more than one, is refused: guessing here is how a fact
// lands in another repository's memory. Both empty answers "" (the user scope).
func agentMemExternalResolve(project, repository string) (string, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return project, nil
	}
	want := agentMemNormalizeRemote(repository)
	if want == "" {
		return "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
			"repository %q is not a remote this memory can match: pass the https or ssh URL printed by `git remote get-url origin` (ssh forms are matched for github.com, gitlab.com, bitbucket.org and codeberg.org only; elsewhere use the https URL), or pass `project` from memory_projects", repository)
	}
	all, err := agentMemMemberProjects()
	if err != nil {
		return "", err
	}
	var ids []string
	for _, p := range all.Projects {
		if p.Count > 0 && p.Project.Remote == want {
			ids = append(ids, p.Project.ID)
		}
	}
	switch {
	case len(ids) == 0:
		return "", memoryErrf(http.StatusNotFound, errCodeMemoryNotFound,
			"no project of this memory has the remote %q: check memory_projects (its `repository` column), or save from inside the workspace first, since a project must already hold memory", want)
	case len(ids) > 1:
		return "", memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"the remote %q matches %d projects (%s): pass `project` from memory_projects to choose one", want, len(ids), strings.Join(ids, ", "))
	}
	if project != "" && project != ids[0] {
		return "", memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
			"`project` (%s) and `repository` (%s) name different projects: pass only one of them", project, ids[0])
	}
	return ids[0], nil
}

// agentMemExternalCallerFor resolves project/repository and returns the caller for it.
func agentMemExternalCallerFor(project, repository string) (agentMemCaller, error) {
	id, err := agentMemExternalResolve(project, repository)
	if err != nil {
		return agentMemCaller{}, err
	}
	return agentMemExternalCaller(id)
}

// agentMemExternalWriteScope refuses a write that does not say where it goes. An omitted scope
// used to fall to the user scope, which published a repository-specific fact to every project
// without an error.
func agentMemExternalWriteScope(scope, project, repository string) error {
	switch scope {
	case agentMemScopeUser:
		return nil
	case agentMemScopeProject:
		if project == "" && strings.TrimSpace(repository) == "" {
			return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
				"scope \"project\" needs `repository` (the output of `git remote get-url origin` in your repository) or `project` (an id from memory_projects)")
		}
		return nil
	case "":
		return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
			"scope is required: pass \"project\" together with `repository` (the output of `git remote get-url origin` in your repository) for a fact about that repository, or \"user\" for a fact that holds in every project")
	}
	return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "scope must be %q or %q", agentMemScopeUser, agentMemScopeProject)
}

// agentMemExternalWriteResult is a write's answer plus the project it landed in, which the CP
// records as the audit target (it cannot resolve a repository itself).
type agentMemExternalWriteResult struct {
	agentMemWriteResult
	Project string `json:"project,omitempty"`
}

func agentMemExternalWrote(res agentMemWriteResult, c agentMemCaller) agentMemExternalWriteResult {
	out := agentMemExternalWriteResult{agentMemWriteResult: res}
	if res.Scope == agentMemScopeProject {
		out.Project = c.Project.ID
	}
	return out
}

// agentMemExternalProjectRow is a project as an outside client sees it. The working copy's path
// stays in the workspace.
type agentMemExternalProjectRow struct {
	ID      string `json:"id"`
	Display string `json:"display"`
	Remote  string `json:"remote,omitempty"`
	Count   int    `json:"count"`
}

type agentMemExternalProjectsWire struct {
	Projects []agentMemExternalProjectRow `json:"projects"`
}

// HandleAgentMemoryExternalProjects lists the projects that hold memory.
func HandleAgentMemoryExternalProjects(w http.ResponseWriter, r *http.Request) {
	if !agentMemExternalAllow(w, false) {
		return
	}
	all, err := agentMemMemberProjects()
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	rows := []agentMemExternalProjectRow{}
	for _, p := range all.Projects {
		if p.Count == 0 {
			continue
		}
		rows = append(rows, agentMemExternalProjectRow{ID: p.Project.ID, Display: p.Project.Display, Remote: p.Project.Remote, Count: p.Count})
	}
	httpx.WriteJSON(w, http.StatusOK, agentMemExternalProjectsWire{Projects: rows})
}

// HandleAgentMemoryExternalIndex lists the memories of the user scope and, when named, a project.
func HandleAgentMemoryExternalIndex(w http.ResponseWriter, r *http.Request) {
	if !agentMemExternalAllow(w, false) {
		return
	}
	q := r.URL.Query()
	c, err := agentMemExternalCallerFor(q.Get("project"), q.Get("repository"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	budget, _ := strconv.Atoi(q.Get("budget"))
	out, err := agentMemListIndex(c, budget)
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// HandleAgentMemoryExternalSearch finds memories by text.
func HandleAgentMemoryExternalSearch(w http.ResponseWriter, r *http.Request) {
	if !agentMemExternalAllow(w, false) {
		return
	}
	q := r.URL.Query()
	c, err := agentMemExternalCallerFor(q.Get("project"), q.Get("repository"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	hits, fallback, err := agentMemSearchRanked(c, q.Get("q"), limit)
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, agentMemSearchWire{Project: c.Project, Hits: hits, Fallback: fallback})
}

// HandleAgentMemoryExternalRead returns one memory with its body.
func HandleAgentMemoryExternalRead(w http.ResponseWriter, r *http.Request) {
	if !agentMemExternalAllow(w, false) {
		return
	}
	q := r.URL.Query()
	c, err := agentMemExternalCallerFor(q.Get("project"), q.Get("repository"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	e, err := agentMemRead(c, q.Get("scope"), q.Get("name"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

type agentMemExternalSaveReq struct {
	agentMemSaveReq
	Project    string `json:"project"`
	Repository string `json:"repository"`
	Client     string `json:"client"`
}

type agentMemExternalForgetReq struct {
	agentMemForgetReq
	Project    string `json:"project"`
	Repository string `json:"repository"`
	Client     string `json:"client"`
}

// agentMemExternalTrailers records the client's self-declared name next to the change.
func agentMemExternalTrailers(client string) []string {
	if c := agentMemExternalClient(client); c != "" {
		return []string{"AF-External-Client: " + c}
	}
	return nil
}

// HandleAgentMemoryExternalSave creates or updates a memory through the tools' own save: the
// same revision check, secret scan and limits.
func HandleAgentMemoryExternalSave(w http.ResponseWriter, r *http.Request) {
	if !agentMemExternalAllow(w, true) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, agentMemSaveMaxRequest)
	var req agentMemExternalSaveReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if err := agentMemExternalWriteScope(req.Scope, req.Project, req.Repository); err != nil {
		agentMemWriteErr(w, err)
		return
	}
	c, err := agentMemExternalCallerFor(req.Project, req.Repository)
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	req.Session = ""
	c.trailers = agentMemExternalTrailers(req.Client)
	res, err := agentMemSave(c, req.agentMemSaveReq, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, agentMemExternalWrote(res, c))
}

// HandleAgentMemoryExternalForget removes a memory from what is published.
func HandleAgentMemoryExternalForget(w http.ResponseWriter, r *http.Request) {
	if !agentMemExternalAllow(w, true) {
		return
	}
	var req agentMemExternalForgetReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if err := agentMemExternalWriteScope(req.Scope, req.Project, req.Repository); err != nil {
		agentMemWriteErr(w, err)
		return
	}
	c, err := agentMemExternalCallerFor(req.Project, req.Repository)
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	req.Session = ""
	c.trailers = agentMemExternalTrailers(req.Client)
	res, err := agentMemForget(c, req.agentMemForgetReq, time.Now())
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, agentMemExternalWrote(res, c))
}
