package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetskills"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// repoSkillsEnv builds the #2795 acceptance fixture under $HOME/repos/demo and returns its dir.
func repoSkillsEnv(t *testing.T) (home, dir string) {
	t.Helper()
	home, cdir, _ := catalogEnv(t)
	dir = filepath.Join(home, "repos", "demo")
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main")
	writeFile(t, filepath.Join(dir, ".claude", "skills", "a", "SKILL.md"), "---\ndescription: root a\n---\n")
	writeFile(t, filepath.Join(dir, ".agents", "skills", "b", "SKILL.md"), "---\ndescription: b\n---\n")
	writeFile(t, filepath.Join(dir, ".codex", "skills", "a", "SKILL.md"), "---\ndescription: codex a\n---\n")
	writeFile(t, filepath.Join(dir, "pkg", ".claude", "skills", "a", "SKILL.md"), "---\ndescription: pkg a\n---\n")
	// A personal skill of the same name, for the link to the personal view.
	writeFile(t, filepath.Join(cdir, "skills", "a", "SKILL.md"), "---\ndescription: mine\n---\n")
	return home, dir
}

func getRepoCatalog(t *testing.T, query string) (int, repoSkillCatalogWire) {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleSkillCatalog(rec, httptest.NewRequest("GET", "/skills/catalog?"+query, nil))
	var w repoSkillCatalogWire
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &w); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, w
}

func repoKind(t *testing.T, w repoSkillCatalogWire, kind string) catalogKind {
	t.Helper()
	for _, k := range w.Kinds {
		if k.Kind == kind {
			return k
		}
	}
	t.Fatalf("kind %s missing", kind)
	return catalogKind{}
}

// entries maps "<rel>" to the entry, failing on a duplicate rel.
func entriesByRel(t *testing.T, k catalogKind) map[string]catalogSkill {
	t.Helper()
	out := map[string]catalogSkill{}
	for _, s := range k.Skills {
		if _, dup := out[s.Rel]; dup {
			t.Fatalf("%s: %s listed twice", k.Kind, s.Rel)
		}
		out[s.Rel] = s
	}
	return out
}

func TestRepoSkillCatalogAcceptanceFixture(t *testing.T) {
	_, _ = repoSkillsEnv(t)

	code, root := getRepoCatalog(t, "repo=demo")
	if code != http.StatusOK {
		t.Fatalf("root: %d", code)
	}
	cl := entriesByRel(t, repoKind(t, root, session.KindClaude))
	if len(cl) != 1 || cl[".claude/skills/a/SKILL.md"].Scope != "project" || cl[".claude/skills/a/SKILL.md"].ShadowedBy != "" {
		t.Errorf("claude at root = %#v", cl)
	}
	if !cl[".claude/skills/a/SKILL.md"].AlsoPersonal {
		t.Errorf("claude a should be flagged as also personal: %#v", cl)
	}
	cx := entriesByRel(t, repoKind(t, root, session.KindCodex))
	if len(cx) != 2 || cx[".codex/skills/a/SKILL.md"].ShadowedBy != "" || cx[".agents/skills/b/SKILL.md"].Name != "b" {
		t.Errorf("codex at root = %#v", cx)
	}
	if len(repoKind(t, root, session.KindOpencode).Skills) != 0 || len(repoKind(t, root, session.KindCursor).Skills) != 0 {
		t.Errorf("opencode/cursor should list nothing: %#v", root.Kinds)
	}
	mu := entriesByRel(t, repoKind(t, root, session.KindMuse))
	if len(mu) != 1 || mu[".agents/skills/b/SKILL.md"].Name != "b" {
		t.Errorf("muse at root = %#v", mu)
	}

	// With subdir pkg the nested claude copy wins and the root one is shadowed by it.
	code, sub := getRepoCatalog(t, "repo=demo&subdir=pkg")
	if code != http.StatusOK || sub.Subdir != "pkg" {
		t.Fatalf("pkg: %d %q", code, sub.Subdir)
	}
	cl = entriesByRel(t, repoKind(t, sub, session.KindClaude))
	nested, rootA := cl["pkg/.claude/skills/a/SKILL.md"], cl[".claude/skills/a/SKILL.md"]
	if nested.ShadowedBy != "" || rootA.ShadowedBy != nested.Path || nested.Path == "" {
		t.Errorf("claude at pkg: nested=%#v root=%#v", nested, rootA)
	}
	// codex stops at the git root and finds nothing under pkg of its own.
	if cx := entriesByRel(t, repoKind(t, sub, session.KindCodex)); len(cx) != 2 {
		t.Errorf("codex at pkg = %#v", cx)
	}
}

