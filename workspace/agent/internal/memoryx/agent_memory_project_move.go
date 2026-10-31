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
	"sync/atomic"
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
	// agentMemLiveBudget bounds the directories one search reads, both phases together, agentMemLiveTimeout its time.
	// Past either the answer is "incomplete" and nothing is called orphaned.
	agentMemLiveBudget  = 100000
	agentMemLiveTimeout = 20 * time.Second
	// agentMemLiveSoft is when phase two of the search stops reading; it leaves time for the
	// session directories and for the answer inside agentMemLiveTimeout.
	agentMemLiveSoft = 12 * time.Second
	// agentMemLiveWorkers caps the git processes the search runs at once. Fixed and small: the
	// host is shared and memory-constrained, and every process of a clone is short.
	agentMemLiveWorkers = 4
)

// agentMemLiveSkip names the dependency stores the search never enters, at any depth, and whose
// being skipped does not make the answer incomplete: a working copy inside one is not a layout
// anyone uses, and both decisions the answer backs (one clone has this remote, a project has no
// clone left) still go through a preview or a named project. Everything else that is not read
// makes the answer incomplete. A directory named like this that is itself a working copy still counts as one; only what
// is inside it is not entered. Changing the list changes what members can rely on: update the ADR 0108 note.
var agentMemLiveSkip = map[string]bool{
	"node_modules": true,
	".venv":        true,
	"venv":         true,
	".gradle":      true,
	".pnpm-store":  true,
	".cache":       true,
}

// agentMemLiveProjects maps the working copies to their projects, by id: every directory under
// ~/repos with a .git or .svn entry, found at any depth, inside other working copies and behind
// symlinks included, and every session's working directory. A project with a live working copy
// is never orphaned, whatever its recorded root says. complete=false says something could not be
// ruled out: a directory that could not be read, a repository whose identity could not be
// resolved, or the budget or time of the search ran out. The map then holds what was found, so a
// project known to be live stays live; only "nothing else exists" is withdrawn.
//
// The directories of agentMemLiveSkip are not entered. The search runs in two phases because reading every tree of a large workspace takes longer than
// the time allowed (800k entries: 20-30 s, #3045). Phase one walks everything outside working
// copies and, inside a git working copy, everything git does not ignore. Phase two walks the
// ignored content (node_modules, build output) and svn working copies, until agentMemLiveSoft:
// what it did not reach makes the answer incomplete rather than guessed empty.
func agentMemLiveProjects() (map[string]*agentMemProject, bool) {
	type result struct {
		live     map[string]*agentMemProject
		complete bool
	}
	// The limits are read here, on the caller's goroutine, and travel with st: a search abandoned
	// by the timeout below keeps running and must not read the package variables afterwards.
	st := &agentMemLiveState{
		live:   map[string]*agentMemProject{},
		stop:   make(chan struct{}),
		budget: agentMemLiveBudget,
		soft:   agentMemLiveSoft,
		// workers and runner are replaced by tests too, so they are read once here as well.
		workers: agentMemLiveWorkers,
		runner:  agentMemLiveRunner,
	}
	timeout := agentMemLiveTimeout
	// Closing stop on the way out makes an abandoned search stop spawning git processes.
	defer close(st.stop)
	done := make(chan result, 1)
	go func() {
		c := agentMemLiveSearch(st)
		done <- result{st.snapshot(), c}
	}()
	select {
	case r := <-done:
		return r.live, r.complete
	case <-time.After(timeout):
		return st.snapshot(), false
	}
}

// agentMemLiveState is the map a search fills, readable by the caller that gives up waiting.
type agentMemLiveState struct {
	mu   sync.Mutex
	live map[string]*agentMemProject
	// stop is closed when the caller no longer waits; nil (never ready) for a search run directly.
	stop chan struct{}
	// The tunables a test may replace (agentMemLiveBudget, agentMemLiveSoft, agentMemLiveWorkers,
	// agentMemLiveRunner) as they were when the search began; read-only afterwards, so an
	// abandoned search never reads the package variables.
	budget  int
	soft    time.Duration
	workers int
	runner  func(dir string) agentMemGitRunner
}

