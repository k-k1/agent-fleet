package memoryx

// Usage counts and pins for the budgeted memory_index (#1703, ADR 0108 decision 5).
//
// A use is one memory_read, or one search hit that was returned. It is counted in a sidecar
// file, `<scope dir>/.usage/<name>`, one byte appended per use: the count is the file size.
// O_APPEND makes each write land atomically at the end, so concurrent readers (several sessions,
// several agent processes) never lose a use and nothing reads before it writes. A counter kept in
// the memory file would be a read-modify-write, and it would also be a commit per read. The
// sidecar is runtime state, not published: it has no history and does not travel with an
// export, so a restored store starts counting again.
//
// A pin is the opposite: the member's decision, published like any other change (`pinned: true`
// in the frontmatter, one commit, op "pin"), so history shows who pinned what. It changes neither
// the revision nor `updated`: an agent that read the memory before can still save it, and the
// save carries the pin forward.

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	agentMemUsageDir = ".usage"
	// agentMemUsageCap bounds a sidecar. Past it a use is not recorded: the ranking only needs
	// "used a lot", and a tampered file cannot grow without bound.
	agentMemUsageCap = 4096
)

// agentMemProjectIDRe is what agentMemProjectID can produce (and what the change list accepts).
// Dots are fine inside a name; only "." and ".." are not a single safe path element.
var agentMemProjectIDRe = regexp.MustCompile(`^[a-z0-9._-]{1,80}$`)

func agentMemValidProjectID(id string) bool {
	return agentMemProjectIDRe.MatchString(id) && id != "." && id != ".."
}

// agentMemRecordUse counts one use of a memory. Best effort: a use that cannot be recorded must
// never fail the read that caused it. The size check and the append are not one step, so
// concurrent uses can run a few bytes past the cap; the cap is a bound, not a quota.
func agentMemRecordUse(scopeRel, name string) {
	dir, err := agentMemCheckDir(scopeRel+"/"+agentMemUsageDir, true)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || st.Size() >= agentMemUsageCap {
		return
	}
	_, _ = f.Write([]byte{'.'})
}

// agentMemRecordUses counts a use for each entry, resolving each one's scope directory for the
// caller (a project entry is the caller's own project).
func agentMemRecordUses(c agentMemCaller, es ...agentMemEntry) {
	for _, e := range es {
		if rel, err := agentMemScopeDir(e.Scope, c); err == nil {
			agentMemRecordUse(rel, e.Name)
		}
	}
}

