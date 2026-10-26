package sessionx

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetskills"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/pathguard"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The Settings "Skills" view (#2792): every kind's PERSONAL skills — where each lives, which
// copy wins, and what it says. The picker's list (session_skills.go) answers "what can I run in
// this session"; this one answers "what have I set up, and where", so it keeps what the picker
// drops: the root, the shadowed duplicates, `user-invocable: false` and the fleet skills.
//
// The roots are the user-level halves of the picker's own tables (claudeSkills, codexSkills,
// opencodeSkills, userSkillRoots) plus opencode's skills root, where fleetskills writes. A kind
// with no user root of its own (cursor, agy, kiro, copilot, lcpp) is not listed. Repository
// roots are #2795.
//
// The Files viewer cannot show these: $CLAUDE_CONFIG_DIR is outside the browse root and
// ~/.codex is denylisted, because credentials and transcripts sit next to the skills. So the
// file half (HandleSkillFile) serves only what lies inside one of these roots, never their
// parents.

// catalogRoot is one directory a kind reads personal skills from.
type catalogRoot struct {
	Dir    string `json:"dir"`
	Scope  string `json:"scope"` // user | cli
	Form   string `json:"form"`  // skills (<dir>/*/SKILL.md) | commands (<dir>/**/*.md)
	Exists bool   `json:"exists"`
}

// catalogSkill is one entry. Path and Root are empty for a CLI-bundled skill that is no file
// anywhere (claude's, see claude.BundledSkills).
type catalogSkill struct {
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	ArgumentHint  string `json:"argumentHint,omitempty"`
	Type          string `json:"type"`  // skill | command
	Scope         string `json:"scope"` // user | fleet | cli
	Root          string `json:"root,omitempty"`
	Path          string `json:"path,omitempty"`
	UserInvocable bool   `json:"userInvocable"`
	// ShadowedBy is the Path (or, for a bundled winner, the name) of the entry the CLI uses
	// instead: within a kind the first root that has the name wins, as in scanSkillRoots.
	ShadowedBy string `json:"shadowedBy,omitempty"`
	// Files are the other files of a skill folder, relative to it (reference docs, scripts).
	Files []string `json:"files,omitempty"`
}

type catalogKind struct {
	Kind   string         `json:"kind"`
	Roots  []catalogRoot  `json:"roots"`
	Skills []catalogSkill `json:"skills"`
}

const (
	maxCatalogFiles   = 100       // per skill folder
	maxSkillFileBytes = 256 << 10 // one file served by HandleSkillFile
	maxCatalogPerKind = maxSessionSkills
)

// personalSkillRoots lists each kind's roots in the CLI's own precedence order — the order
// shadowing is decided in.
func personalSkillRoots() []struct {
	kind  string
	roots []catalogRoot
} {
	cdir := claude.ConfigDir()
	chome := paths.CodexHome()
	ocfg := paths.OpencodeConfigDir()
	return []struct {
		kind  string
		roots []catalogRoot
	}{
		{session.KindClaude, []catalogRoot{
			{Dir: filepath.Join(cdir, "skills"), Scope: "user", Form: "skills"},
			{Dir: filepath.Join(cdir, "commands"), Scope: "user", Form: "commands"},
		}},
		{session.KindCodex, []catalogRoot{
			{Dir: filepath.Join(chome, "skills"), Scope: "user", Form: "skills"},
			{Dir: filepath.Join(chome, "skills", ".system"), Scope: "cli", Form: "skills"},
		}},
		{session.KindOpencode, []catalogRoot{
			{Dir: filepath.Join(ocfg, "skills"), Scope: "user", Form: "skills"},
			{Dir: filepath.Join(ocfg, "command"), Scope: "user", Form: "commands"},
			{Dir: filepath.Join(ocfg, "commands"), Scope: "user", Form: "commands"},
		}},
		{session.KindMuse, []catalogRoot{
			{Dir: muse.SkillsDir(), Scope: "user", Form: "skills"},
		}},
	}
}

type skillCatalogWire struct {
	Kinds []catalogKind `json:"kinds"`
}

type skillFileWire struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int    `json:"size"`
}

func HandleSkillCatalog(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, skillCatalogWire{Kinds: skillCatalog()})
}

func skillCatalog() []catalogKind {
	out := []catalogKind{}
	for _, k := range personalSkillRoots() {
		ck := catalogKind{Kind: k.kind, Roots: []catalogRoot{}, Skills: []catalogSkill{}}
		winner := map[string]string{}
		for _, root := range k.roots {
			fi, err := os.Stat(root.Dir)
			root.Exists = err == nil && fi.IsDir()
			ck.Roots = append(ck.Roots, root)
			if !root.Exists {
				continue
			}
			var items []catalogSkill
			if root.Form == "skills" {
				items = catalogSkillEntries(root)
			} else {
				items = catalogCommandEntries(root)
			}
			for _, it := range items {
				if len(ck.Skills) >= maxCatalogPerKind {
					break
				}
				if w, ok := winner[it.Name]; ok {
					it.ShadowedBy = w
				} else {
					winner[it.Name] = it.Path
				}
				ck.Skills = append(ck.Skills, it)
			}
		}
		if k.kind == session.KindClaude {
			for _, nm := range claudeBundledSkills() {
				if nm == "" || len(ck.Skills) >= maxCatalogPerKind {
					continue
				}
				// The init frame mixes the user's own skills into the bundled list; a name
				// already found on disk is that file, not a second entry.
				if _, ok := winner[nm]; ok {
					continue
				}
				winner[nm] = nm
				ck.Skills = append(ck.Skills, catalogSkill{Name: nm, Type: "skill", Scope: "cli", UserInvocable: true})
			}
		}
		sort.SliceStable(ck.Skills, func(i, j int) bool { return ck.Skills[i].Name < ck.Skills[j].Name })
		out = append(out, ck)
	}
	return out
}

