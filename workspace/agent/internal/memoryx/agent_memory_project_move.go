package memoryx

// Moving or merging the memories of a project whose working copy is gone (#1984, ADR 0108
// decision 2 and open question 5). A project is keyed by its repository's absolute
// git-common-dir, so a repository renamed, moved or cloned again at another path is a new,
// empty project and the old one's memories can no longer be reached by any session.
//
// Three routes serve the Console: the list of projects (orphaned ones flagged, with counts), a
// read-only preview of one move, and the move itself. The move answers only a request carrying
// the CP's member-origin mark (ADR 0113) and has no MCP tool.
//
// A move is never a silent overwrite and never a half-move that loses text. Each memory is two
// published changes, both authored by the member and both visible in the changes list, so each
// can be undone there: a create in the target, then a forget in the source. The create comes
// first, so a failure between the two leaves the text in both projects; running the move again
// finds the identical copy in the target and finishes with the forget. A name already taken in
// the target is skipped or renamed, as the member chose, and a memory changed since the preview
// is left where it is.

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	agentMemMoveSkip   = "skip"
	agentMemMoveRename = "rename"

	// agentMemMoveBatch bounds the memories one apply request moves: each is two commits, so a
	// request has to stay short enough for the proxy in front of it. The Console sends batches.
	agentMemMoveBatch = 50
	// agentMemMoveRenameTries bounds the search for a free name.
	agentMemMoveRenameTries = 50
)

var (
	// agentMemRootProbeTimeout bounds the look at a project's root: a root on a hung mount
	// must read as "unknown", not stall the list.
	agentMemRootProbeTimeout = 3 * time.Second

	// agentMemMoveMu serializes moves, so two requests never interleave their per-memory steps.
	agentMemMoveMu sync.Mutex

	// agentMemMoveStep runs between the two changes of one memory; a test fails it to stop a
	// move half-way.
	agentMemMoveStep = func(step, name string) error { return nil }
)

// agentMemRootGone says that root is positively known not to exist. When in doubt it answers
// false: a project is called orphaned only on evidence, because the move is offered on that word.
// It is false for a root outside ~/repos (another mount's), an unreadable or empty ~/repos (not
// mounted yet), any symlink on the way (a /scratch link dangles after a stop), any error but
// "does not exist", and a probe that does not answer in time.
func agentMemRootGone(root string) bool {
	if root == "" || !filepath.IsAbs(root) {
		return false
	}
	repos := gitx.ReposRoot()
	rel, err := filepath.Rel(repos, filepath.Clean(root))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	done := make(chan bool, 1)
	go func() { done <- agentMemRootGoneProbe(repos, rel) }()
	select {
	case gone := <-done:
		return gone
	case <-time.After(agentMemRootProbeTimeout):
		return false
	}
}

func agentMemRootGoneProbe(repos, rel string) bool {
	st, err := os.Lstat(repos)
	if err != nil || !st.IsDir() {
		return false
	}
	ents, err := os.ReadDir(repos)
	if err != nil || len(ents) == 0 {
		return false
	}
	cur := repos
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, seg)
		st, err := os.Lstat(cur)
		switch {
		case err == nil && (st.Mode()&os.ModeSymlink != 0 || !st.IsDir()):
			return false
		case err == nil:
			continue
		case errors.Is(err, fs.ErrNotExist):
			return true
		default:
			return false
		}
	}
	return false
}

var (
	// agentMemLiveBudget bounds the directories one search reads, agentMemLiveTimeout its time.
	// Past either the answer is "incomplete" and nothing is called orphaned.
	agentMemLiveBudget  = 100000
	agentMemLiveTimeout = 20 * time.Second
	// agentMemNestedDepth: below a working copy's root the walk reads this many directory levels
	// beyond the root's own (1 = its subdirectories are read, their children only checked for a
	// marker).
	agentMemNestedDepth = 1
)