func TestRepoSkillCatalogMissingRootsCarryPathToCreate(t *testing.T) {
	_, _ = repoSkillsEnv(t)
	_, root := getRepoCatalog(t, "repo=demo")
	got := map[string]catalogRoot{}
	for _, r := range repoKind(t, root, session.KindOpencode).Roots {
		got[r.Rel] = r
	}
	for _, rel := range []string{".opencode/command", ".opencode/commands"} {
		if r, ok := got[rel]; !ok || r.Exists || r.Scope != "project" {
			t.Errorf("opencode root %s = %#v", rel, got)
		}
	}
	var present []string
	for _, r := range repoKind(t, root, session.KindClaude).Roots {
		if r.Exists {
			present = append(present, r.Rel)
		}
	}
	if len(present) != 1 || present[0] != ".claude/skills" {
		t.Errorf("claude present roots = %v", present)
	}
}

func TestRepoSkillCatalogRejectsBadScope(t *testing.T) {
	_, dir := repoSkillsEnv(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, ".claude", "skills", "x", "SKILL.md"), "x")
	if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int{
		"repo=..":                  http.StatusBadRequest,
		"repo=nope":                http.StatusNotFound,
		"repo=demo&subdir=..":      http.StatusBadRequest,
		"repo=demo&subdir=a/../..": http.StatusBadRequest,
		"repo=demo&subdir=/etc":    http.StatusBadRequest,
		"repo=demo&subdir=linked":  http.StatusBadRequest,
		"repo=demo&subdir=gone":    http.StatusNotFound,
	} {
		if code, _ := getRepoCatalog(t, q); code != want {
			t.Errorf("%s: %d, want %d", q, code, want)
		}
	}
}

func getRepoSkillFile(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleSkillFile(rec, httptest.NewRequest("GET", "/skills/file?"+query, nil))
	return rec
}

