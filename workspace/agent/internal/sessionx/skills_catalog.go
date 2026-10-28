package sessionx

import (
	"io"
	"io/fs"
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
// The roots are the user-level halves of the picker's own layouts (agents.SkillsLayout's User
// and Shared roots) plus opencode's skills root, where fleetskills writes. A kind
// with no user root of its own (cursor, agy, kiro, copilot, lcpp) is not listed. The
// repository's roots are the same shapes answered on ?repo= (skills_repo_catalog.go).
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
	// Rel is the root's path relative to the working copy (repository view only; "../" for a
	// level above it).
	Rel string `json:"rel,omitempty"`
	// fence, when set, is the directory every file of this root must resolve inside after
	// symlinks (repository view only). An entry that leaves it is listed without being read.
	fence string
	// fenceRoot, when set, is the already opened fence directory; the request holds it so the
	// fence cannot be renamed away and replaced by a link between the checks and the reads.
	fenceRoot *os.Root
	// opencode marks opencode's skills root, which is discovered by its own rules
	// (opencodeSkillFiles) rather than as a flat <dir>/*/SKILL.md tree.
	opencode bool
}

// catalogSkill is one entry. Path and Root are empty for a CLI-bundled skill that is no file
// anywhere (claude's, see claude.BundledSkills).
type catalogSkill struct {
	// Name is what the CLI identifies the entry by: the frontmatter name (or folder) for
	// claude/codex/muse, the path-derived ID for opencode, whose frontmatter name is only a label.
	Name string `json:"name"`
	// Title is opencode's frontmatter name when it differs from the ID.
	Title         string `json:"title,omitempty"`
	Description   string `json:"description,omitempty"`
	ArgumentHint  string `json:"argumentHint,omitempty"`
	Type          string `json:"type"`  // skill | command
	Scope         string `json:"scope"` // user | fleet | cli
	Root          string `json:"root,omitempty"`
	Path          string `json:"path,omitempty"`
	UserInvocable bool   `json:"userInvocable"`
	// Rel is Path relative to the working copy (repository view only).
	Rel string `json:"rel,omitempty"`
	// Outside marks an entry whose file resolves outside the root's fence (a symlink leaving the
	// working copy): it is listed by name only, never read or served.
	Outside bool `json:"outside,omitempty"`
	// AlsoPersonal says the same kind has a personal skill of this name (repository view only);
	// for claude and codex the repository copy wins.
	AlsoPersonal bool `json:"alsoPersonal,omitempty"`
	// ShadowedBy is the Path (or, for a bundled winner, the name) of the entry the CLI uses
	// instead: within a kind the first root that has the name wins, as in scanSkillRoots.
	// claude has one slash namespace for skills and commands; every other kind keeps them apart.
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
			{Dir: filepath.Join(ocfg, "skills"), Scope: "user", Form: "skills", opencode: true},
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
	if r.URL.Query().Has("repo") {
		handleRepoSkillCatalog(w, r)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, skillCatalogWire{Kinds: skillCatalog()})
}