// agentMemLiveProjects maps the working copies to their projects, by id: every directory under
// ~/repos with a .git or .svn entry, found at any depth outside other working copies and behind
// symlinks included, and every session's working directory. A project with a live working copy
// is never orphaned, whatever its recorded root says. complete=false says something could not be
// ruled out: a directory that could not be read, a repository whose identity could not be
// resolved, or the budget or time of the search ran out.
//
// Inside a working copy the walk looks only agentMemNestedDepth directory levels down, for a
// worktree or clone placed below another one (repo/.worktrees/x, repo/build/live); a repository
// deeper than that is found only when it is also a session's directory. Walking whole
// node_modules trees timed out on a large workspace, and an empty answer from a timeout reads as
// "nothing is live" (#3045).
func agentMemLiveProjects() (map[string]*agentMemProject, bool) {
	type result struct {
		live     map[string]*agentMemProject
		complete bool
	}
	done := make(chan result, 1)
	go func() {
		l, c := agentMemLiveSearch()
		done <- result{l, c}
	}()
	select {
	case r := <-done:
		return r.live, r.complete
	case <-time.After(agentMemLiveTimeout):
		return map[string]*agentMemProject{}, false
	}
}

// agentMemLiveRunner is the git runner of the live search, replaceable by a test that counts calls.
var agentMemLiveRunner = func(dir string) agentMemGitRunner {
	return func(args ...string) (string, error) { return gitx.Run(dir, args...) }
}

func agentMemLiveSearch() (live map[string]*agentMemProject, complete bool) {
	live = map[string]*agentMemProject{}
	complete = true
	// resolved holds the directories whose identity was computed, by real path: a session's
	// directory the walk already found costs nothing.
	resolved := map[string]bool{}
	add := func(dir string) {
		real := dir
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			real = r
		}
		if resolved[real] {
			return
		}
		resolved[real] = true
		p, identified := agentMemProjectIdent(dir, dir, agentMemLiveRunner(dir))
		if p == nil {
			return
		}
		if !identified {
			// The id would be derived from the path, not the repository: it cannot be compared.
			complete = false
		}
		if live[p.ID] == nil {
			live[p.ID] = p
		}
	}
	// left is how many more levels may be read below dir: -1 outside any working copy
	// (unbounded, the budget limits it), counting down inside one.
	type item struct {
		dir  string
		left int
	}
	visited := map[string]bool{}
	queue := []item{{gitx.ReposRoot(), -1}}
	reads := 0
	for len(queue) > 0 {
		dir, left := queue[0].dir, queue[0].left
		queue = queue[1:]
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			if visited[real] {
				continue
			}
			visited[real] = true
		}
		if reads++; reads > agentMemLiveBudget {
			complete = false
			break
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			// Not knowing what is below is not knowing there is nothing below.
			complete = false
			continue
		}
		for _, e := range ents {
			if e.Name() == ".git" {
				continue // git's own directory holds no working copy; a .git entry is the marker below
			}
			d := filepath.Join(dir, e.Name())
			if e.Type()&fs.ModeSymlink != 0 {
				st, err := os.Stat(d)
				switch {
				case err != nil && !errors.Is(err, fs.ErrNotExist):
					complete = false
					continue
				case err != nil || !st.IsDir():
					continue // a dangling link or a file
				}
			} else if !e.IsDir() {
				continue
			}
			isCopy := false
			for _, marker := range []string{".git", ".svn"} {
				if _, err := os.Lstat(filepath.Join(d, marker)); err == nil {
					isCopy = true
					break
				} else if !errors.Is(err, fs.ErrNotExist) {
					complete = false
				}
			}
			switch {
			case isCopy:
				add(d)
				if left < 0 {
					// Its content is not walked whole: only agentMemNestedDepth levels.
					queue = append(queue, item{d, agentMemNestedDepth})
				} // a copy inside a copy is a leaf
			case left < 0:
				queue = append(queue, item{d, -1})
			case left > 0:
				queue = append(queue, item{d, left - 1})
			}
		}
	}
	for _, m := range session.ListMetas() {
		if m.Dir != "" {
			if _, err := os.Stat(m.Dir); err == nil {
				add(m.Dir)
			}
		}
	}
	return live, complete
}

// agentMemProjectRow is one project of the store as the Console lists it.
type agentMemProjectRow struct {
	Project *agentMemProject `json:"project"`
	// Count is the memories that can be read; Withheld the files left out because they failed
	// the scan or cannot be parsed (they are never moved).
	Count    int  `json:"count"`
	Withheld int  `json:"withheld,omitempty"`
	Orphaned bool `json:"orphaned"`
	// Live says a working copy under ~/repos belongs to the project: a move can target it even
	// before it holds a memory.
	Live bool `json:"live"`
	// MergeInto is the one live project whose normalised remote equals this orphan's: the move
	// the Console suggests (#2110). Never set when two live projects share the remote (two clones
	// of one repository: a guess between them would be wrong half the time), when the orphan has
	// no recorded remote, or for a project that is not orphaned. A suggestion only; nothing moves
	// unless the member confirms the usual preview.
	MergeInto *agentMemProject `json:"mergeInto,omitempty"`
}