func TestRepoSkillFileStaysInsideTheWorkingCopy(t *testing.T) {
	home, dir := repoSkillsEnv(t)
	secret := filepath.Join(home, "secret.txt")
	writeFile(t, secret, "do not serve")
	writeFile(t, filepath.Join(dir, "notes.md"), "not in a skill root")
	// A file symlink and a folder symlink inside skill folders, both leaving the working copy,
	// and one relative link that stays inside it (a bridge to another root) and must keep working.
	// (Absolute links are refused even when they point inside: the open is confined to the tree.)
	if err := os.Symlink(secret, filepath.Join(dir, ".claude", "skills", "a", "leak.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(dir, ".claude", "skills", "home")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".claude", "commands", "ok.md"), "cmd")
	if err := os.Symlink(secret, filepath.Join(dir, ".claude", "commands", "bad.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../.agents/skills/b", filepath.Join(dir, ".codex", "skills", "b")); err != nil {
		t.Fatal(err)
	}

	q := func(p string) string { return "repo=demo&path=" + url.QueryEscape(p) }
	for _, ok := range []string{
		".claude/skills/a/SKILL.md",
		".claude/commands/ok.md",
		".codex/skills/b/SKILL.md",
	} {
		if rec := getRepoSkillFile(t, q(ok)); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", ok, rec.Code, rec.Body)
		}
	}
	if rec := getRepoSkillFile(t, q("pkg/.claude/skills/a/SKILL.md")+"&subdir=pkg"); rec.Code != http.StatusOK {
		t.Errorf("subdir read: %d", rec.Code)
	}
	for _, bad := range []string{
		".claude/skills/a/leak.md",       // symlink out of the working copy
		".claude/skills/home/secret.txt", // folder symlink out of it
		".claude/commands/bad.md",
		"notes.md",
		".claude/skills",
		".claude/skills/a/../../../secret.txt",
		"../secret.txt",
		secret,
		"",
	} {
		if rec := getRepoSkillFile(t, q(bad)); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Errorf("%q: status %d, want refused (%s)", bad, rec.Code, rec.Body)
		}
		if rec := getRepoSkillFile(t, q(bad)); rec.Code == http.StatusOK {
			t.Errorf("%q was served", bad)
		}
	}
	// The listing names an escaping entry but never reads it.
	_, root := getRepoCatalog(t, "repo=demo")
	cl := entriesByRel(t, repoKind(t, root, session.KindClaude))
	if h, ok := cl[".claude/skills/home/SKILL.md"]; !ok || !h.Outside || h.Description != "" {
		t.Errorf("escaping folder = %#v", h)
	}
	if b := cl[".claude/commands/bad.md"]; !b.Outside || b.Description != "" {
		t.Errorf("escaping command = %#v", b)
	}
	for _, f := range cl[".claude/skills/a/SKILL.md"].Files {
		if f == "leak.md" {
			t.Errorf("symlinked file listed among the skill's files: %v", cl[".claude/skills/a/SKILL.md"].Files)
		}
	}
}

// A link from a project skill folder to a SKILL.md elsewhere is refused even when the folder
// itself is inside the working copy.
func TestRepoSkillCatalogSkillMdSymlinkOut(t *testing.T) {
	home, dir := repoSkillsEnv(t)
	writeFile(t, filepath.Join(home, "elsewhere", "SKILL.md"), "---\ndescription: private\n---\n")
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "skills", "sneaky"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "elsewhere", "SKILL.md"), filepath.Join(dir, ".claude", "skills", "sneaky", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	_, root := getRepoCatalog(t, "repo=demo")
	s := entriesByRel(t, repoKind(t, root, session.KindClaude))[".claude/skills/sneaky/SKILL.md"]
	if !s.Outside || s.Description != "" {
		t.Errorf("sneaky = %#v", s)
	}
	if rec := getRepoSkillFile(t, "repo=demo&path="+url.QueryEscape(".claude/skills/sneaky/SKILL.md")); rec.Code != http.StatusForbidden {
		t.Errorf("served: %d", rec.Code)
	}
}

// A link in ~/repos, or a plain folder there, is no working copy: the fence would otherwise be
// drawn around whatever the link points at.
func TestRepoSkillScopeNeedsARealWorkingCopy(t *testing.T) {
	home, _ := repoSkillsEnv(t)
	writeFile(t, filepath.Join(home, ".claude", "skills", "x", "id_rsa"), "private")
	writeFile(t, filepath.Join(home, ".claude", "skills", "x", "SKILL.md"), "---\n---\n")
	if err := os.Symlink(home, filepath.Join(home, "repos", "alias")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "repos", "plain", ".claude", "skills", "p", "SKILL.md"), "x")
	for _, repo := range []string{"alias", "plain"} {
		if code, _ := getRepoCatalog(t, "repo="+repo); code != http.StatusNotFound {
			t.Errorf("catalog %s: %d, want 404", repo, code)
		}
		if rec := getRepoSkillFile(t, "repo="+repo+"&path="+url.QueryEscape(".claude/skills/x/id_rsa")); rec.Code != http.StatusNotFound {
			t.Errorf("file %s: %d %s", repo, rec.Code, rec.Body)
		}
	}
}

