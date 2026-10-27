package sessionx

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The Repository "Skills" view (#2795): what each kind would discover in a working copy, and in
// a Subdir of it. It answers on the personal catalog's routes with ?repo=&subdir=, reusing its
// entry shape, shadowing and file rules. The roots come from projectSkillRoots, the same table
// the composer picker scans, so the two cannot drift.
//
// Unlike the personal view, a root here can be reached through a symlink planted in the
// repository (it is a cloned, partly untrusted tree). Every file has to resolve inside the
// working copy; a levels-above root (the "user" level of a CWD chain) inside its own directory.

// repoSkillKinds is the order the view lists kinds in.
var repoSkillKinds = []string{session.KindClaude, session.KindCodex, session.KindOpencode, session.KindCursor, session.KindMuse}

type repoSkillCatalogWire struct {
	Repo   string        `json:"repo"`
	Subdir string        `json:"subdir"`
	Kinds  []catalogKind `json:"kinds"`
}

// repoScope is one request's view of a working copy. wc is the working copy opened ONCE, from
// the repos root, without following a link out of it; every later read goes through that handle,
// so renaming the folder away and putting a link in its place cannot move the fence.
type repoScope struct {
	dir, cwd, sub string
	wc            *os.Root
}

func (sc *repoScope) close() { _ = sc.wc.Close() }

// beforeRepoOpen lets a test swap the repository folder between name validation and the open.
var beforeRepoOpen func()

// resolveRepoSkillScope validates ?repo= and ?subdir= like HandleRepoSkills, and in addition
// refuses a Subdir that does not exist: the picker may fall back to the root, but a view whose
// point is "what does this folder change" must not silently answer for another one. A link in
// ~/repos (alias -> $HOME) and a folder with no .git/.svn are no working copy.
func resolveRepoSkillScope(w http.ResponseWriter, r *http.Request) (*repoScope, bool) {
	name := r.URL.Query().Get("repo")
	dir, ok := gitx.ResolveRepoDir(name)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_repo", "invalid repo name")
		return nil, false
	}
	sub, ok := session.CleanSubdir(r.URL.Query().Get("subdir"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_subdir", "subdir must be a relative path inside the working copy")
		return nil, false
	}
	notFound := func() (*repoScope, bool) {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such repo: "+name)
		return nil, false
	}
	repos, err := os.OpenRoot(gitx.ReposRoot())
	if err != nil {
		return notFound()
	}
	defer repos.Close()
	if beforeRepoOpen != nil {
		beforeRepoOpen()
	}
	wc, err := repos.OpenRoot(name)
	if err != nil {
		return notFound()
	}
	if fi, err := repos.Lstat(name); err != nil || !fi.IsDir() || !wcHasMarker(wc) {
		_ = wc.Close()
		return notFound()
	}
	sc := &repoScope{dir: dir, cwd: dir, sub: sub, wc: wc}
	if sub != "" {
		fi, err := wc.Stat(filepath.FromSlash(sub))
		switch {
		case err != nil && !os.IsNotExist(err):
			sc.close()
			httpx.WriteErr(w, http.StatusBadRequest, "bad_subdir", "subdir must be a relative path inside the working copy")
			return nil, false
		case err != nil || !fi.IsDir():
			sc.close()
			httpx.WriteErr(w, http.StatusNotFound, "no_subdir", "no such subdir: "+sub)
			return nil, false
		}
		sc.cwd = filepath.Join(dir, filepath.FromSlash(sub))
	}
	return sc, true
}

// wcHasMarker: a git or svn working copy, by its marker.
func wcHasMarker(wc *os.Root) bool {
	if fi, err := wc.Stat(".git"); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
		return true
	}
	fi, err := wc.Stat(".svn")
	return err == nil && fi.IsDir()
}