type agentMemProjectsWire struct {
	Projects []agentMemProjectRow `json:"projects"`
}

// agentMemOrphaned judges one stored project from its record.
func agentMemOrphaned(p *agentMemProject, live map[string]*agentMemProject, complete bool) bool {
	return complete && live[p.ID] == nil && agentMemRootGone(p.Root)
}

// agentMemRemoteMatch is the live project an orphan's memories are suggested to move into: the
// only live project (other than the orphan) with the same non-empty normalised remote.
func agentMemRemoteMatch(orphan *agentMemProject, live map[string]*agentMemProject) *agentMemProject {
	if orphan.Remote == "" {
		return nil
	}
	var match *agentMemProject
	for id, p := range live {
		if id == orphan.ID || p.Remote != orphan.Remote {
			continue
		}
		if match != nil {
			return nil
		}
		match = p
	}
	return match
}

// agentMemBackfillRemotes adds the remote to the project.json of every project that has a working
// copy now and was recorded without one, in one commit (#2110). Best effort: an orphan whose root
// is gone never gets one, and any failure is logged and left for the next start. It runs after
// the working copies are found, so it also covers a project nobody has written to since the
// upgrade.
func agentMemBackfillRemotes(now time.Time) {
	live, _ := agentMemLiveProjects()
	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()
	ids := make([]string, 0, len(live))
	for id, p := range live {
		if p.Remote != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var changes []agentMemChange
	for _, id := range ids {
		if !agentMemValidProjectID(id) {
			continue
		}
		if _, err := agentMemCheckDir("projects/"+id, false); err != nil {
			continue
		}
		ch, err := agentMemProjectInfoChange(*live[id])
		if err != nil {
			continue
		}
		changes = append(changes, ch...)
	}
	if len(changes) == 0 {
		return
	}
	unknown := agentMemCaller{Session: agentMemUnknown, Kind: agentMemUnknown}
	if _, err := agentMemApplyLocked(changes, agentMemOpProjectInfo, "projects", unknown, now); err != nil {
		log.Printf("agent memory: record project remotes: %s", agentMemErrKind(err))
	}
}

// BackfillAgentMemoryRemotes is agentMemBackfillRemotes for the Agent's start.
func BackfillAgentMemoryRemotes() { agentMemBackfillRemotes(time.Now()) }

// agentMemMemberProjects lists every project that holds memory or has a working copy, orphaned
// ones flagged.
func agentMemMemberProjects() (agentMemProjectsWire, error) {
	out := agentMemProjectsWire{Projects: []agentMemProjectRow{}}
	live, complete := agentMemLiveProjects()
	seen := map[string]bool{}
	agentMemMu.RLock()
	if dir, err := agentMemCheckDir("projects", false); err == nil {
		ents, _ := os.ReadDir(dir)
		for _, d := range ents {
			id := d.Name()
			if !d.IsDir() || !agentMemValidProjectID(id) || !agentMemCleanText(id) {
				continue
			}
			es, withheld, err := agentMemLoadDir(agentMemScopeProject, "projects/"+id)
			if err != nil {
				agentMemMu.RUnlock()
				return out, err
			}
			p := agentMemProjectInfo(id)
			seen[id] = true
			out.Projects = append(out.Projects, agentMemProjectRow{
				Project: p, Count: len(es), Withheld: withheld, Live: live[id] != nil,
			})
		}
	}
	agentMemMu.RUnlock()
	for id, p := range live {
		if !seen[id] {
			out.Projects = append(out.Projects, agentMemProjectRow{Project: p, Live: true})
		}
	}
	for i := range out.Projects {
		r := &out.Projects[i]
		// A project with nothing to move is not offered as an orphan.
		r.Orphaned = (r.Count > 0 || r.Withheld > 0) && agentMemOrphaned(r.Project, live, complete)
		if r.Orphaned {
			r.MergeInto = agentMemRemoteMatch(r.Project, live)
		}
	}
	sort.Slice(out.Projects, func(i, j int) bool {
		a, b := out.Projects[i], out.Projects[j]
		if a.Orphaned != b.Orphaned {
			return a.Orphaned
		}
		if a.Project.Display != b.Project.Display {
			return a.Project.Display < b.Project.Display
		}
		return a.Project.ID < b.Project.ID
	})
	return out, nil
}

// HandleAgentMemoryMemberProjects lists the projects to the Console. Read-only.
func HandleAgentMemoryMemberProjects(w http.ResponseWriter, r *http.Request) {
	out, err := agentMemMemberProjects()
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// Statuses of one memory in a move plan.
const (
	agentMemMoveStatusMove      = "move"      // the name is free in the target
	agentMemMoveStatusSame      = "same"      // the target holds this very text: only the source copy goes
	agentMemMoveStatusCollision = "collision" // the name is taken; no policy chosen yet
	agentMemMoveStatusRename    = "rename"    // moves under newName
	agentMemMoveStatusSkip      = "skip"      // stays in the source
)

// agentMemMoveItem is one source memory in a plan.
type agentMemMoveItem struct {
	Name        string `json:"name"`
	NewName     string `json:"newName,omitempty"`
	Status      string `json:"status"`
	Description string `json:"description"`
	Type        string `json:"type,omitempty"`
	Revision    int    `json:"revision"`
}

// agentMemMovePreview is what a move would do, written to nothing.
type agentMemMovePreview struct {
	From  *agentMemProject   `json:"from"`
	To    *agentMemProject   `json:"to"`
	Items []agentMemMoveItem `json:"items"`
	// Counts is the number of items per status.
	Counts map[string]int `json:"counts"`
	// Withheld counts source files that are not moved: they failed the scan or cannot be parsed.
	Withheld int `json:"withheld,omitempty"`
	// Collision is the policy this plan was made under ("" = none chosen).
	Collision string `json:"collision,omitempty"`
	// NeedsPolicy says at least one name is taken in the target and no policy was chosen, so an
	// apply would be refused.
	NeedsPolicy bool `json:"needsPolicy,omitempty"`
}

// agentMemMovePlan is a preview with what the apply needs besides.
type agentMemMovePlan struct {
	agentMemMovePreview
	toLive bool // the target is known from a working copy, so its project.json may be written
}

func agentMemMoveBadReq(format string, args ...any) error {
	return memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, format, args...)
}

func agentMemMoveSame(a, b agentMemEntry) bool {
	// The pin and the import provenance are part of the memory: a copy that lacks them is not the
	// same, or removing the source would drop the member's pin or the import's source hash.
	if a.Description != b.Description || a.Type != b.Type || strings.TrimRight(a.Body, "\n") != strings.TrimRight(b.Body, "\n") ||
		a.Pinned != b.Pinned || a.Source != b.Source || a.SourceHash != b.SourceHash {
		return false
	}
	ka, kb := append([]string(nil), a.Kinds...), append([]string(nil), b.Kinds...)
	sort.Strings(ka)
	sort.Strings(kb)
	return strings.Join(ka, ",") == strings.Join(kb, ",")
}

func agentMemMoveRenamed(name string, n int) string {
	suffix := "-moved"
	if n > 1 {
		suffix += fmt.Sprintf("-%d", n)
	}
	if len(name)+len(suffix) > 64 {
		name = name[:64-len(suffix)]
	}
	return name + suffix
}

// agentMemMovePlanLocked builds the plan with agentMemMu held (read or write).
func agentMemMovePlanLocked(fromID, toID, policy string, live map[string]*agentMemProject, complete bool) (agentMemMovePlan, error) {
	var plan agentMemMovePlan
	switch policy {
	case "", agentMemMoveSkip, agentMemMoveRename:
	default:
		return plan, agentMemMoveBadReq("collision must be %q or %q", agentMemMoveSkip, agentMemMoveRename)
	}
	if !agentMemValidProjectID(fromID) || !agentMemValidProjectID(toID) {
		return plan, agentMemMoveBadReq("from and to must be project ids")
	}
	if fromID == toID {
		return plan, agentMemMoveBadReq("a project cannot be moved into itself")
	}
	if _, err := agentMemCheckDir("projects/"+fromID, false); err != nil {
		if os.IsNotExist(err) {
			return plan, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no such project")
		}
		return plan, err
	}
	from := agentMemProjectInfo(fromID)
	if !agentMemOrphaned(from, live, complete) {
		return plan, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"that project is not orphaned: its working copy still exists, or cannot be shown to be gone")
	}
	to := live[toID]
	plan.toLive = to != nil
	if to == nil {
		if _, err := agentMemCheckDir("projects/"+toID, false); err != nil {
			if os.IsNotExist(err) {
				return plan, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no such target project")
			}
			return plan, err
		}
		to = agentMemProjectInfo(toID)
		if agentMemRootGone(to.Root) {
			return plan, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "the target project's working copy is gone too")
		}
	}
	src, withheld, err := agentMemLoadDir(agentMemScopeProject, "projects/"+fromID)
	if err != nil {
		return plan, err
	}
	dst, _, err := agentMemLoadDir(agentMemScopeProject, "projects/"+toID)
	if err != nil {
		return plan, err
	}
	dstByName := map[string]agentMemEntry{}
	taken := map[string]bool{}
	for _, e := range dst {
		dstByName[e.Name] = e
		taken[e.Name] = true
	}
	// A file in the target that did not load (withheld) still holds its name.
	if dir, err := agentMemCheckDir("projects/"+toID, false); err == nil {
		ents, _ := os.ReadDir(dir)
		for _, d := range ents {
			if name, ok := strings.CutSuffix(d.Name(), ".md"); ok {
				taken[name] = true
			}
		}
	}
	sort.Slice(src, func(i, j int) bool { return src[i].Name < src[j].Name })
	srcNames := map[string]bool{}
	for _, e := range src {
		srcNames[e.Name] = true
	}
	plan.From, plan.To, plan.Withheld, plan.Collision = from, to, withheld, policy
	plan.Items = []agentMemMoveItem{}
	plan.Counts = map[string]int{}
	for _, e := range src {
		it := agentMemMoveItem{Name: e.Name, Description: e.Description, Type: e.Type, Revision: e.Revision}
		switch other, clash := dstByName[e.Name]; {
		case !taken[e.Name]:
			it.Status = agentMemMoveStatusMove
		case clash && agentMemMoveSame(e, other):
			it.Status = agentMemMoveStatusSame
		case policy == "":
			it.Status = agentMemMoveStatusCollision
			plan.NeedsPolicy = true
		case policy == agentMemMoveSkip:
			it.Status = agentMemMoveStatusSkip
		default:
			it.Status = agentMemMoveStatusSkip
			for n := 1; n <= agentMemMoveRenameTries; n++ {
				c := agentMemMoveRenamed(e.Name, n)
				if o, ok := dstByName[c]; ok && agentMemMoveSame(e, o) {
					// An earlier, interrupted run put this very copy there.
					it.Status, it.NewName = agentMemMoveStatusSame, c
					break
				}
				if !taken[c] && !srcNames[c] {
					it.Status, it.NewName = agentMemMoveStatusRename, c
					taken[c] = true
					break
				}
			}
		}
		plan.Items = append(plan.Items, it)
		plan.Counts[it.Status]++
	}
	return plan, nil
}