// The check and the open are one step: a file swapped for a link after the shape check, on the
// file endpoint and while the catalog reads frontmatter, still does not leak.
func TestRepoSkillSymlinkSwappedAfterTheCheck(t *testing.T) {
	home, dir := repoSkillsEnv(t)
	secret := filepath.Join(home, "secret.txt")
	writeFile(t, secret, "---\ndescription: leaked\n---\n")
	target := filepath.Join(dir, ".claude", "skills", "a", "SKILL.md")
	swap := func() {
		_ = os.Remove(target)
		if err := os.Symlink(secret, target); err != nil {
			t.Error(err)
		}
	}
	beforeFenceOpen = swap
	t.Cleanup(func() { beforeFenceOpen = nil })
	if rec := getRepoSkillFile(t, "repo=demo&path="+url.QueryEscape(".claude/skills/a/SKILL.md")); rec.Code == http.StatusOK {
		t.Errorf("file endpoint served a swapped link: %s", rec.Body)
	}

	writeFile(t, filepath.Join(dir, ".claude", "skills", "a", "SKILL.md"), "---\ndescription: root a\n---\n")
	_ = os.Remove(target)
	writeFile(t, target, "---\ndescription: root a\n---\n")
	_, w := getRepoCatalog(t, "repo=demo")
	a := entriesByRel(t, repoKind(t, w, session.KindClaude))[".claude/skills/a/SKILL.md"]
	if a.Description == "leaked" {
		t.Errorf("catalog read through a swapped link: %#v", a)
	}
	if !a.Outside {
		t.Errorf("swapped entry should be flagged outside: %#v", a)
	}
}

// A repository's SKILL.md that quotes the marker stays a project skill in this view.
func TestRepoSkillMarkerDoesNotMakeItAFleetSkill(t *testing.T) {
	_, dir := repoSkillsEnv(t)
	writeFile(t, filepath.Join(dir, ".claude", "skills", "m", "SKILL.md"), "---\ndescription: m\n---\n"+fleetskills.Marker+"\n")
	_, w := getRepoCatalog(t, "repo=demo")
	m := entriesByRel(t, repoKind(t, w, session.KindClaude))[".claude/skills/m/SKILL.md"]
	if m.Scope != "project" {
		t.Errorf("scope = %q, want project", m.Scope)
	}
}

// A root ABOVE the working copy is fenced at the folder holding it, so a link at the root itself
// (~/repos/.claude/commands -> /) cannot reach files the personal endpoint would refuse.
func TestRepoSkillAboveRootLinkCannotLeadOut(t *testing.T) {
	home, _ := repoSkillsEnv(t)
	writeFile(t, filepath.Join(home, "private.md"), "---\ndescription: private\n---\n")
	writeFile(t, filepath.Join(home, "repos", ".claude", "skills", "shared", "SKILL.md"), "---\ndescription: shared\n---\n")
	if err := os.Symlink("/", filepath.Join(home, "repos", ".claude", "commands")); err != nil {
		t.Fatal(err)
	}
	leak := "../.claude/commands/" + filepath.ToSlash(home)[1:] + "/private.md"
	if rec := getRepoSkillFile(t, "repo=demo&path="+url.QueryEscape(leak)); rec.Code == http.StatusOK {
		t.Errorf("served through a root link: %s", rec.Body)
	}
	if rec := getRepoSkillFile(t, "repo=demo&path="+url.QueryEscape("../.claude/skills/shared/SKILL.md")); rec.Code != http.StatusOK {
		t.Errorf("a real above-root skill should still be served: %d %s", rec.Code, rec.Body)
	}
	_, w := getRepoCatalog(t, "repo=demo")
	for _, s := range repoKind(t, w, session.KindClaude).Skills {
		if s.Description == "private" {
			t.Errorf("listed content from outside: %#v", s)
		}
	}
}

// The repository folder is opened once from the repos root: replacing it by a link to $HOME after
// its name was validated must not turn $HOME into the fence.
func TestRepoSkillFolderSwappedForALink(t *testing.T) {
	home, dir := repoSkillsEnv(t)
	writeFile(t, filepath.Join(home, ".git", "HEAD"), "ref: x")
	writeFile(t, filepath.Join(home, ".claude", "skills", "x", "SKILL.md"), "---\ndescription: home\n---\n")
	beforeRepoOpen = func() {
		if err := os.Rename(dir, dir+".bak"); err != nil {
			t.Error(err)
			return
		}
		if err := os.Symlink(home, dir); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeRepoOpen = nil })
	if code, _ := getRepoCatalog(t, "repo=demo"); code != http.StatusNotFound {
		t.Errorf("catalog after the swap: %d, want 404", code)
	}
	_ = os.Remove(dir)
	_ = os.Rename(dir+".bak", dir)
	beforeRepoOpen = func() {
		_ = os.Rename(dir, dir+".bak")
		_ = os.Symlink(home, dir)
	}
	if rec := getRepoSkillFile(t, "repo=demo&path="+url.QueryEscape(".claude/skills/x/SKILL.md")); rec.Code == http.StatusOK {
		t.Errorf("file after the swap: %s", rec.Body)
	}
}

