package sessionx

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/pathguard"
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

// resolveRepoSkillScope validates ?repo= and ?subdir= like HandleRepoSkills, and in addition
// refuses a Subdir that does not exist: the picker may fall back to the root, but a view whose
// point is "what does this folder change" must not silently answer for another one.
func resolveRepoSkillScope(w http.ResponseWriter, r *http.Request) (dir, cwd, sub string, ok bool) {
	name := r.URL.Query().Get("repo")
	dir, ok = gitx.ResolveRepoDir(name)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_repo", "invalid repo name")
		return
	}
	// Lstat, not Stat: a link in ~/repos (alias -> $HOME) would make the target the "working
	// copy" and every fence below would then be drawn around the wrong tree. A plain folder is
	// no working copy either.
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() || !isWorkingCopy(dir) {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such repo: "+name)
		return "", "", "", false
	}
	sub, ok = session.CleanSubdir(r.URL.Query().Get("subdir"))
	if ok && sub != "" {
		_, ok = pathguard.ResolveUnder(filepath.Join(dir, filepath.FromSlash(sub)), dir)
	}
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_subdir", "subdir must be a relative path inside the working copy")
		return "", "", "", false
	}
	cwd = session.Meta{Dir: dir, Subdir: sub}.CWD()
	if sub != "" && cwd == dir {
		httpx.WriteErr(w, http.StatusNotFound, "no_subdir", "no such subdir: "+sub)
		return "", "", "", false
	}
	return dir, cwd, sub, true
}

// isWorkingCopy: a git or svn working copy, by its marker.
func isWorkingCopy(dir string) bool {
	if gitx.IsGitRepo(dir) {
		return true
	}
	fi, err := os.Stat(filepath.Join(dir, ".svn"))
	return err == nil && fi.IsDir()
}

// repoCatalogRoots is one kind's repository roots as catalog roots, each fenced.
func repoCatalogRoots(kind, cwd, dir string) []catalogRoot {
	roots := []catalogRoot{}
	for _, sr := range projectSkillRoots(kind, cwd, dir) {
		rel, err := filepath.Rel(dir, sr.dir)
		if err != nil {
			continue
		}
		fence := sr.dir
		if under(dir, sr.dir) {
			fence = dir
		}
		roots = append(roots, catalogRoot{Dir: sr.dir, Scope: sr.source, Form: sr.form, Rel: filepath.ToSlash(rel), fence: fence})
	}
	return roots
}

func repoSkillCatalog(dir, cwd string) []catalogKind {
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
		ck, _ := fillCatalogKind(kind, repoCatalogRoots(kind, cwd, dir))
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
	dir, cwd, sub, ok := resolveRepoSkillScope(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, repoSkillCatalogWire{Repo: r.URL.Query().Get("repo"), Subdir: sub, Kinds: repoSkillCatalog(dir, cwd)})
}

// handleRepoSkillFile serves one file by its working-copy-relative path. It is accepted only
// when it has the shape of a skill file of a root listed for this (repo, subdir) AND resolves,
// after symlinks, inside that root's fence.
func handleRepoSkillFile(w http.ResponseWriter, r *http.Request) {
	dir, cwd, _, ok := resolveRepoSkillScope(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" || filepath.IsAbs(rel) {
		httpx.WriteErr(w, http.StatusForbidden, "not_a_skill_file", "path is not inside a skills folder")
		return
	}
	full := filepath.Join(dir, filepath.FromSlash(rel))
	for _, kind := range repoSkillKinds {
		for _, root := range repoCatalogRoots(kind, cwd, dir) {
			if !root.lexicallyHolds(full) {
				continue
			}
			// Opened beneath the fence in one step, so a link swapped in after the shape check
			// still cannot lead out.
			f, err := openBeneath(root.fence, full)
			if err != nil {
				continue
			}
			defer f.Close()
			serveSkillFile(w, f, rel)
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