func (s *agentMemLiveState) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *agentMemLiveState) put(p *agentMemProject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live[p.ID] == nil {
		s.live[p.ID] = p
	}
}

func (s *agentMemLiveState) snapshot() map[string]*agentMemProject {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*agentMemProject, len(s.live))
	for k, v := range s.live {
		out[k] = v
	}
	return out
}

// agentMemLiveRunner is the git runner of the live search, replaceable by a test that counts calls.
var agentMemLiveRunner = func(dir string) agentMemGitRunner {
	return func(args ...string) (string, error) { return gitx.Run(dir, args...) }
}

// agentMemIgnoredDirs lists the directories git ignores in a working copy, by absolute path.
// ok=false when git could not say. NUL-separated and untrimmed: without -z git quotes names with
// non-ASCII or control characters, and a quoted name never matches a directory.
func agentMemIgnoredDirs(dir string) (map[string]bool, bool) {
	out, err := gitx.Cmd(dir, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory").Output()
	if err != nil {
		return nil, false
	}
	ign := map[string]bool{}
	for _, name := range strings.Split(string(out), "\x00") {
		if rel, ok := strings.CutSuffix(name, "/"); ok && rel != "" {
			ign[filepath.Join(dir, filepath.FromSlash(rel))] = true
		}
	}
	return ign, true
}

// forEach runs work(i) for i in [0,n) on at most agentMemLiveWorkers goroutines and
// calls apply(i) on the caller's goroutine in index order, as soon as item i is done: results
// land in the same order a sequential search would produce them, whatever the scheduling, and a
// timeout finds the finished prefix already applied. It returns false when the search was
// abandoned; every worker then stops before its next item (an item already running finishes).
func (s *agentMemLiveState) forEach(n int, work, apply func(i int)) bool {
	if n == 0 {
		return true
	}
	done := make([]chan struct{}, n)
	for i := range done {
		done[i] = make(chan struct{})
	}
	var next atomic.Int64
	workers := min(max(s.workers, 1), n)
	for w := 0; w < workers; w++ {
		go func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= n || s.stopped() {
					return
				}
				work(i)
				close(done[i])
			}
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case <-done[i]:
			apply(i)
		case <-s.stop:
			return false
		}
	}
	return true
}

