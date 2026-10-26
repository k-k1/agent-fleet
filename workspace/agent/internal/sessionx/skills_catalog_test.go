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

// catalogEnv points every kind's personal root into a temp home and returns
// (home, CLAUDE_CONFIG_DIR, CODEX_HOME).
func catalogEnv(t *testing.T, bundled ...string) (string, string, string) {
	t.Helper()
	home := t.TempDir()
	cdir := filepath.Join(home, "claude-config")
	chome := filepath.Join(home, ".codex")
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", cdir)
	t.Setenv("CODEX_HOME", chome)
	orig := claudeBundledSkills
	claudeBundledSkills = func() []string { return bundled }
	t.Cleanup(func() { claudeBundledSkills = orig })
	return home, cdir, chome
}

func catalogOf(t *testing.T, kind string) catalogKind {
	t.Helper()
	for _, k := range skillCatalog() {
		if k.Kind == kind {
			return k
		}
	}
	t.Fatalf("kind %s missing from the catalog", kind)
	return catalogKind{}
}

func skillNamed(t *testing.T, k catalogKind, name, scope string) catalogSkill {
	t.Helper()
	for _, s := range k.Skills {
		if s.Name == name && (scope == "" || s.Scope == scope) {
			return s
		}
	}
	t.Fatalf("%s: no %s skill %q in %#v", k.Kind, scope, name, k.Skills)
	return catalogSkill{}
}

func TestSkillCatalogPersonalRoots(t *testing.T) {
	home, cdir, chome := catalogEnv(t, "simplify", "handoff")
	writeFile(t, filepath.Join(cdir, "skills", "handoff", "SKILL.md"),
		"---\nname: handoff\ndescription: hand over\nargument-hint: \"<who>\"\n---\nbody")
	writeFile(t, filepath.Join(cdir, "skills", "handoff", "refs", "notes.md"), "ref")
	writeFile(t, filepath.Join(cdir, "skills", "handoff", ".git", "HEAD"), "hidden")
	writeFile(t, filepath.Join(cdir, "skills", "quiet", "SKILL.md"), "---\nuser-invocable: false\n---\n")
	writeFile(t, filepath.Join(cdir, "skills", "af-build", "SKILL.md"), "---\nname: af-build\n---\n"+fleetskills.Marker+"\n")
	// A command of the same name as a skill: claude has one slash namespace, the skill wins.
	writeFile(t, filepath.Join(cdir, "commands", "handoff.md"), "---\ndescription: twin\n---\n")
	writeFile(t, filepath.Join(chome, "skills", "review", "SKILL.md"), "---\nname: review\n---\n")
	writeFile(t, filepath.Join(chome, "skills", ".system", "review", "SKILL.md"), "---\nname: review\n---\n")
	writeFile(t, filepath.Join(home, ".config", "muse", "skills", "draft", "SKILL.md"), "---\ndescription: d\n---\n")

	cl := catalogOf(t, session.KindClaude)
	h := skillNamed(t, cl, "handoff", "user")
	if h.Type != "skill" || h.Description != "hand over" || h.ArgumentHint != "<who>" || h.ShadowedBy != "" {
		t.Errorf("handoff skill = %#v", h)
	}
	if h.Path != filepath.Join(cdir, "skills", "handoff", "SKILL.md") || h.Root != filepath.Join(cdir, "skills") {
		t.Errorf("handoff location = %q in %q", h.Path, h.Root)
	}
	if strings.Join(h.Files, ",") != "refs/notes.md" {
		t.Errorf("handoff files = %v, want only refs/notes.md (no SKILL.md, no hidden)", h.Files)
	}
	cmd := skillNamed(t, cl, "handoff", "")
	for _, s := range cl.Skills {
		if s.Name == "handoff" && s.Type == "command" {
			cmd = s
		}
	}
	if cmd.Type != "command" || cmd.ShadowedBy != h.Path {
		t.Errorf("handoff command should be shadowed by the skill: %#v", cmd)
	}
	if q := skillNamed(t, cl, "quiet", "user"); q.UserInvocable {
		t.Errorf("quiet should be listed as not user-invocable: %#v", q)
	}
	skillNamed(t, cl, "af-build", "fleet")
	// The bundled list carries the user's own names too; only the unknown one is added.
	if s := skillNamed(t, cl, "simplify", "cli"); s.Path != "" {
		t.Errorf("bundled skill has a path: %#v", s)
	}
	n := 0
	for _, s := range cl.Skills {
		if s.Name == "handoff" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("handoff entries = %d, want the skill and the command only", n)
	}

	cx := catalogOf(t, session.KindCodex)
	if u := skillNamed(t, cx, "review", "user"); u.ShadowedBy != "" {
		t.Errorf("user review should win: %#v", u)
	}
	if s := skillNamed(t, cx, "review", "cli"); s.ShadowedBy != filepath.Join(chome, "skills", "review", "SKILL.md") {
		t.Errorf("bundled review should be shadowed by the user one: %#v", s)
	}
	skillNamed(t, catalogOf(t, session.KindMuse), "draft", "user")

	oc := catalogOf(t, session.KindOpencode)
	if len(oc.Skills) != 0 || len(oc.Roots) != 3 {
		t.Fatalf("opencode = %#v", oc)
	}
	for _, r := range oc.Roots {
		if r.Exists {
			t.Errorf("opencode root %s reported present", r.Dir)
		}
	}
}

