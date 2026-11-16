package memoryx

// One-time import of muse's native memory into AF memory (ADR 0108 decision 6, #1991, #2005). It
// runs the claude import's evaluation (agent_memory_claude_import.go): preview first, secret hits
// skipped, the forgotten never brought back, the member as the commit's author and the author of
// the memory unknown.
//
// muse has three stores (measured on muse 1.4.4, ADR 0108 note of 2026-10-12):
//
//   - project: <working copy>/.agents/memory, into that working copy's AF project. It is inside the
//     repository and a team may commit it, so every item says whether git tracks it.
//   - personal_project: ~/.local/share/muse/memory/projects/<slug>-<16 hex>, into the AF project of
//     the working copy that directory belongs to. <slug> is the absolute cwd with its separators
//     turned into "-" and the hex is the FNV-1a 64 hash of that absolute path (measured: the
//     probe's directory name matched it), so the mapping runs forward from each working copy under
//     ~/repos. A directory no hash matches is matched by its slug and is "ambiguous" when two
//     different projects fit.
//   - personal: ~/.local/share/muse/memory/personal. It would land in AF's user scope, which every
//     project reads, so it is read only when the caller asks for that explicitly (into=user).
//
// muse writes a note's content verbatim: no frontmatter is needed, and a path may contain "/"
// (it is relative to the scope root). A note without frontmatter takes its name from the file
// name, its description from the first non-empty line and no type; a nested note "a/b.md" is the
// AF memory "a--b" (two files that map to one name are both refused). Directories are walked
// through pinned handles and never through a symlink. .muse-memory.lock and anything that is not
// .md are not memories. MEMORY.md at a scope root is muse's index, but muse also stores short facts
// in it (measured on muse 1.4.20: add_memory with path MEMORY.md), so its lines that are not index
// links are one note per store, named museIndexNoteName; an index of links only is no memory.

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/projcfg"
)

const (
	// agentMemMuseSourcePrefix marks a memory imported from muse; a muse update only replaces a
	// copy that carries it.
	agentMemMuseSourcePrefix = "muse:"
	// agentMemMuseSubdir is muse's project-scope store, relative to a working copy.
	agentMemMuseSubdir = ".agents/memory"

	museScopeProject         = "project"
	museScopePersonalProject = "personal_project"
	museScopePersonal        = "personal"
	// museIntoUser is the explicit choice that sends the personal scope into AF's user scope.
	museIntoUser = "user"

	// museImportDiverged: AF has its own version (Reason says why) and muse's file is not applied.
	museImportDiverged = "diverged"

	// museImportChooseUser is the preview's Reason for the personal scope until into=user is given.
	museImportChooseUser = "choose_user_scope"
	// museMaxDepth bounds the directories walked below a scope root.
	museMaxDepth = 6
	// museIndexNoteName is the AF memory made of the facts in a scope root's MEMORY.md. It is a
	// fixed name so a re-run finds the same memory; the "muse-" prefix keeps it clear of the
	// topic files muse names after their subject.
	museIndexNoteName = "muse-memory-md"
)