func agentMemLiveSearch(st *agentMemLiveState) (complete bool) {
	complete = true
	start := time.Now()
	// resolved holds the directories whose identity was computed, by cleaned path: a session's
	// directory the walk already found costs nothing. Not by real path: an id derived from the
	// path (svn, no VCS) differs between a symlink and its target, and each must be live.
	// resolved, visited and complete belong to this goroutine alone; the workers of forEach only
	// fill the slots of their own job.
	resolved := map[string]bool{}
	// claim says whether dir's identity is still to be computed, and marks it.
	claim := func(dir string) bool {
		dir = filepath.Clean(dir)
		if resolved[dir] {
			return false
		}
		resolved[dir] = true
		return true
	}
	// record files the identity of a claimed directory.
	record := func(p *agentMemProject, identified bool) {
		if p == nil {
			return
		}
		if !identified {
			// The id would be derived from the path, not the repository: it cannot be compared.
			complete = false
		}
		st.put(p)
	}
	// ign is the set of directories git ignores in the working copy the item lies in: phase one
	// only. An item of the deferred phase is walked whole.
	type item struct {
		dir string
		ign map[string]bool
	}
	// child is one subdirectory found by walk, with the git work it needs.
	type child struct {
		dir           string
		isCopy, isGit bool
		skipped       bool // a dependency store: only its own identity counts
		ident         bool // the identity is to be computed (claimed)
		wantIgn       bool // phase one of a git working copy: list what it ignores
		// results, filled by a worker
		p          *agentMemProject
		identified bool
		ign        map[string]bool
		ignOK      bool
	}
	var deferred []item
	visited := map[string]bool{}
	// walk reads one directory and returns its subdirectories to read next, split by phase.
	walk := func(cur item, whole bool) (next, later []item, ok bool) {
		if real, err := filepath.EvalSymlinks(cur.dir); err == nil {
			if visited[real] {
				return nil, nil, true
			}
			visited[real] = true
		}
		ents, err := os.ReadDir(cur.dir)
		if err != nil {
			// Not knowing what is below is not knowing there is nothing below.
			complete = false
			return nil, nil, true
		}
		var kids []*child
		for _, e := range ents {
			if e.Name() == ".git" {
				continue // git's own directory holds no working copy; a .git entry is the marker below
			}
			d := filepath.Join(cur.dir, e.Name())
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
			k := &child{dir: d}
			for _, marker := range []string{".git", ".svn"} {
				if _, err := os.Lstat(filepath.Join(d, marker)); err == nil {
					k.isCopy, k.isGit = true, marker == ".git"
					break
				} else if !errors.Is(err, fs.ErrNotExist) {
					complete = false
				}
			}
			k.skipped = agentMemLiveSkip[e.Name()]
			// A skipped directory may itself be a working copy; only its content is not entered.
			if k.isCopy {
				k.ident = claim(d)
			}
			k.wantIgn = !k.skipped && !whole && k.isGit
			kids = append(kids, k)
		}
		// The git work of every child at once, bounded; the results are applied in directory order.
		okAll := st.forEach(len(kids), func(i int) {
			k := kids[i]
			if k.ident {
				k.p, k.identified = agentMemProjectIdent(k.dir, k.dir, st.runner(k.dir))
			}
			if k.wantIgn {
				k.ign, k.ignOK = agentMemIgnoredDirs(k.dir)
			}
		}, func(i int) {
			k := kids[i]
			if k.ident {
				record(k.p, k.identified)
			}
			if k.skipped {
				return
			}
			switch {
			case whole:
				next = append(next, item{k.dir, nil})
			case k.isCopy && k.isGit:
				if k.ignOK {
					next = append(next, item{k.dir, k.ign})
				} else {
					later = append(later, item{k.dir, nil})
				}
			case k.isCopy:
				later = append(later, item{k.dir, nil}) // svn says nothing of what it ignores: its content is phase two
			case cur.ign[k.dir]:
				later = append(later, item{k.dir, nil})
			default:
				next = append(next, item{k.dir, cur.ign})
			}
		})
		if !okAll {
			return nil, nil, false
		}
		return next, later, true
	}
	queue := []item{{gitx.ReposRoot(), nil}}
	reads := 0
	for len(queue) > 0 {
		if st.stopped() {
			return false
		}
		cur := queue[0]
		queue = queue[1:]
		if reads++; reads > st.budget {
			complete = false
			break
		}
		next, later, ok := walk(cur, false)
		if !ok {
			return false
		}
		queue = append(queue, next...)
		deferred = append(deferred, later...)
	}
	for len(deferred) > 0 {
		if time.Since(start) > st.soft {
			complete = false
			break
		}
		if reads++; reads > st.budget {
			complete = false
			break
		}
		if st.stopped() {
			return false
		}
		cur := deferred[0]
		deferred = deferred[1:]
		next, _, ok := walk(cur, true)
		if !ok {
			return false
		}
		deferred = append(deferred, next...)
	}
	for _, m := range session.ListMetas() {
		if st.stopped() {
			return false
		}
		if m.Dir != "" {
			if _, err := os.Stat(m.Dir); err == nil && claim(m.Dir) {
				record(agentMemProjectIdent(m.Dir, m.Dir, st.runner(m.Dir)))
			}
		}
	}
	return complete
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