func getSkillFile(t *testing.T, p string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleSkillFile(rec, httptest.NewRequest("GET", "/skills/file?path="+url.QueryEscape(p), nil))
	return rec
}

func TestSkillFileStaysInsideSkillRoots(t *testing.T) {
	home, cdir, chome := catalogEnv(t)
	skill := filepath.Join(cdir, "skills", "handoff", "SKILL.md")
	writeFile(t, skill, "---\nname: handoff\n---\nbody")
	writeFile(t, filepath.Join(cdir, ".credentials.json"), "secret")
	writeFile(t, filepath.Join(chome, "auth.json"), "secret")
	writeFile(t, filepath.Join(cdir, "commands", "go.md"), "cmd")
	writeFile(t, filepath.Join(cdir, "skills", "loose.md"), "loose")
	// A link inside a skill folder that leaves the roots, and one codex bridge-style link to a
	// claude skill (another listed root), which must keep working.
	if err := os.Symlink(filepath.Join(chome, "auth.json"), filepath.Join(cdir, "skills", "handoff", "leak.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(chome, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(cdir, "skills", "handoff"), filepath.Join(chome, "skills", "handoff")); err != nil {
		t.Fatal(err)
	}

	rec := getSkillFile(t, skill)
	if rec.Code != http.StatusOK {
		t.Fatalf("skill file: %d %s", rec.Code, rec.Body)
	}
	var got struct{ Content string }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !strings.Contains(got.Content, "body") {
		t.Errorf("content = %q", got.Content)
	}
	for _, ok := range []string{
		filepath.Join(cdir, "commands", "go.md"),
		filepath.Join(chome, "skills", "handoff", "SKILL.md"),
	} {
		if rec := getSkillFile(t, ok); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", ok, rec.Code, rec.Body)
		}
	}
	for _, bad := range []string{
		filepath.Join(cdir, ".credentials.json"),
		filepath.Join(chome, "auth.json"),
		filepath.Join(cdir, "skills", "handoff", "..", "..", ".credentials.json"),
		filepath.Join(cdir, "skills", "handoff", "leak.md"),
		filepath.Join(cdir, "skills", "loose.md"),
		filepath.Join(cdir, "skills"),
		filepath.Join(home, ".bashrc"),
		"claude-config/skills/handoff/SKILL.md",
		"",
	} {
		if rec := getSkillFile(t, bad); rec.Code != http.StatusForbidden {
			t.Errorf("%q: status %d, want 403 (%s)", bad, rec.Code, rec.Body)
		}
	}
}

// opencode 2.x: IDs come from the path (the frontmatter name is a label), a loose <root>/<id>.md
// and a nested <dir>/<id>/SKILL.md are both skills, and skills and commands are separate
// namespaces, so neither shadows the other.
func TestSkillCatalogOpencodeDiscovery(t *testing.T) {
	home, _, _ := catalogEnv(t)
	oc := filepath.Join(home, ".config", "opencode")
	writeFile(t, filepath.Join(oc, "skills", "alpha", "SKILL.md"), "---\nname: Review\ndescription: a\n---\n")
	writeFile(t, filepath.Join(oc, "skills", "beta", "SKILL.md"), "---\nname: Review\ndescription: b\n---\n")
	writeFile(t, filepath.Join(oc, "skills", "review.md"), "---\ndescription: loose\n---\n")
	writeFile(t, filepath.Join(oc, "skills", "teams", "release", "SKILL.md"), "---\ndescription: nested\n---\n")
	// Below a SKILL.md nothing is a skill of its own, and loose files count only at the root.
	writeFile(t, filepath.Join(oc, "skills", "alpha", "inner", "SKILL.md"), "---\n---\n")
	writeFile(t, filepath.Join(oc, "skills", "teams", "notes.md"), "not a skill")
	writeFile(t, filepath.Join(oc, "skills", ".hidden", "SKILL.md"), "---\n---\n")
	writeFile(t, filepath.Join(oc, "commands", "review.md"), "---\ndescription: cmd\n---\n")

	k := catalogOf(t, session.KindOpencode)
	got := map[string]catalogSkill{}
	for _, s := range k.Skills {
		got[s.Type+":"+s.Name] = s
	}
	want := []string{"skill:alpha", "skill:beta", "skill:review", "skill:release", "command:review"}
	if len(got) != len(want) || len(k.Skills) != len(want) {
		t.Fatalf("opencode entries = %#v, want %v", k.Skills, want)
	}
	for _, w := range want {
		s, ok := got[w]
		if !ok {
			t.Fatalf("missing %s in %#v", w, k.Skills)
		}
		if s.ShadowedBy != "" {
			t.Errorf("%s is shadowed by %s", w, s.ShadowedBy)
		}
	}
	if got["skill:alpha"].Title != "Review" || got["skill:review"].Title != "" {
		t.Errorf("titles: alpha=%q review=%q", got["skill:alpha"].Title, got["skill:review"].Title)
	}
	loose := filepath.Join(oc, "skills", "review.md")
	if got["skill:review"].Path != loose {
		t.Errorf("loose skill path = %q", got["skill:review"].Path)
	}
	if rec := getSkillFile(t, loose); rec.Code != http.StatusOK {
		t.Errorf("loose opencode skill: %d %s", rec.Code, rec.Body)
	}
	if rec := getSkillFile(t, filepath.Join(oc, "skills", "teams", "release", "SKILL.md")); rec.Code != http.StatusOK {
		t.Errorf("nested opencode skill: %d %s", rec.Code, rec.Body)
	}
}