var (
	museDirRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+=-]{0,254}$`)
	// museHashSuffixRe splits "<slug>-<16 hex>".
	museHashSuffixRe = regexp.MustCompile(`^(.*)-([0-9a-f]{16})$`)
	// museIndexLinkRe matches an index line "- [Title](file.md) ...".
	museIndexLinkRe = regexp.MustCompile(`^\s*[-*+]\s*\[[^\]]*\]\([^)]*\)`)
)

// museIndexFacts is MEMORY.md reduced to the facts muse stored in it: index links, blank lines and
// headings are dropped. "" means the file is an index only.
func museIndexFacts(raw []byte) []byte {
	var keep []string
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || museIndexLinkRe.MatchString(line) {
			continue
		}
		keep = append(keep, strings.TrimRight(line, " \t"))
	}
	if len(keep) == 0 {
		return nil
	}
	return []byte(strings.Join(keep, "\n") + "\n")
}

// museIndexHasFacts reads a scope root's MEMORY.md. A file that cannot be opened (a symlink) is not
// listed; one that opens but cannot be read is, so the preview shows it as invalid.
func museIndexHasFacts(root *os.File) bool {
	f, err := agentMemImportOpen(root, agentMemImportIndex)
	if err != nil {
		return false
	}
	defer f.Close()
	raw, err := agentMemReadHandle(f)
	return err != nil || museIndexFacts(raw) != nil
}

// agentMemMuseSource is one muse directory that has notes.
type agentMemMuseSource struct {
	// Scope is project | personal_project | personal. Dir is the working copy's name (project) or
	// muse's own directory name (personal_project); the personal scope has none.
	Scope string `json:"scope"`
	Dir   string `json:"dir"`
	Count int    `json:"count"`
	// Shared counts the files git tracks in that working copy (project scope).
	Shared int `json:"shared"`
	// Target is "user" for the personal scope: it needs an explicit choice and has no project.
	Target string `json:"target,omitempty"`
	// Project is where the files would go; nil when Reason says why not.
	Project *agentMemProject `json:"project,omitempty"`
	Reason  string           `json:"reason,omitempty"`
}

type agentMemMuseSources struct {
	Sources  []agentMemMuseSource `json:"sources"`
	Withheld int                  `json:"withheld,omitempty"`
}

type agentMemMusePreview struct {
	Scope    string               `json:"scope"`
	Dir      string               `json:"dir"`
	Target   string               `json:"target"`
	Project  *agentMemProject     `json:"project,omitempty"`
	Reason   string               `json:"reason,omitempty"`
	Items    []agentMemImportItem `json:"items"`
	Counts   map[string]int       `json:"counts"`
	Withheld int                  `json:"withheld,omitempty"`
	Truncate bool                 `json:"truncated,omitempty"`
	byName   map[string]*agentMemImportItem
}

// agentMemMuseDirOK says whether a name may be taken for a working copy under ~/repos.
func agentMemMuseDirOK(dir string) bool {
	return dir != "" && dir != "." && dir != ".." && !strings.ContainsAny(dir, "/\\\x00") &&
		agentMemImportShowable(dir)
}

// museDirOK says whether a name may be taken for a directory of muse's personal_project store.
func museDirOK(dir string) bool {
	// The pattern already bounds the length (a slug of a deep path outgrows the 100 characters
	// a file name may show) and allows no control character.
	return museDirRe.MatchString(dir) && !strings.Contains(dir, "..") && agentMemCleanText(dir)
}

// museWalk opens segs below fd one component at a time with O_NOFOLLOW and consumes fd.
func museWalk(fd int, segs ...string) (int, error) {
	flags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	for _, seg := range segs {
		next, err := syscall.Openat(fd, seg, flags, 0)
		_ = syscall.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// museOpenUnder opens base (the member's own home path, symlinks followed) and then segs without
// following one.
func museOpenUnder(base string, segs ...string) (*os.File, bool) {
	fd, err := syscall.Open(base, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, false
	}
	fd, err = museWalk(fd, segs...)
	if err != nil {
		return nil, false
	}
	return os.NewFile(uintptr(fd), "muse-memory"), true
}

// museDataBase is the directory that holds muse's own "muse" data directory: an absolute
// $XDG_DATA_HOME, else ~/.local/share (muse 1.4.4's embedded help names both roots). It is the
// member's own path, so a symlink in it (a persisted home) is followed; everything below it is not.
func museDataBase() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Clean(x)
	}
	return filepath.Dir(muse.DataHome())
}

// museStoreOpen opens a directory of muse's personal stores (muse/memory/<segs>), every component
// from "muse" down without following a symlink.
func museStoreOpen(segs ...string) (*os.File, bool) {
	return museOpenUnder(museDataBase(), append([]string{"muse", "memory"}, segs...)...)
}

// agentMemMuseOpenWC opens <repos>/<dir> without following a symlink, the working copy included:
// a symlink under ~/repos could otherwise point outside it.
func agentMemMuseOpenWC(dir string) (*os.File, bool) {
	return museOpenUnder(gitx.ReposRoot(), dir)
}

// agentMemMuseOpen opens <repos>/<dir>/.agents/memory the same way. The caller reads every file
// relative to mem, and runs the project mapping and git through wc, so nothing is swapped between
// a check and a use. Both handles are the caller's to close.
func agentMemMuseOpen(dir string) (mem, wc *os.File, ok bool) {
	wc, ok = agentMemMuseOpenWC(dir)
	if !ok {
		return nil, nil, false
	}
	// A second descriptor: the walk consumes it.
	dup, err := syscall.Dup(int(wc.Fd()))
	if err != nil {
		wc.Close()
		return nil, nil, false
	}
	syscall.CloseOnExec(dup)
	fd, err := museWalk(dup, strings.Split(agentMemMuseSubdir, "/")...)
	if err != nil {
		wc.Close()
		return nil, nil, false
	}
	return os.NewFile(uintptr(fd), "muse-memory"), wc, true
}

// agentMemMuseFiles lists the notes of a scope root and below, as paths relative to it with "/".
// Every directory is opened through its parent's handle with O_NOFOLLOW, so a symlinked directory
// is not entered; hidden directories are skipped, and so is everything that is not
// a .md file (.muse-memory.lock included). A symlink named *.md is listed and refused on open.
// MEMORY.md is listed at the scope root only, and only when it holds more than index links.
func agentMemMuseFiles(root *os.File) (files []string, truncated bool) {
	var walk func(dir *os.File, prefix string, depth int)
	walk = func(dir *os.File, prefix string, depth int) {
		ents, err := dir.ReadDir(-1)
		if err != nil {
			return
		}
		sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
		for _, d := range ents {
			if truncated {
				return
			}
			n := d.Name()
			switch {
			case d.IsDir():
				if strings.HasPrefix(n, ".") {
					continue
				}
				if depth >= museMaxDepth {
					truncated = true
					return
				}
				fd, err := museWalk(mustDup(dir), n)
				if err != nil {
					continue
				}
				sub := os.NewFile(uintptr(fd), "muse-memory-sub")
				walk(sub, prefix+n+"/", depth+1)
				sub.Close()
			case strings.HasSuffix(n, ".md"):
				if n == agentMemImportIndex && (depth > 0 || !museIndexHasFacts(dir)) {
					continue
				}
				if len(files) >= agentMemImportMaxFiles {
					truncated = true
					return
				}
				files = append(files, prefix+n)
			}
		}
	}
	walk(root, "", 0)
	return files, truncated
}

// mustDup returns a descriptor for dir that the caller consumes; -1 (which makes the next openat
// fail) when it cannot be had.
func mustDup(dir *os.File) int {
	fd, err := syscall.Dup(int(dir.Fd()))
	if err != nil {
		return -1
	}
	syscall.CloseOnExec(fd)
	return fd
}

// agentMemMuseDerive reads a note that has no frontmatter: the whole text is the body and the
// first non-empty line, cut to AF's limit, is the description.
func agentMemMuseDerive(raw []byte) agentMemEntry {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	var e agentMemEntry
	e.Body = text
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			e.Description = line
			break
		}
	}
	if utf8.RuneCountInString(e.Description) > agentMemMaxDescription {
		r := []rune(e.Description)
		e.Description = strings.TrimRight(string(r[:agentMemMaxDescription-1]), " ") + "…"
	}
	return e
}

// agentMemMuseFdPath names the opened working copy for a child process (chdir into it) and for
// path checks. It follows the descriptor, not the working copy's current path.
func agentMemMuseFdPath(wc *os.File) string {
	return fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), wc.Fd())
}

func agentMemMuseGit(wc *os.File, args ...string) *exec.Cmd {
	cmd := gitx.Cmd("", args...)
	cmd.Dir = agentMemMuseFdPath(wc)
	return cmd
}

// agentMemMuseTracked returns the file names git tracks under .agents/memory of a working copy.
// A directory that is not a git repository tracks nothing. When git cannot say, every file counts
// as tracked: the warning errs toward "shared".
func agentMemMuseTracked(wc *os.File, files []string) map[string]bool {
	out := map[string]bool{}
	if projcfg.DetectVCS(agentMemMuseFdPath(wc)) != projcfg.VCSGit {
		return out
	}
	// The repository's own config is the member's, but a cloned repo's is not: core.fsmonitor runs
	// an arbitrary command for ls-files, so it is forced off (measured: a marker script ran on
	// every preview without it).
	b, err := agentMemMuseGit(wc, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
		"ls-files", "-z", "--", agentMemMuseSubdir).Output()
	if err != nil {
		for _, f := range files {
			out[f] = true
		}
		return out
	}
	for _, p := range strings.Split(string(b), "\x00") {
		if n, ok := strings.CutPrefix(p, agentMemMuseSubdir+"/"); ok {
			out[n] = true
		}
	}
	return out
}

// agentMemMuseProject maps the opened working copy to its AF project; reason says why it has
// none. The directory name only labels it: git answers about the descriptor's own directory.
func agentMemMuseProject(dir string, wc *os.File) (*agentMemProject, string) {
	p := agentMemProjectWith(filepath.Join(gitx.ReposRoot(), dir), agentMemMuseFdPath(wc),
		func(args ...string) (string, error) {
			out, err := agentMemMuseGit(wc, args...).Output()
			return strings.TrimSpace(string(out)), err
		})
	switch {
	case p == nil:
		return nil, claudeImportNoProject
	case !agentMemCleanText(p.Root + "\n" + p.Display + "\n" + p.ID):
		return nil, claudeImportNoProject
	}
	return p, ""
}

// museFnv is the 16-hex suffix of a personal_project directory: FNV-1a 64 of the absolute cwd.
func museFnv(path string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	return fmt.Sprintf("%016x", h.Sum64())
}

// museSlugNorm reduces a path or a directory's slug to lower-case letters and digits with "-"
// between; only used to compare, because the exact slug rule was measured on one path only.
func museSlugNorm(s string) string {
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return strings.Trim(string(b), "-")
}

// agentMemMuseMapDir maps a personal_project directory to the AF project of the working copy under
// ~/repos it belongs to. The hash names the working copy exactly; failing that, the slug is
// compared with every working copy's path (the cwd may have been a subdirectory, or the
// directory moved), and two different projects fitting is "ambiguous".
func agentMemMuseMapDir(dir string) (*agentMemProject, string) {
	slug, sum := dir, ""
	if m := museHashSuffixRe.FindStringSubmatch(dir); m != nil {
		slug, sum = m[1], m[2]
	}
	ents, err := os.ReadDir(gitx.ReposRoot())
	if err != nil {
		return nil, claudeImportNoProject
	}
	roots := []string{gitx.ReposRoot()}
	if r, err := filepath.EvalSymlinks(gitx.ReposRoot()); err == nil && r != roots[0] {
		roots = append(roots, r)
	}
	var exact, near []string
	want := museSlugNorm(slug)
	for _, d := range ents {
		if !d.IsDir() || !agentMemMuseDirOK(d.Name()) {
			continue
		}
		hit := false
		for _, r := range roots {
			p := filepath.Join(r, d.Name())
			if sum != "" && museFnv(p) == sum {
				hit = true
			}
			if w := museSlugNorm(p); w == want || strings.HasPrefix(want, w+"-") {
				near = append(near, d.Name())
				break
			}
		}
		if hit {
			exact = append(exact, d.Name())
		}
	}
	pick := exact
	if len(pick) == 0 {
		pick = near
	}
	seen := map[string]*agentMemProject{}
	for _, name := range pick {
		wc, ok := agentMemMuseOpenWC(name)
		if !ok {
			continue
		}
		p, _ := agentMemMuseProject(name, wc)
		wc.Close()
		if p != nil {
			seen[p.ID] = p
		}
	}
	switch len(seen) {
	case 0:
		return nil, claudeImportNoProject
	case 1:
		for _, p := range seen {
			return p, ""
		}
	}
	return nil, claudeImportAmbiguous
}

// agentMemMuseList returns the muse directories that hold notes.
func agentMemMuseList() agentMemMuseSources {
	out := agentMemMuseSources{Sources: []agentMemMuseSource{}}
	if ents, err := os.ReadDir(gitx.ReposRoot()); err == nil {
		for _, d := range ents {
			mem, wc, ok := agentMemMuseOpen(d.Name())
			if !ok {
				continue
			}
			files, _ := agentMemMuseFiles(mem)
			mem.Close()
			if len(files) == 0 || !agentMemMuseDirOK(d.Name()) {
				wc.Close()
				if len(files) > 0 {
					out.Withheld++
				}
				continue
			}
			s := agentMemMuseSource{Scope: museScopeProject, Dir: d.Name(), Count: len(files)}
			listed := map[string]bool{}
			for _, f := range files {
				listed[f] = true
			}
			for f := range agentMemMuseTracked(wc, files) {
				if listed[f] {
					s.Shared++
				}
			}
			s.Project, s.Reason = agentMemMuseProject(d.Name(), wc)
			wc.Close()
			out.Sources = append(out.Sources, s)
		}
	}
	if pd, ok := museStoreOpen("projects"); ok {
		ents, _ := pd.ReadDir(-1)
		pd.Close()
		for _, d := range ents {
			mem, ok := museStoreOpen("projects", d.Name())
			if !ok {
				continue
			}
			files, _ := agentMemMuseFiles(mem)
			mem.Close()
			if len(files) == 0 {
				continue
			}
			if !museDirOK(d.Name()) {
				out.Withheld++
				continue
			}
			s := agentMemMuseSource{Scope: museScopePersonalProject, Dir: d.Name(), Count: len(files)}
			s.Project, s.Reason = agentMemMuseMapDir(d.Name())
			out.Sources = append(out.Sources, s)
		}
	}
	if mem, ok := museStoreOpen("personal"); ok {
		files, _ := agentMemMuseFiles(mem)
		mem.Close()
		if len(files) > 0 {
			out.Sources = append(out.Sources, agentMemMuseSource{Scope: museScopePersonal, Count: len(files), Target: museIntoUser})
		}
	}
	rank := map[string]int{museScopeProject: 0, museScopePersonalProject: 1, museScopePersonal: 2}
	sort.SliceStable(out.Sources, func(i, j int) bool {
		a, b := out.Sources[i], out.Sources[j]
		if a.Scope != b.Scope {
			return rank[a.Scope] < rank[b.Scope]
		}
		return a.Dir < b.Dir
	})
	return out
}

// agentMemMuseBuild evaluates every note of one muse directory. Callers hold agentMemMu. into is
// only read for the personal scope.
func agentMemMuseBuild(scope, dir, into string) (*agentMemMusePreview, error) {
	pv := &agentMemMusePreview{Scope: scope, Dir: dir, Target: "project", Items: []agentMemImportItem{},
		Counts: map[string]int{}, byName: map[string]*agentMemImportItem{}}
	var (
		mem, wc *os.File
		ok      bool
		rel     string
		src     func(file string) agentMemImportSrc
		tracked map[string]bool
	)
	// The name is never echoed: a directory name can be a token, and the list withholds those.
	switch scope {
	case museScopeProject, "":
		if !agentMemMuseDirOK(dir) {
			return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "dir is not a working copy name")
		}
		pv.Scope = museScopeProject
		if mem, wc, ok = agentMemMuseOpen(dir); !ok {
			return nil, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no muse project memory in that working copy")
		}
		defer wc.Close()
		pv.Project, pv.Reason = agentMemMuseProject(dir, wc)
	case museScopePersonalProject:
		if !museDirOK(dir) {
			return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "dir is not a muse project directory name")
		}
		if mem, ok = museStoreOpen("projects", dir); !ok {
			return nil, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no muse personal project memory in that directory")
		}
		pv.Project, pv.Reason = agentMemMuseMapDir(dir)
	case museScopePersonal:
		if dir != "" {
			return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "the personal scope has no dir")
		}
		if mem, ok = museStoreOpen("personal"); !ok {
			return nil, memoryErrf(http.StatusNotFound, errCodeMemoryNotFound, "no muse personal memory")
		}
		pv.Target = museIntoUser
		if into != museIntoUser {
			// It spreads to every project: nothing is evaluated until the caller has said so.
			pv.Reason = museImportChooseUser
		}
	default:
		return nil, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest, "scope must be project, personal_project or personal")
	}
	defer mem.Close()
	switch pv.Scope {
	case museScopePersonal:
		if pv.Reason != "" {
			return pv, nil
		}
		rel = "user"
		src = func(f string) agentMemImportSrc {
			return agentMemImportSrc{Source: agentMemMuseSourcePrefix + "personal/" + f, Muse: true, Scope: agentMemScopeUser}
		}
	default:
		if pv.Project == nil {
			return pv, nil
		}
		rel = "projects/" + pv.Project.ID
		if pv.Scope == museScopeProject {
			src = func(f string) agentMemImportSrc {
				return agentMemImportSrc{Source: agentMemMuseSourcePrefix + "project/" + agentMemMuseSubdir + "/" + f,
					Muse: true, Shared: tracked[f]}
			}
		} else {
			src = func(f string) agentMemImportSrc {
				// The directory is part of the origin: a worktree's folder and its parent's hold
				// different notes of the same file name.
				return agentMemImportSrc{Source: agentMemMuseSourcePrefix + "personal_project/" + dir + "/" + f, Muse: true}
			}
		}
	}
	history, err := agentMemImportHistory(rel)
	if err != nil {
		return nil, err
	}
	files, trunc := agentMemMuseFiles(mem)
	pv.Truncate = trunc
	if pv.Scope == museScopeProject {
		tracked = agentMemMuseTracked(wc, files)
	}
	gone, err := agentMemMuseForgottenHashes(rel)
	if err != nil {
		return nil, err
	}
	// Two files that map to one name (a/b.md and a--b.md) cannot both be that memory.
	names := map[string]int{}
	for _, f := range files {
		names[museName(f)]++
	}
	for _, f := range files {
		var it agentMemImportItem
		var withheld bool
		if names[museName(f)] > 1 && agentMemImportShowable(museName(f)) {
			it = agentMemImportItem{Name: museName(f), Path: f, Status: claudeImportInvalid, Reason: "name_collision"}
		} else {
			s := src(f)
			if f == agentMemImportIndex {
				s.Name, s.Transform = museIndexNoteName, museIndexFacts
			}
			s.ForgottenHashes = gone
			it, withheld = agentMemImportEvaluateSrc(mem, f, s, rel, history)
		}
		if withheld {
			pv.Withheld++
			continue
		}
		pv.Counts[it.Status]++
		pv.Items = append(pv.Items, it)
	}
	sort.SliceStable(pv.Items, func(i, j int) bool {
		a, b := pv.Items[i], pv.Items[j]
		if claudeImportOrder[a.Status] != claudeImportOrder[b.Status] {
			return claudeImportOrder[a.Status] < claudeImportOrder[b.Status]
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Path < b.Path
	})
	for i := range pv.Items {
		pv.byName[pv.Items[i].Name] = &pv.Items[i]
	}
	return pv, nil
}

// museName is the AF memory name a note's path maps to.
func museName(file string) string {
	if file == agentMemImportIndex {
		return museIndexNoteName
	}
	return strings.ReplaceAll(strings.TrimSuffix(file, ".md"), "/", "--")
}

// agentMemMusePreviewFor is the read-only preview; it works with the switch off.
func agentMemMusePreviewFor(scope, dir, into string) (*agentMemMusePreview, error) {
	agentMemMu.RLock()
	defer agentMemMu.RUnlock()
	return agentMemMuseBuild(scope, dir, into)
}

type agentMemMuseReq struct {
	// Scope defaults to project. Into must be "user" for the personal scope and is refused for
	// the others; Project is the previewed project's id and must be empty for the user scope.
	Scope   string                  `json:"scope"`
	Project string                  `json:"project"`
	Dir     string                  `json:"dir"`
	Into    string                  `json:"into"`
	Items   []agentMemImportReqItem `json:"items"`
}

// agentMemMuseApply imports the listed items, one commit each. The caller has checked the switch.
// A directory cut at the file limit is refused: name uniqueness (a/b.md against a--b.md) cannot be
// proven for files that were not listed.
func agentMemMuseApply(req agentMemMuseReq, now time.Time) (agentMemImportApplied, error) {
	if len(req.Items) == 0 || len(req.Items) > agentMemImportMaxItems {
		return agentMemImportApplied{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
			"send between 1 and %d items per request", agentMemImportMaxItems)
	}
	if (req.Scope == museScopePersonal) != (req.Into == museIntoUser) && req.Into != "" {
		return agentMemImportApplied{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
			"into=user belongs to the personal scope only")
	}
	if req.Scope == museScopePersonal && req.Into != museIntoUser {
		return agentMemImportApplied{}, memoryErrf(http.StatusBadRequest, errCodeMemoryBadRequest,
			"the personal scope is imported into your user memory only when into=user is sent")
	}
	agentMemMu.Lock()
	defer agentMemMu.Unlock()
	memorySnapshotMu.Lock()
	defer memorySnapshotMu.Unlock()

	pv, err := agentMemMuseBuild(req.Scope, req.Dir, req.Into)
	if err != nil {
		return agentMemImportApplied{}, err
	}
	if pv.Truncate {
		return agentMemImportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"this muse directory has more notes than one import examines; remove some and preview it again")
	}
	if pv.Scope == museScopePersonal {
		if req.Project != "" {
			return agentMemImportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
				"the personal scope goes to the user scope, which has no project")
		}
		return agentMemImportApplyRel("user", nil, pv.byName, req.Items, now), nil
	}
	if pv.Project == nil {
		return agentMemImportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"this muse memory cannot be imported (%s)", pv.Reason)
	}
	if pv.Project.ID != req.Project {
		return agentMemImportApplied{}, memoryErrf(http.StatusConflict, errCodeMemoryConflict,
			"the project this muse directory maps to has changed; preview it again")
	}
	return agentMemImportApplyItems(pv.Project, pv.byName, req.Items, now), nil
}

// agentMemMuseForgottenHashes returns the source hashes this scope ever stored for a name that is
// no longer live (forgotten, or its import reverted). A forgotten memory is matched by name in
// agentMemImportHistory; this also catches the same file renamed in muse, whose bytes are
// unchanged. A file edited as well as renamed is a different text and is not caught.
func agentMemMuseForgottenHashes(rel string) (map[string]bool, error) {
	out := map[string]bool{}
	if !memoryHasCommits() {
		return out, nil
	}
	log, err := memoryGitRun("log", memoryBranch, "--no-renames", "-p", "--format=", "-U0",
		"--", agentMemRepoPrefix+"/"+rel)
	if err != nil {
		return nil, err
	}
	prefix := agentMemRepoPrefix + "/" + rel + "/"
	byName := map[string][]string{}
	name := ""
	for _, line := range strings.Split(log, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"), strings.HasPrefix(line, "--- a/"):
			name = ""
			if n, ok := strings.CutPrefix(line[6:], prefix); ok {
				if n, ok = strings.CutSuffix(n, ".md"); ok && agentMemValidName(n) {
					name = n
				}
			}
		case name != "" && (strings.HasPrefix(line, "+source_hash:") || strings.HasPrefix(line, "-source_hash:")):
			h := strings.Trim(strings.TrimSpace(line[len("+source_hash:"):]), `"`)
			byName[name] = append(byName[name], h)
		}
	}
	dir, err := agentMemCheckDir(rel, false)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for n, hs := range byName {
		if err == nil {
			if _, serr := os.Stat(filepath.Join(dir, n+".md")); serr == nil {
				continue
			}
		}
		for _, h := range hs {
			if h != "" {
				out[h] = true
			}
		}
	}
	return out, nil
}
