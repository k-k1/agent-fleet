package memoryx

// AF-owned agent memory for a caller outside Agent Fleet (#1988): the member's own agent on
// another machine, reaching the store through the Control Plane's /mcp. Such a caller has no
// session, so it names the project itself, and its writes are recorded with author kind
// "external". The routes are gated by the externalMemory setting (off / read / write), which is
// separate from the in-workspace agentMemory switch: turning one on says nothing about the other.

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// agentMemExternalTarget is the project a call is about: its id, and the working copy's identity
// when the project holds no memory yet but a working copy under ~/repos stands for it (Live).
type agentMemExternalTarget struct {
	ID   string
	Live *agentMemProject
}

// agentMemExternalLive finds the working copy that stands for a project id that holds no memory.
// complete=false (the search ran out of budget or time) says a clone could not be ruled out, so the
// caller must not treat a nil answer as "not cloned".
func agentMemExternalLive(id string) (p *agentMemProject, complete bool) {
	live, complete := agentMemLiveProjects()
	return live[id], complete
}

// agentMemExternalCaller is the outside caller for one project. An empty project is the user
// scope only. A project that holds no memory is only known through its working copy under
// ~/repos, which is where its root comes from; with none the project does not exist. start makes
// a save write the project's record with its first memory, as a session's first save does.
func agentMemExternalCaller(t agentMemExternalTarget, start bool) (agentMemCaller, error) {
	c := agentMemCaller{Session: agentMemUnknown, Kind: agentMemExternalKind}
	if t.ID == "" {
		return c, nil
	}
	m, err := agentMemMemberCaller(agentMemScopeProject, t.ID)
	if err == nil {
		c.Project = m.Project
		if !start {
			return c, nil
		}
		// A directory without a record is what a save that failed after creating it leaves behind
		// (a stale revision, a scan failure): the retry still starts the project from its working
		// copy, so the record lands with the first memory that is actually written.
		live := t.Live
		if live == nil {
			if _, err := os.Stat(filepath.Join(agentMemDir(), "projects", t.ID, "project.json")); err == nil || !os.IsNotExist(err) {
				return c, nil
			}
			var complete bool
			if live, complete = agentMemExternalLive(t.ID); live == nil {
				return c, agentMemExternalNoIdentity(complete)
			}
		}
		if live.ID == t.ID {
			c.Project, c.startProject = live, true
		}
		return c, nil
	}
	var ue *memoryUserErr
	if !errors.As(err, &ue) || ue.Code != errCodeMemoryNotFound {
		return c, err
	}
	live := t.Live
	if live == nil {
		var complete bool
		if live, complete = agentMemExternalLive(t.ID); live == nil && !complete && start {
			return c, agentMemExternalNoIdentity(false)
		}
	}
	if live == nil || live.ID != t.ID {
		return c, err
	}
	c.Project, c.startProject = live, start
	return c, nil
}

// agentMemExternalNoIdentity refuses a save into a project that has no record and whose working
// copy cannot be found: without the identity the record cannot be written with the first memory.
func agentMemExternalNoIdentity(complete bool) error {
	if !complete {
		return memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"the working copies under ~/repos could not all be read, so this project cannot be started safely: try again")
	}
	return memoryErrf(http.StatusNotFound, errCodeMemoryNotFound,
		"this project holds no memory record and its working copy is no longer cloned in the workspace: clone the repository again first")
}