// agentMemClearUsage forgets a memory's count, so a memory created later under the same name
// does not inherit it.
func agentMemClearUsage(scopeRel, name string) {
	if dir, err := agentMemCheckDir(scopeRel+"/"+agentMemUsageDir, false); err == nil {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// agentMemLoadUsage is the use count of every memory in one scope directory, by name. Anything
// that is not a regular file counts for nothing.
func agentMemLoadUsage(scopeRel string) map[string]int {
	dir, err := agentMemCheckDir(scopeRel+"/"+agentMemUsageDir, false)
	if err != nil {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make(map[string]int, len(ents))
	for _, d := range ents {
		if !d.Type().IsRegular() || !agentMemValidName(d.Name()) {
			continue
		}
		if st, err := d.Info(); err == nil {
			out[d.Name()] = int(min(st.Size(), agentMemUsageCap))
		}
	}
	return out
}

// agentMemSetUses fills Uses on entries of one scope.
func agentMemSetUses(es []agentMemEntry, scopeRel string) {
	if len(es) == 0 {
		return
	}
	uses := agentMemLoadUsage(scopeRel)
	for i := range es {
		es[i].Uses = uses[es[i].Name]
	}
}

// agentMemListed is one row of the Console's list: no body, with the state the member acts on.
type agentMemListed struct {
	Name        string           `json:"name"`
	Scope       string           `json:"scope"`
	Project     *agentMemProject `json:"project,omitempty"`
	Description string           `json:"description"`
	Type        string           `json:"type,omitempty"`
	Kinds       []string         `json:"kinds,omitempty"`
	Updated     string           `json:"updated"`
	Pinned      bool             `json:"pinned"`
	Uses        int              `json:"uses"`
}

// agentMemListAllOut is every memory of every scope, for the Console. Withheld counts files
// left out because they failed the scan or cannot be read; only the count is sent.
type agentMemListAllOut struct {
	Entries  []agentMemListed `json:"entries"`
	Withheld int              `json:"withheld,omitempty"`
}

// agentMemListAll lists the user scope and every project's memories, pinned first, then by use.
// It does not count as a use of anything.
func agentMemListAll() (agentMemListAllOut, error) {
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	out := agentMemListAllOut{Entries: []agentMemListed{}}
	type scopeRef struct {
		scope, rel string
		project    *agentMemProject
	}
	refs := []scopeRef{{scope: agentMemScopeUser, rel: "user"}}
	if dir, err := agentMemCheckDir("projects", false); err == nil {
		if ents, err := os.ReadDir(dir); err == nil {
			for _, d := range ents {
				if d.IsDir() && agentMemValidProjectID(d.Name()) {
					refs = append(refs, scopeRef{agentMemScopeProject, "projects/" + d.Name(), agentMemProjectInfo(d.Name())})
				}
			}
		}
	}
	for _, r := range refs {
		es, withheld, err := agentMemLoadDir(r.scope, r.rel)
		if err != nil {
			return out, err
		}
		out.Withheld += withheld
		agentMemSetUses(es, r.rel)
		for _, e := range es {
			out.Entries = append(out.Entries, agentMemListed{
				Name: e.Name, Scope: e.Scope, Project: r.project, Description: e.Description,
				Type: e.Type, Kinds: e.Kinds, Updated: e.Updated, Pinned: e.Pinned, Uses: e.Uses,
			})
		}
	}
	sort.SliceStable(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		if a.Uses != b.Uses {
			return a.Uses > b.Uses
		}
		return a.Updated > b.Updated
	})
	return out, nil
}

// agentMemPinReq pins or unpins one memory. Project is the project id for scope "project"; the
// Console has no session to resolve it from.
type agentMemPinReq struct {
	Scope   string `json:"scope"`
	Project string `json:"project"`
	Name    string `json:"name"`
	Pinned  bool   `json:"pinned"`
}

// agentMemPin sets the member's pin on a memory and publishes it as a change by the member. Pinning
// what is already pinned changes nothing and commits nothing.
func agentMemPin(req agentMemPinReq, now time.Time) (agentMemWriteResult, error) {
	req.Name = strings.TrimSpace(req.Name)
	if err := agentMemCheckName(req.Name); err != nil {
		return agentMemWriteResult{}, err
	}
	var rel string
	switch req.Scope {
	case agentMemScopeUser:
		rel = "user"
	case agentMemScopeProject:
		if !agentMemValidProjectID(req.Project) {
			return agentMemWriteResult{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "project is not a project id")
		}
		rel = "projects/" + req.Project
	default:
		return agentMemWriteResult{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "scope must be %q or %q", agentMemScopeUser, agentMemScopeProject)
	}

	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	dir, err := agentMemCheckDir(rel, false)
	if err != nil {
		if os.IsNotExist(err) {
			return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no memory by that name")
		}
		return agentMemWriteResult{}, err
	}
	cur, exists, err := agentMemLoadFile(filepath.Join(dir, req.Name+".md"), req.Scope, req.Name)
	if err != nil {
		return agentMemWriteResult{}, err
	}
	if !exists {
		return agentMemWriteResult{}, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no memory by that name")
	}
	if cur.Withheld {
		return agentMemWriteResult{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "this memory is withheld; fix or remove the file first")
	}
	res := agentMemWriteResult{Name: req.Name, Scope: req.Scope, Revision: cur.Entry.Revision}
	if cur.Entry.Pinned == req.Pinned {
		return res, nil
	}
	e := cur.Entry
	e.Pinned = req.Pinned
	data := agentMemRender(e)
	if f := agentMemScanText("memory", string(data)); len(f) > 0 {
		return agentMemWriteResult{}, &agentMemSecretErr{Findings: f}
	}
	repoRel := rel + "/" + req.Name + ".md"
	res.Commit, err = agentMemApplyLocked([]agentMemChange{{Rel: repoRel, Data: data}}, "pin", repoRel, agentMemMember, now)
	return res, err
}