// catalogSkillEntries reads <root>/*/SKILL.md like readSkillEntries, but keeps the entries the
// picker hides and marks AF's own fleet skills.
func catalogSkillEntries(root catalogRoot) []catalogSkill {
	ents, err := os.ReadDir(root.Dir)
	if err != nil {
		return nil
	}
	out := []catalogSkill{}
	for _, e := range ents {
		dir := filepath.Join(root.Dir, e.Name())
		// A skill folder may be a symlink (codex's .codex/skills bridge links are), so Stat
		// rather than trusting the dirent type.
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		p := filepath.Join(dir, "SKILL.md")
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		fm, _ := splitFrontmatter(string(b))
		nm := fm["name"]
		if nm == "" {
			nm = e.Name()
		}
		scope := root.Scope
		if strings.Contains(string(b), fleetskills.Marker) {
			scope = "fleet"
		}
		out = append(out, catalogSkill{
			Name:          nm,
			Description:   fm["description"],
			ArgumentHint:  fm["argument-hint"],
			Type:          "skill",
			Scope:         scope,
			Root:          root.Dir,
			Path:          p,
			UserInvocable: !isNo(fm["user-invocable"]),
			Files:         skillFolderFiles(dir),
		})
	}
	return out
}

func catalogCommandEntries(root catalogRoot) []catalogSkill {
	out := []catalogSkill{}
	for _, it := range readCommandEntriesWithPath(root.Dir) {
		out = append(out, catalogSkill{
			Name:          it.Name,
			Description:   it.Description,
			ArgumentHint:  it.ArgumentHint,
			Type:          "command",
			Scope:         root.Scope,
			Root:          root.Dir,
			Path:          it.Path,
			UserInvocable: true,
		})
	}
	return out
}

// readCommandEntriesWithPath is readCommandEntries with each file's path kept.
func readCommandEntriesWithPath(root string) []sessionSkill {
	out := []sessionSkill{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		meta, _ := splitFrontmatter(string(b))
		out = append(out, sessionSkill{
			Name:         strings.TrimSuffix(d.Name(), ".md"),
			Description:  meta["description"],
			ArgumentHint: meta["argument-hint"],
			Path:         p,
		})
		if len(out) >= maxSessionSkills {
			return filepath.SkipAll
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// skillFolderFiles lists a skill folder's regular files other than SKILL.md, relative to it.
// Hidden entries are skipped: a skill checked out with its .git would otherwise list hundreds.
func skillFolderFiles(dir string) []string {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil
	}
	out := []string{}
	_ = filepath.WalkDir(real, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p != real && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(real, p)
		if err != nil || rel == "SKILL.md" {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		if len(out) >= maxCatalogFiles {
			return filepath.SkipAll
		}
		return nil
	})
	return out
}

// HandleSkillFile serves one file of the catalog, read-only. The path has to sit inside a
// listed root both as written and after symlinks are resolved — a link may point into another
// listed root (codex's bridge links point at claude's skills) but never out of them, which is
// what keeps $CLAUDE_CONFIG_DIR/.credentials.json and ~/.codex/auth.json unreachable.
func HandleSkillFile(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	full, ok := skillFilePath(p)
	if !ok {
		httpx.WriteErr(w, http.StatusForbidden, "not_a_skill_file", "path is not inside a skills folder")
		return
	}
	fi, err := os.Stat(full)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such file")
		return
	}
	if !fi.Mode().IsRegular() {
		httpx.WriteErr(w, http.StatusForbidden, "not_a_skill_file", "not a regular file")
		return
	}
	if fi.Size() > maxSkillFileBytes {
		httpx.WriteErr(w, http.StatusRequestEntityTooLarge, "too_large", "file is too large to show")
		return
	}
	b, err := os.ReadFile(full)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "cannot read file")
		return
	}
	if !utf8.Valid(b) {
		httpx.WriteErr(w, http.StatusUnsupportedMediaType, "binary", "not a text file")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, skillFileWire{Path: p, Content: string(b), Size: len(b)})
}

// skillFilePath decides whether p may be served. Lexically it must be below a skills-form root
// by at least <root>/<skill>/<file>, or a .md below a commands-form root; the resolved file must
// then lie under some listed root. The root directory itself, or a loose file directly in a
// skills root, is not a skill file.
func skillFilePath(p string) (string, bool) {
	if p == "" || !filepath.IsAbs(p) {
		return "", false
	}
	p = filepath.Clean(p)
	var roots []catalogRoot
	for _, k := range personalSkillRoots() {
		roots = append(roots, k.roots...)
	}
	lexical := false
	for _, root := range roots {
		rel, err := filepath.Rel(root.Dir, p)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if root.Form == "skills" && strings.Contains(rel, string(filepath.Separator)) {
			lexical = true
		}
		if root.Form == "commands" && strings.HasSuffix(rel, ".md") {
			lexical = true
		}
	}
	if !lexical {
		return "", false
	}
	for _, root := range roots {
		if _, ok := pathguard.ResolveUnder(p, root.Dir); ok {
			return p, true
		}
	}
	return "", false
}