// agentMemExternalResolve names the project a call is about. `repository` is a git remote URL
// (what `git remote get-url origin` prints) and is matched, after the same normalisation the
// projects' Remote went through, against the projects that hold memory, so an outside model needs
// no opaque id. When none holds memory, the working copies under ~/repos are matched the same
// way, and exactly one is the project to start. `project` stays accepted; when both are given
// they must name the same project. A remote that matches no project, or more than one, is
// refused: guessing here is how a fact lands in another repository's memory. Both empty answers
// the user scope.
func agentMemExternalResolve(project, repository string) (agentMemExternalTarget, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return agentMemExternalTarget{ID: project}, nil
	}
	want := agentMemNormalizeRemote(repository)
	if want == "" {
		want = agentMemNormalizedRemoteForm(repository)
	}
	if want == "" {
		return agentMemExternalTarget{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
			"repository %q is not a remote this memory can match: pass the https or ssh URL printed by `git remote get-url origin`, or a `remote` value from memory_projects (only github.com, gitlab.com, bitbucket.org and codeberg.org remotes are matched), or pass `project` from memory_projects", repository)
	}
	// The normaliser drops the port, so on a self-hosted host "forge:8443/x" and "forge:9443/x" (or
	// a port-less one) are one string yet can be different services. Only the big forges, whose
	// host names one service, are trusted to pick a write target; elsewhere the id is the answer.
	if host, _, _ := strings.Cut(want, "/"); !forgeHosts[host] {
		return agentMemExternalTarget{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
			"repository on %q cannot be matched safely (the remote does not tell self-hosted services on different ports apart): pass `project` from memory_projects instead", host)
	}
	all, err := agentMemMemberProjects()
	if err != nil {
		return agentMemExternalTarget{}, err
	}
	var ids []string
	for _, p := range all.Projects {
		if p.Count > 0 && p.Project.Remote == want {
			ids = append(ids, p.Project.ID)
		}
	}
	t := agentMemExternalTarget{}
	switch {
	case len(ids) > 1:
		return t, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"the remote %q matches %d projects (%s): pass `project` from memory_projects to choose one", want, len(ids), strings.Join(ids, ", "))
	case len(ids) == 1:
		t.ID = ids[0]
	default:
		// No project holds memory for it: a working copy that is cloned in the workspace starts one.
		live, complete := agentMemLiveProjects()
		var found []string
		for id, p := range live {
			if p.Remote == want {
				found = append(found, id)
			}
		}
		sort.Strings(found)
		switch {
		case len(found) == 0:
			return t, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound,
				"no project of this memory has the remote %q and no working copy of it is cloned in the workspace: clone the repository under ~/repos in the workspace first, or check memory_projects (its `repository` column)", want)
		case !complete:
			return t, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
				"the working copies under ~/repos could not all be read, so the project for the remote %q cannot be chosen safely: try again, or pass `project` from memory_projects", want)
		case len(found) > 1:
			return t, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
				"the remote %q matches %d working copies in the workspace (%s): pass `project` from memory_projects to choose one", want, len(found), strings.Join(found, ", "))
		}
		t.ID, t.Live = found[0], live[found[0]]
	}
	if project != "" && project != t.ID {
		return agentMemExternalTarget{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
			"`project` (%s) and `repository` (%s) name different projects: pass only one of them", project, t.ID)
	}
	return t, nil
}

// agentMemNormalizedRemoteForm accepts a repository that is already in the normalised form
// "host/owner/repo", which is what memory_projects lists as a project's `remote`; the URL
// normaliser answers "" for it because it has no scheme. Only a forge host is taken, with at least
// owner and repo after it, so a bare word, a local path ("./x/y", "foo/bar") and a self-hosted
// host stay refused; userinfo, a port, a query or a trailing slash fail agentMemRemoteOK. The host
// is lower-cased as the normaliser does and the path keeps its case. "" means not this form.
func agentMemNormalizedRemoteForm(raw string) string {
	host, path, ok := strings.Cut(raw, "/")
	host = strings.ToLower(host)
	if !ok || !forgeHosts[host] || strings.Count(path, "/") < 1 {
		return ""
	}
	out := host + "/" + path
	if !agentMemRemoteOK(out) {
		return ""
	}
	return out
}

// agentMemExternalCallerFor resolves project/repository and returns the caller for it. start is
// true for a save, the only call that may bring a project into being.
func agentMemExternalCallerFor(project, repository string, start bool) (agentMemCaller, error) {
	t, err := agentMemExternalResolve(project, repository)
	if err != nil {
		return agentMemCaller{}, err
	}
	return agentMemExternalCaller(t, start)
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

// HandleAgentMemoryExternalProjects lists the projects that hold memory and the working copies
// under ~/repos that could start one (count 0): an ambiguous `repository` is resolved by passing
// one of these ids as `project`, so they have to be listable.
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
		if p.Count == 0 && !p.Live {
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
	c, err := agentMemExternalCallerFor(q.Get("project"), q.Get("repository"), false)
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
	c, err := agentMemExternalCallerFor(q.Get("project"), q.Get("repository"), false)
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
	c, err := agentMemExternalCallerFor(q.Get("project"), q.Get("repository"), false)
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
	c, err := agentMemExternalCallerFor(req.Project, req.Repository, true)
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
	c, err := agentMemExternalCallerFor(req.Project, req.Repository, false)
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