func skillCatalog() []catalogKind {
	out := []catalogKind{}
	for _, k := range personalSkillRoots() {
		ck, winner := fillCatalogKind(k.kind, k.roots)
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

// fillCatalogKind reads one kind's roots in precedence order and marks the entries an earlier
// root already claimed. It is shared by the personal and the repository view. The returned map
// is the winner's Path by shadowing key.
func fillCatalogKind(kind string, roots []catalogRoot) (catalogKind, map[string]string) {
	ck := catalogKind{Kind: kind, Roots: []catalogRoot{}, Skills: []catalogSkill{}}
	winner := map[string]string{}
	for _, root := range roots {
		root.Exists = root.dirExists()
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
			key := shadowKey(kind, it)
			if w, ok := winner[key]; ok {
				it.ShadowedBy = w
			} else {
				winner[key] = it.Path
			}
			ck.Skills = append(ck.Skills, it)
		}
	}
	return ck, winner
}

// dirExists reports whether the root is a directory, resolved inside the fence when it has one.
func (r catalogRoot) dirExists() bool {
	sf, err := r.openFS()
	if err != nil {
		return false
	}
	defer sf.close()
	return sf.isDir(r.Dir)
}

// catalogSkillEntries reads <root>/*/SKILL.md like readSkillEntries, but keeps the entries the
// picker hides and marks AF's own fleet skills.
func catalogSkillEntries(root catalogRoot) []catalogSkill {
	if root.opencode {
		return opencodeCatalogEntries(root)
	}
	sf, err := root.openFS()
	if err != nil {
		return nil
	}
	defer sf.close()
	ents, err := sf.readDir(root.Dir)
	if err != nil {
		return nil
	}
	out := []catalogSkill{}
	for _, e := range ents {
		dir := filepath.Join(root.Dir, e.Name())
		// A skill folder may be a symlink (codex's .codex/skills bridge links are), so stat
		// rather than trusting the dirent type.
		if !sf.isDir(dir) {
			// A link that does not resolve inside the fence (out of the working copy, or an
			// absolute one): named, never read.
			if sf.fenced() && sf.isLink(dir) {
				out = append(out, catalogSkill{Name: e.Name(), Type: "skill", Scope: root.Scope, Root: root.Dir, Path: filepath.Join(dir, "SKILL.md"), UserInvocable: true, Outside: true})
			}
			continue
		}
		p := filepath.Join(dir, "SKILL.md")
		b, err := sf.read(p)
		if err != nil {
			// Present but not openable beneath the fence: a link leading out (an absolute link
			// counts, even to a place inside). Listed by name only.
			if sf.fenced() && sf.present(p) {
				out = append(out, catalogSkill{Name: e.Name(), Type: "skill", Scope: root.Scope, Root: root.Dir, Path: p, UserInvocable: true, Outside: true})
			}
			continue
		}
		fm, _ := splitFrontmatter(string(b))
		nm := fm["name"]
		if nm == "" {
			nm = e.Name()
		}
		scope := root.Scope
		// A repository's own SKILL.md may quote the marker; only the personal view tells the
		// files Agent Fleet wrote apart by it.
		if !sf.fenced() && strings.Contains(string(b), fleetskills.Marker) {
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
			Files:         sf.folderFiles(dir),
		})
	}
	return out
}

func catalogCommandEntries(root catalogRoot) []catalogSkill {
	sf, err := root.openFS()
	if err != nil {
		return []catalogSkill{}
	}
	defer sf.close()
	out := []catalogSkill{}
	base := sf.name(root.Dir)
	if base == "" {
		return out
	}
	_ = fs.WalkDir(sf.fsys, base, func(n string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		p := filepath.Join(root.Dir, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(n, base), "/")))
		name := strings.TrimSuffix(d.Name(), ".md")
		b, err := sf.read(p)
		if err != nil {
			if sf.fenced() && sf.present(p) {
				out = append(out, catalogSkill{Name: name, Type: "command", Scope: root.Scope, Root: root.Dir, Path: p, UserInvocable: true, Outside: true})
			}
			return nil
		}
		meta, _ := splitFrontmatter(string(b))
		out = append(out, catalogSkill{
			Name:          name,
			Description:   meta["description"],
			ArgumentHint:  meta["argument-hint"],
			Type:          "command",
			Scope:         root.Scope,
			Root:          root.Dir,
			Path:          p,
			UserInvocable: true,
		})
		if len(out) >= maxSessionSkills {
			return fs.SkipAll
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// opencodeSkillDepth is opencode's own limit on how deep below the root a SKILL.md is looked
// for (the binary's discovery walk, measured on 2.1.4).
const opencodeSkillDepth = 6

// opencodeIgnoredDirs are the directories opencode's walk never enters, besides hidden ones.
var opencodeIgnoredDirs = map[string]bool{"node_modules": true, ".git": true, ".venv": true, "venv": true, "__pycache__": true}

// opencodeSkillFiles mirrors opencode 2.x discovery (measured in the 2.1.4 binary): breadth
// first from the root; a directory holding SKILL.md yields that file and is not descended
// further; loose *.md files count only directly in the root; directories are followed up to
// opencodeSkillDepth; hidden entries are skipped.
func opencodeSkillFiles(root string) []string {
	type level struct {
		dir   string
		depth int
	}
	out := []string{}
	queue := []level{{root, 0}}
	for len(queue) > 0 && len(out) < maxSessionSkills {
		cur := queue[0]
		queue = queue[1:]
		ents, err := os.ReadDir(cur.dir)
		if err != nil {
			continue
		}
		var files, dirs []string
		hasSkill := false
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") || opencodeIgnoredDirs[e.Name()] {
				continue
			}
			p := filepath.Join(cur.dir, e.Name())
			fi, err := os.Stat(p) // follows links, as opencode's walk does
			if err != nil {
				continue
			}
			switch {
			case fi.Mode().IsRegular() && e.Name() == "SKILL.md":
				hasSkill = true
			case fi.Mode().IsRegular() && strings.HasSuffix(e.Name(), ".md"):
				files = append(files, p)
			case fi.IsDir():
				dirs = append(dirs, p)
			}
		}
		if hasSkill {
			out = append(out, filepath.Join(cur.dir, "SKILL.md"))
			continue
		}
		if cur.depth == 0 {
			out = append(out, files...)
		}
		if cur.depth < opencodeSkillDepth {
			for _, d := range dirs {
				queue = append(queue, level{d, cur.depth + 1})
			}
		}
	}
	sort.Strings(out)
	return out
}