// agentMemMovePreviewFor is the read-only preview.
func agentMemMovePreviewFor(fromID, toID, policy string) (agentMemMovePreview, error) {
	live, complete := agentMemLiveProjects()
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	plan, err := agentMemMovePlanLocked(fromID, toID, policy, live, complete)
	return plan.agentMemMovePreview, err
}

// HandleAgentMemoryMemberMovePreview previews a move to the Console. Read-only.
func HandleAgentMemoryMemberMovePreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := agentMemMovePreviewFor(q.Get("from"), q.Get("to"), q.Get("collision"))
	if err != nil {
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// agentMemMoveReq moves the memories the member previewed. Collision is required once a name is
// taken: the member chooses, the server never does. Each item carries what the preview showed
// (the source revision, the status and the new name); the plan is made again at apply time and an
// item whose plan differs stays in the source, so the apply is bound to the preview.
type agentMemMoveReq struct {
	From      string                `json:"from"`
	To        string                `json:"to"`
	Collision string                `json:"collision"`
	Items     []agentMemMoveReqItem `json:"items"`
}

// agentMemMoveReqItem is one previewed memory.
type agentMemMoveReqItem struct {
	Name     string `json:"name"`
	NewName  string `json:"newName,omitempty"`
	Status   string `json:"status"`
	Revision int    `json:"revision"`
}

// agentMemMoveResultItem is what became of one requested memory.
type agentMemMoveResultItem struct {
	Name    string `json:"name"`
	NewName string `json:"newName,omitempty"`
	// Result is moved | merged (the target already held this text) | renamed | skipped, or, for
	// the memory a stopped move broke off at, partial (its copy is in the target, the source
	// copy is still there; Commit is the create) | failed (nothing was written).
	Result string `json:"result"`
	// Reason says why a memory stayed: collision | changed | exists | secret.
	Reason string `json:"reason,omitempty"`
	Commit string `json:"commit,omitempty"`
}

type agentMemMoveResult struct {
	Results []agentMemMoveResultItem `json:"results"`
	// Stopped names the memory a failure broke the move off at; Pending are the requested ones
	// after it, not attempted. Both are empty when the request ran to the end.
	Stopped string   `json:"stopped,omitempty"`
	Pending []string `json:"pending,omitempty"`
}

// agentMemMove moves the requested memories, up to agentMemMoveBatch of them. When a memory fails
// the result so far is returned with the error, so the caller can say what was done.
func agentMemMove(req agentMemMoveReq, now time.Time) (agentMemMoveResult, error) {
	out := agentMemMoveResult{Results: []agentMemMoveResultItem{}}
	if len(req.Items) == 0 || len(req.Items) > agentMemMoveBatch {
		return out, agentMemMoveBadReq("items must hold 1 to %d memories", agentMemMoveBatch)
	}
	for _, n := range req.Items {
		if !agentMemValidName(n.Name) || (n.NewName != "" && !agentMemValidName(n.NewName)) {
			return out, agentMemMoveBadReq("items holds a name that is not a memory name")
		}
	}
	if !agentMemMoveMu.TryLock() {
		return out, memoryErrf(http.StatusConflict, errCodeMemoryConflict, "another move is running; wait for it to finish")
	}
	defer agentMemMoveMu.Unlock()

	live, complete := agentMemLiveProjects()
	agentMemMu.RLock()
	plan, err := agentMemMovePlanLocked(req.From, req.To, req.Collision, live, complete)
	agentMemMu.RUnlock()
	if err != nil {
		return out, err
	}
	byName := map[string]agentMemMoveItem{}
	for _, it := range plan.Items {
		byName[it.Name] = it
	}
	var items []agentMemMoveItem
	for _, r := range req.Items {
		it, ok := byName[r.Name]
		switch {
		case ok && it.Status == agentMemMoveStatusCollision:
			return out, agentMemMoveBadReq("a name is taken in the target; choose to skip or rename the collisions")
		case !ok || it.Status != r.Status || it.NewName != r.NewName || it.Revision != r.Revision:
			// Not what the member previewed: it stays where it is.
			out.Results = append(out.Results, agentMemMoveResultItem{Name: r.Name, Result: "skipped", Reason: "changed"})
		default:
			items = append(items, it)
		}
	}
	for i, it := range items {
		res, err := agentMemMoveOne(plan, it, now)
		if err != nil {
			res.Result = "failed"
			if res.Commit != "" {
				res.Result = "partial"
			}
			res.Reason = ""
			out.Results = append(out.Results, res)
			out.Stopped = it.Name
			for _, rest := range items[i+1:] {
				out.Pending = append(out.Pending, rest.Name)
			}
			return out, memoryErrf(http.StatusInternalServerError, errCodeMemorySnapshotFailed,
				"the move stopped at one memory after %d of %d (%s); nothing was lost, preview and apply again to finish",
				i, len(items), agentMemErrKind(err))
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
}

// agentMemMoveOne moves one memory: a create in the target, then a forget in the source. Both
// are checked again under the write lock, because the plan was made before it.
func agentMemMoveOne(plan agentMemMovePlan, it agentMemMoveItem, now time.Time) (agentMemMoveResultItem, error) {
	res := agentMemMoveResultItem{Name: it.Name, NewName: it.NewName}
	skipped := func(reason string) (agentMemMoveResultItem, error) {
		res.Result, res.Reason = "skipped", reason
		return res, nil
	}
	if it.Status == agentMemMoveStatusSkip {
		return skipped("collision")
	}
	newName := it.Name
	if it.NewName != "" {
		newName = it.NewName
	}
	fromRel, toRel := "projects/"+plan.From.ID, "projects/"+plan.To.ID

	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	srcDir, err := agentMemCheckDir(fromRel, false)
	if err != nil {
		if os.IsNotExist(err) {
			return skipped("changed")
		}
		return res, err
	}
	cur, ok, err := agentMemLoadFile(filepath.Join(srcDir, it.Name+".md"), agentMemScopeProject, it.Name)
	if err != nil {
		return res, err
	}
	if !ok || cur.Withheld || cur.Entry.Revision != it.Revision {
		return skipped("changed")
	}

	var other agentMemLoaded
	otherOK := false
	if dir, err := agentMemCheckDir(toRel, false); err == nil {
		if other, otherOK, err = agentMemLoadFile(filepath.Join(dir, newName+".md"), agentMemScopeProject, newName); err != nil {
			return res, err
		}
	} else if !os.IsNotExist(err) {
		return res, err
	}

	if it.Status == agentMemMoveStatusSame {
		if !otherOK || other.Withheld || !agentMemMoveSame(cur.Entry, other.Entry) {
			return skipped("changed")
		}
		res.Result = "merged"
	} else {
		if otherOK {
			// Whatever appeared since the plan was made, it is never overwritten.
			return skipped("exists")
		}
		tomb, err := agentMemTombRevision(toRel, newName)
		if err != nil {
			return res, err
		}
		e := cur.Entry
		e.Name, e.Scope, e.Revision, e.Uses = newName, agentMemScopeProject, tomb+1, 0
		data := agentMemRender(e)
		if f := agentMemScanText("memory", string(data)); len(f) > 0 {
			return skipped("secret")
		}
		repoRel := toRel + "/" + newName + ".md"
		changes := []agentMemChange{{Rel: repoRel, Data: data}}
		if plan.toLive {
			extra, err := agentMemProjectInfoChange(*plan.To)
			if err != nil {
				var se *agentMemSecretErr
				if errors.As(err, &se) {
					return skipped("secret")
				}
				return res, err
			}
			changes = append(changes, extra...)
		}
		rev, err := agentMemApplyLocked(changes, "create", repoRel, agentMemMember, now,
			"AF-Moved-From: "+fromRel+"/"+it.Name+".md")
		if err != nil {
			return res, err
		}
		agentMemRemoveTomb(toRel, newName)
		agentMemClearUsage(toRel, newName)
		agentMemPrimeScan(data, agentMemScopeProject, newName)
		res.Commit = rev
		res.Result = "moved"
		if it.NewName != "" {
			res.Result = "renamed"
		}
	}

	if err := agentMemMoveStep("created", it.Name); err != nil {
		return res, err
	}
	srcRel := fromRel + "/" + it.Name + ".md"
	base, err := forgetIntent(fromRel, it.Name)
	if err != nil {
		return res, err
	}
	if err := agentMemWriteTomb(fromRel, it.Name, cur.Entry.Revision); err != nil {
		return res, err
	}
	rev, err := agentMemApplyLocked([]agentMemChange{{Rel: srcRel, Delete: true}}, "forget", srcRel, agentMemMember, now,
		"AF-Moved-To: "+toRel+"/"+newName+".md")
	if err != nil {
		return res, err
	}
	agentMemClearUsage(fromRel, it.Name)
	forgetDone(fromRel, it.Name, base)
	if res.Commit == "" {
		res.Commit = rev
	}
	return res, nil
}

// agentMemMoveStoppedWire is the answer to a move that broke off: the error and what was done.
type agentMemMoveStoppedWire struct {
	Error agentMemSecretWireErr `json:"error"`
	agentMemMoveResult
}

// HandleAgentMemoryMemberMove moves memories between projects as the member.
func HandleAgentMemoryMemberMove(w http.ResponseWriter, r *http.Request) {
	if !memberorigin.RefuseUnverified(w, r) {
		return
	}
	var req agentMemMoveReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	res, err := agentMemMove(req, time.Now())
	if err != nil {
		var ue *memoryUserErr
		if res.Stopped != "" && errors.As(err, &ue) {
			httpx.WriteJSON(w, ue.Status, agentMemMoveStoppedWire{
				Error: agentMemSecretWireErr{Code: ue.Code, Message: ue.Msg}, agentMemMoveResult: res,
			})
			return
		}
		agentMemWriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
