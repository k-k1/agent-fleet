// Package homeusage measures where a whole home volume went, by category — the Settings
// "Machine" tab's breakdown (#3083, phase 1 of #3079).
//
// The other cleanup surveys each measure what one cleanup can take. None answers "where did
// my 70G go": on the home that prompted this, the two largest buckets (build output inside
// ~100 worktrees, ~/.gradle) were in no survey at all.
//
// The figures are meant to agree with `du -sx ~`: allocated blocks, a hard-linked file counted
// once, no symlink followed and no other filesystem entered. Following links would count
// ~/.config (which may live on other storage), a worktree's node_modules shared from the parent
// clone twice, and /scratch, none of which a cleanup of home gives back.
//
// Which directories mean what is the caller's (Config.Roots): the tool-cache rows and the
// trash live in package main.
package homeusage

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Place says where a figure lives, in the shape of package main's usagePlace (same JSON), so
// the Console can open the folder.
type Place struct {
	Path   string `json:"path,omitempty"`
	Browse string `json:"browse,omitempty"`
}

// Config is what one walk needs from the caller.
type Config struct {
	Home   string
	Repos  string // the working copies' root (~/repos)
	AfWork string // ~/.af-work
	Roots  map[string]Root
	// Place turns an absolute path into a Place; nil leaves the path as is.
	Place func(abs string) Place
	// AfWorkOrphans names the ~/.af-work folders the leftovers button would remove;
	// judged=false when the session store cannot be read.
	AfWorkOrphans func() (orphans map[string]bool, judged bool)
}

// Root is a directory with a category (and a part name) of its own. The deepest root
// containing a path decides; anything under no root is other.
type Root struct {
	Cat, Part string
	// Reclaimable = an existing cleanup can take all of it (a tool-cache row, the trash).
	Reclaimable bool
}

// The categories, in the order the Console lists them.
const (
	CatSource      = "source"      // working copies: tracked and untracked files, git data
	CatRegenerable = "regenerable" // build output inside working copies
	CatToolCache   = "tool_cache"  // package-manager and build-tool caches
	CatTools       = "tools"       // installed runtimes, JDKs, CLIs
	CatAfWork      = "af_work"     // ~/.af-work
	CatTrash       = "trash"       // cleanup archives (ADR 0101)
	CatAgent       = "agent"       // Agent Fleet's own data and cache, the trash excluded
	CatOther       = "other"
)

var categoryOrder = []string{
	CatSource, CatRegenerable, CatToolCache, CatTools,
	CatAfWork, CatTrash, CatAgent, CatOther,
}

// regenerableNames are the directory names inside a working copy counted as build output.
// The figure is an estimate of what a later cleanup could take; that cleanup judges each
// directory again (ignored, untracked, not in use) before it deletes anything.
// A directory of that name holding a tracked file is source instead (this repository tracks
// docs/build/), which is why each working copy's file list is read once before the walk.
var regenerableNames = map[string]bool{
	"node_modules": true, ".next": true, "target": true, ".gradle": true, "build": true, "dist": true,
}

const (
	// maxEntries caps one walk; past it the figures are lower bounds (truncated).
	maxEntries = 20_000_000
	// maxInodes caps the hard-link ledger. Past it a further hard link counts every
	// time it is met, which overstates rather than stops; it is said as approximate.
	maxInodes  = 2_000_000
	gitTimeout = 20 * time.Second
)

// --- the answer ---------------------------------------------------------------------------

type Part struct {
	Name        string `json:"name"`
	Bytes       int64  `json:"bytes"`
	Reclaimable bool   `json:"reclaimable,omitempty"`
	Place
}