// repoCatalogRoots is one kind's repository roots as catalog roots, each fenced.
func repoCatalogRoots(kind string, sc *repoScope) []catalogRoot {
	dir := sc.dir
	roots := []catalogRoot{}
	for _, sr := range projectSkillRoots(kind, sc.cwd, dir) {
		rel, err := filepath.Rel(dir, sr.dir)
		if err != nil {
			continue
		}
		cr := catalogRoot{Dir: sr.dir, Scope: sr.source, Form: sr.form, Rel: filepath.ToSlash(rel), fence: dir, fenceRoot: sc.wc}
		if !under(dir, sr.dir) {
			// A level above the working copy: fenced at the folder that holds the .claude /
			// .codex / .agents directory, so a link at the root itself cannot lead out either.
			cr.fence, cr.fenceRoot = filepath.Dir(filepath.Dir(sr.dir)), nil
		}
		roots = append(roots, cr)
	}
	return roots
}

func repoSkillCatalog(sc *repoScope) []catalogKind {
	dir := sc.dir
	// Personal names per kind, keyed like the shadowing keys, for the "also in your personal
	// skills" link. The bundled-skill probe is skipped: those are not personal.
	personal := map[string]map[string]bool{}
	for _, k := range personalSkillRoots() {
		ck, _ := fillCatalogKind(k.kind, k.roots)
		names := map[string]bool{}
		for _, s := range ck.Skills {
			if s.Scope == "user" {
				names[shadowKey(k.kind, s)] = true
			}
		}
		personal[k.kind] = names
	}
	out := []catalogKind{}
	for _, kind := range repoSkillKinds {
		ck, _ := fillCatalogKind(kind, repoCatalogRoots(kind, sc))
		for i := range ck.Skills {
			s := &ck.Skills[i]
			if rel, err := filepath.Rel(dir, s.Path); err == nil {
				s.Rel = filepath.ToSlash(rel)
			}
			s.AlsoPersonal = personal[kind][shadowKey(kind, *s)]
		}
		sort.SliceStable(ck.Skills, func(i, j int) bool { return ck.Skills[i].Name < ck.Skills[j].Name })
		out = append(out, ck)
	}
	return out
}

func handleRepoSkillCatalog(w http.ResponseWriter, r *http.Request) {
	sc, ok := resolveRepoSkillScope(w, r)
	if !ok {
		return
	}
	defer sc.close()
	httpx.WriteJSON(w, http.StatusOK, repoSkillCatalogWire{Repo: r.URL.Query().Get("repo"), Subdir: sc.sub, Kinds: repoSkillCatalog(sc)})
}

// handleRepoSkillFile serves one file by its working-copy-relative path. It is accepted only
// when it has the shape of a skill file of a root listed for this (repo, subdir) AND resolves,
// after symlinks, inside that root's fence.
func handleRepoSkillFile(w http.ResponseWriter, r *http.Request) {
	sc, ok := resolveRepoSkillScope(w, r)
	if !ok {
		return
	}
	defer sc.close()
	rel := r.URL.Query().Get("path")
	if rel == "" || filepath.IsAbs(rel) {
		httpx.WriteErr(w, http.StatusForbidden, "not_a_skill_file", "path is not inside a skills folder")
		return
	}
	full := filepath.Join(sc.dir, filepath.FromSlash(rel))
	for _, kind := range repoSkillKinds {
		for _, root := range repoCatalogRoots(kind, sc) {
			if !root.lexicallyHolds(full) {
				continue
			}
			// Opened beneath the fence in one step, so a link swapped in after the shape check
			// still cannot lead out.
			sf, err := root.openFS()
			if err != nil {
				continue
			}
			f, err := sf.open(full)
			if err != nil {
				sf.close()
				continue
			}
			serveSkillFile(w, f, rel)
			_ = f.Close()
			sf.close()
			return
		}
	}
	httpx.WriteErr(w, http.StatusForbidden, "not_a_skill_file", "path is not inside a skills folder")
}

// shadowKey is the name a kind's CLI resolves an entry by: one namespace for claude, skills and
// commands apart for the rest.
func shadowKey(kind string, s catalogSkill) string {
	if kind == session.KindClaude {
		return s.Name
	}
	return s.Type + ":" + s.Name
}