// opencodeCatalogEntries lists opencode's skills root. The ID is the file name for a loose
// <root>/<id>.md and the folder name otherwise; the frontmatter name is a display label only.
func opencodeCatalogEntries(root catalogRoot) []catalogSkill {
	out := []catalogSkill{}
	for _, p := range opencodeSkillFiles(root.Dir) {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		fm, _ := splitFrontmatter(string(b))
		dir := filepath.Dir(p)
		id := filepath.Base(dir)
		var files []string
		if filepath.Base(p) == "SKILL.md" {
			files = skillFolderFiles(dir)
		} else {
			id = strings.TrimSuffix(filepath.Base(p), ".md")
		}
		scope := root.Scope
		if strings.Contains(string(b), fleetskills.Marker) {
			scope = "fleet"
		}
		title := fm["name"]
		if title == id {
			title = ""
		}
		out = append(out, catalogSkill{
			Name:          id,
			Title:         title,
			Description:   fm["description"],
			ArgumentHint:  fm["argument-hint"],
			Type:          "skill",
			Scope:         scope,
			Root:          root.Dir,
			Path:          p,
			UserInvocable: !isNo(fm["user-invocable"]),
			Files:         files,
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
	if r.URL.Query().Has("repo") {
		handleRepoSkillFile(w, r)
		return
	}
	p := r.URL.Query().Get("path")
	full, ok := skillFilePath(p)
	if !ok {
		httpx.WriteErr(w, http.StatusForbidden, "not_a_skill_file", "path is not inside a skills folder")
		return
	}
	f, err := os.Open(full)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such file")
		return
	}
	defer f.Close()
	serveSkillFile(w, f, p)
}

// serveSkillFile answers with an already opened, already authorised file under the caller's
// name for the path.
func serveSkillFile(w http.ResponseWriter, f fs.File, name string) {
	fi, err := f.Stat()
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
	b, err := io.ReadAll(io.LimitReader(f, maxSkillFileBytes+1))
	if err != nil || len(b) > maxSkillFileBytes {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "cannot read file")
		return
	}
	if !utf8.Valid(b) {
		httpx.WriteErr(w, http.StatusUnsupportedMediaType, "binary", "not a text file")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, skillFileWire{Path: name, Content: string(b), Size: len(b)})
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
		if root.lexicallyHolds(p) {
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

// lexicallyHolds reports whether the cleaned absolute p has the shape of a skill file of this
// root: <root>/<skill>/<file> for a skills root, a .md for a commands root (or opencode's loose
// <id>.md). The root directory itself and a loose file in a skills root do not.
func (r catalogRoot) lexicallyHolds(p string) bool {
	rel, err := filepath.Rel(r.Dir, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	switch {
	case r.Form == "skills" && strings.Contains(rel, string(filepath.Separator)):
		return true
	case r.opencode && strings.HasSuffix(rel, ".md"):
		return true
	case r.Form == "commands" && strings.HasSuffix(rel, ".md"):
		return true
	}
	return false
}