type Category struct {
	Key   string `json:"key"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
	// Reclaimable is what an existing cleanup can give back today; 0 where none applies yet.
	Reclaimable int64  `json:"reclaimable"`
	Parts       []Part `json:"parts,omitempty"`
}

// Repo is one working copy directly under ~/repos.
type Repo struct {
	Name        string `json:"name"`
	Bytes       int64  `json:"bytes"`
	Regenerable int64  `json:"regenerable"`
	// Unjudged = its file list could not be read, so no directory in it was counted as build
	// output (the conservative side: a tracked build/ is never shown as reclaimable).
	Unjudged bool `json:"unjudged,omitempty"`
	Place
}

// AfWork is one folder directly under ~/.af-work.
type AfWork struct {
	Name     string `json:"name"`
	Bytes    int64  `json:"bytes"`
	Modified string `json:"modified,omitempty"` // newest mtime inside, RFC 3339
	// Marked = the Agent created it for a session (paths.SessionWorkDirMarker).
	Marked bool `json:"marked,omitempty"`
	// Orphan = no session or working copy is named after it: the leftovers button takes it.
	Orphan bool `json:"orphan,omitempty"`
	Place
}

type Usage struct {
	Total      int64      `json:"total"`
	Files      int        `json:"files"`
	Categories []Category `json:"categories"`
	Repos      []Repo     `json:"repos"`
	AfWork     []AfWork   `json:"af_work"`
	// AfWorkJudged = the session store could be read, so Orphan means something.
	AfWorkJudged bool `json:"af_work_judged"`
	// Truncated = the entry cap stopped the walk; the figures are lower bounds.
	Truncated bool `json:"truncated,omitempty"`
	// Approximate = the hard-link ledger filled up, so some hard links count more than once.
	Approximate bool `json:"approximate,omitempty"`
	// Unreadable counts directories the walk could not list; what they hold is in no figure.
	Unreadable int    `json:"unreadable,omitempty"`
	MeasuredAt string `json:"measured_at"`
	ElapsedMs  int64  `json:"elapsed_ms"`
}

// --- the walk -----------------------------------------------------------------------------

type groupTally struct {
	bytes, regen int64
	newest       time.Time
}

type walker struct {
	dev     uint64
	place   func(abs string) Place
	roots   map[string]Root
	repos   string
	afWork  string
	tracked map[string]map[string]bool // working copy → tracked directories (relative, slash)
	budget  int
	scanned *atomic.Int64

	seen        map[[2]uint64]struct{}
	approximate bool
	truncated   bool
	unreadable  int

	cats  map[string]*Category
	parts map[string]map[string]*Part // category → part name → tally
	repo  map[string]*groupTally
	afw   map[string]*groupTally
	total int64
	files int
}

// walkState is what a directory hands down to its children.
type walkState struct {
	cat, part   string
	partPath    string // the root directory the part is named after, "" for none
	reclaimable bool
	repo        string // the working copy it is in, "" outside ~/repos
	rel         string // path relative to that working copy, slash-separated
	afWork      string // the ~/.af-work folder it is in
}

func newWalker(cfg Config, tracked map[string]map[string]bool, scanned *atomic.Int64) (*walker, error) {
	fi, err := os.Stat(cfg.Home)
	if err != nil {
		return nil, err
	}
	place := cfg.Place
	if place == nil {
		place = func(abs string) Place { return Place{Path: abs} }
	}
	w := &walker{
		place: place, roots: cfg.Roots, repos: cfg.Repos, afWork: cfg.AfWork,
		tracked: tracked, budget: maxEntries, scanned: scanned,
		seen: map[[2]uint64]struct{}{}, cats: map[string]*Category{},
		parts: map[string]map[string]*Part{}, repo: map[string]*groupTally{}, afw: map[string]*groupTally{},
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		w.dev = uint64(st.Dev)
	}
	return w, nil
}

func (w *walker) add(s walkState, size int64, file bool, mtime time.Time) {
	w.total += size
	c := w.cats[s.cat]
	if c == nil {
		c = &Category{Key: s.cat}
		w.cats[s.cat] = c
	}
	c.Bytes += size
	if s.reclaimable {
		c.Reclaimable += size
	}
	if file {
		w.files++
		c.Files++
	}
	if s.part != "" {
		m := w.parts[s.cat]
		if m == nil {
			m = map[string]*Part{}
			w.parts[s.cat] = m
		}
		p := m[s.part]
		if p == nil {
			p = &Part{Name: s.part, Reclaimable: s.reclaimable}
			if s.partPath != "" {
				p.Place = w.place(s.partPath)
			}
			m[s.part] = p
		}
		p.Bytes += size
	}
	if s.repo != "" {
		g := w.group(w.repo, s.repo)
		g.bytes += size
		if s.cat == CatRegenerable {
			g.regen += size
		}
	}
	if s.afWork != "" {
		g := w.group(w.afw, s.afWork)
		g.bytes += size
		if mtime.After(g.newest) {
			g.newest = mtime
		}
	}
}

func (w *walker) group(m map[string]*groupTally, k string) *groupTally {
	g := m[k]
	if g == nil {
		g = &groupTally{}
		m[k] = g
	}
	return g
}

// walk visits dir's entries; dir itself has been counted by the caller.
func (w *walker) walk(dir string, s walkState) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		w.unreadable++
		return
	}
	for _, e := range ents {
		if w.budget <= 0 {
			w.truncated = true
			return
		}
		w.budget--
		w.scanned.Add(1)
		p := filepath.Join(dir, e.Name())
		info, err := e.Info() // lstat: a symlink is the link, never its target
		if err != nil {
			continue // gone since the listing
		}
		var size int64
		var nlink uint64
		st, hasSt := info.Sys().(*syscall.Stat_t)
		if hasSt {
			size = st.Blocks * 512
			nlink = uint64(st.Nlink)
		} else {
			size = info.Size()
		}
		isDir := info.IsDir()
		if isDir && hasSt && w.dev != 0 && uint64(st.Dev) != w.dev {
			continue // another filesystem mounted here: not home's bytes
		}
		cs := w.child(dir, p, e.Name(), isDir, s)
		if !isDir && nlink > 1 && hasSt {
			k := [2]uint64{uint64(st.Dev), st.Ino}
			if _, dup := w.seen[k]; dup {
				continue
			}
			if len(w.seen) < maxInodes {
				w.seen[k] = struct{}{}
			} else {
				w.approximate = true
			}
		}
		w.add(cs, size, !isDir, info.ModTime())
		if isDir {
			w.walk(p, cs)
		}
	}
}

// child works out the state of entry p (named name) inside dir.
func (w *walker) child(dir, p, name string, isDir bool, s walkState) walkState {
	cs := s
	if isDir {
		if r, ok := w.roots[p]; ok {
			cs.cat, cs.part, cs.partPath, cs.reclaimable = r.Cat, r.Part, p, r.Reclaimable
		}
	}
	switch {
	case dir == w.repos:
		cs.repo, cs.rel = name, ""
		cs.cat, cs.part, cs.partPath, cs.reclaimable = CatSource, "", "", false
	case s.repo != "":
		if s.rel == "" {
			cs.rel = name
		} else {
			cs.rel = s.rel + "/" + name
		}
		if isDir && s.cat == CatSource && regenerableNames[name] {
			if t, judged := w.tracked[s.repo]; judged && !t[cs.rel] {
				cs.cat, cs.part, cs.partPath = CatRegenerable, name, ""
			}
		}
	}
	if dir == w.afWork {
		cs.afWork = name
	}
	return cs
}

func (w *walker) result() *Usage {
	u := &Usage{
		Total: w.total, Files: w.files, Truncated: w.truncated, Approximate: w.approximate,
		Unreadable: w.unreadable, Categories: []Category{}, Repos: []Repo{}, AfWork: []AfWork{},
	}
	for _, k := range categoryOrder {
		c := w.cats[k]
		if c == nil {
			continue
		}
		for _, p := range w.parts[k] {
			c.Parts = append(c.Parts, *p)
		}
		sort.Slice(c.Parts, func(i, j int) bool { return c.Parts[i].Bytes > c.Parts[j].Bytes })
		u.Categories = append(u.Categories, *c)
	}
	for name, g := range w.repo {
		_, judged := w.tracked[name]
		u.Repos = append(u.Repos, Repo{
			Name: name, Bytes: g.bytes, Regenerable: g.regen, Unjudged: !judged,
			Place: w.place(filepath.Join(w.repos, name)),
		})
	}
	sort.Slice(u.Repos, func(i, j int) bool {
		if u.Repos[i].Regenerable != u.Repos[j].Regenerable {
			return u.Repos[i].Regenerable > u.Repos[j].Regenerable
		}
		return u.Repos[i].Bytes > u.Repos[j].Bytes
	})
	for name, g := range w.afw {
		row := AfWork{Name: name, Bytes: g.bytes, Place: w.place(filepath.Join(w.afWork, name))}
		if !g.newest.IsZero() {
			row.Modified = g.newest.UTC().Format(time.RFC3339)
		}
		_, err := os.Lstat(filepath.Join(w.afWork, name, paths.SessionWorkDirMarker))
		row.Marked = err == nil
		u.AfWork = append(u.AfWork, row)
	}
	sort.Slice(u.AfWork, func(i, j int) bool { return u.AfWork[i].Bytes > u.AfWork[j].Bytes })
	return u
}

// markAfWorkOrphans flags the folders orphans names and folds their size into the category's
// reclaimable figure. judged=false (no readable session store) flags nothing, and
// AfWorkJudged says so.
func markAfWorkOrphans(u *Usage, orphans map[string]bool, judged bool) {
	u.AfWorkJudged = judged
	if !judged {
		return
	}
	var reclaim int64
	for i := range u.AfWork {
		if orphans[u.AfWork[i].Name] {
			u.AfWork[i].Orphan = true
			reclaim += u.AfWork[i].Bytes
		}
	}
	for i := range u.Categories {
		if u.Categories[i].Key == CatAfWork {
			u.Categories[i].Reclaimable = reclaim
		}
	}
}

// TrackedDirs reads one working copy's file list and returns every directory that holds a
// tracked file. ok=false when git cannot answer (not a repository, a broken worktree, too
// slow): the walk then counts nothing in it as build output.
func TrackedDirs(ctx context.Context, dir string) (map[string]bool, bool) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-z")
	// A folder under ~/repos that is not a repository must not answer with an enclosing one.
	cmd.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	dirs := map[string]bool{}
	for _, f := range bytes.Split(out, []byte{0}) {
		p := string(f)
		for {
			i := strings.LastIndexByte(p, '/')
			if i < 0 {
				break
			}
			p = p[:i]
			if dirs[p] {
				break // its parents are in already
			}
			dirs[p] = true
		}
	}
	return dirs, true
}

// Measure walks cfg.Home once. scanned counts the entries visited so far, for progress.
func Measure(ctx context.Context, cfg Config, scanned *atomic.Int64) (*Usage, error) {
	start := time.Now()
	tracked := map[string]map[string]bool{}
	ents, _ := os.ReadDir(cfg.Repos)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if t, ok := TrackedDirs(ctx, filepath.Join(cfg.Repos, e.Name())); ok {
			tracked[e.Name()] = t
		}
	}
	w, err := newWalker(cfg, tracked, scanned)
	if err != nil {
		return nil, err
	}
	w.walk(cfg.Home, walkState{cat: CatOther})
	u := w.result()
	if cfg.AfWorkOrphans != nil {
		orphans, judged := cfg.AfWorkOrphans()
		markAfWorkOrphans(u, orphans, judged)
	}
	u.MeasuredAt = time.Now().UTC().Format(time.RFC3339)
	u.ElapsedMs = time.Since(start).Milliseconds()
	return u, nil
}

// Runner holds the one walk that may run at a time and the last result, which is kept so a
// later reader (the Console on opening the tab, a low-disk notice) can quote it without
// walking again. The walk runs in the background: a home of millions of entries outlives the
// CP's two-minute request to the Agent (control-plane/agent_client.go).
type Runner struct {
	mu      sync.Mutex
	running bool
	started time.Time
	scanned atomic.Int64
	last    *Usage
	err     string
}

// Status is the answer of GET /cleanup/home.
type Status struct {
	Running   bool   `json:"running"`
	StartedAt string `json:"started_at,omitempty"`
	Scanned   int64  `json:"scanned"`
	Error     string `json:"error,omitempty"`
	Result    *Usage `json:"result"`
}

// Snapshot reports the walk in progress, if any, and the last finished result.
func (r *Runner) Snapshot() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Status{Running: r.running, Scanned: r.scanned.Load(), Error: r.err, Result: r.last}
	if r.running {
		s.StartedAt = r.started.UTC().Format(time.RFC3339)
	}
	return s
}

// Start begins a walk with the Config cfg returns, unless one is running; started reports
// which. cfg is called on the walk's goroutine, so what it reads is current when the walk begins.
func (r *Runner) Start(cfg func() Config) (started bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return false
	}
	r.running = true
	r.started = time.Now()
	r.scanned.Store(0)
	go func() {
		u, err := Measure(context.Background(), cfg(), &r.scanned)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.running = false
		if err != nil {
			r.err = err.Error()
			return
		}
		r.err = ""
		r.last = u
	}()
	return true
}