// The folder's file list is read through the same fence: a skill folder swapped for a link to an
// outside tree after SKILL.md was read exposes none of its names.
func TestRepoSkillFolderFilesSwappedForALink(t *testing.T) {
	home, dir := repoSkillsEnv(t)
	outside := filepath.Join(home, "outside")
	writeFile(t, filepath.Join(outside, "private-name.txt"), "x")
	skill := filepath.Join(dir, ".claude", "skills", "a")
	beforeFolderFiles = func() {
		_ = os.RemoveAll(skill)
		_ = os.Symlink(outside, skill)
	}
	t.Cleanup(func() { beforeFolderFiles = nil })
	_, w := getRepoCatalog(t, "repo=demo")
	a := entriesByRel(t, repoKind(t, w, session.KindClaude))[".claude/skills/a/SKILL.md"]
	for _, f := range a.Files {
		if strings.Contains(f, "private-name.txt") {
			t.Errorf("outside file name exposed: %v", a.Files)
		}
	}
}

// A skill folder that is itself a link keeps its helper files listed: in the personal view (the
// Codex bridge shape) and in a repository when the link stays inside the working copy.
func TestSkillFolderLinkKeepsItsFileList(t *testing.T) {
	home, dir := repoSkillsEnv(t)
	real := filepath.Join(home, "real", "h")
	writeFile(t, filepath.Join(real, "SKILL.md"), "---\n---\n")
	writeFile(t, filepath.Join(real, "refs", "n.md"), "x")
	if err := os.Symlink(real, filepath.Join(home, "claude-config", "skills", "h")); err != nil {
		t.Fatal(err)
	}
	if f := skillNamed(t, catalogOf(t, session.KindClaude), "h", "user").Files; strings.Join(f, ",") != "refs/n.md" {
		t.Errorf("personal linked folder files = %v", f)
	}

	writeFile(t, filepath.Join(dir, "shared", "h", "SKILL.md"), "---\n---\n")
	writeFile(t, filepath.Join(dir, "shared", "h", "refs", "n.md"), "x")
	if err := os.Symlink("../../shared/h", filepath.Join(dir, ".claude", "skills", "h")); err != nil {
		t.Fatal(err)
	}
	_, w := getRepoCatalog(t, "repo=demo")
	h := entriesByRel(t, repoKind(t, w, session.KindClaude))[".claude/skills/h/SKILL.md"]
	if h.Outside || strings.Join(h.Files, ",") != "refs/n.md" {
		t.Errorf("repo linked folder = %#v", h)
	}
}

// A skill folder that is a link out of the working copy is named, flagged outside and never read.
func TestRepoSkillFolderLinkingOutIsListedAsOutside(t *testing.T) {
	home, dir := repoSkillsEnv(t)
	writeFile(t, filepath.Join(home, "elsewhere", "SKILL.md"), "---\ndescription: private\n---\n")
	if err := os.Symlink(filepath.Join(home, "elsewhere"), filepath.Join(dir, ".claude", "skills", "away")); err != nil {
		t.Fatal(err)
	}
	_, w := getRepoCatalog(t, "repo=demo")
	e, ok := entriesByRel(t, repoKind(t, w, session.KindClaude))[".claude/skills/away/SKILL.md"]
	if !ok || !e.Outside || e.Description != "" || e.Name != "away" || len(e.Files) != 0 {
		t.Errorf("away = %#v (listed %v)", e, ok)
	}
}
