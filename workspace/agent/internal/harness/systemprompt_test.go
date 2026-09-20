package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateHome points $HOME (and clears AF_WORKSPACE_NOTES*) at a fresh temp dir, so
// userinstr.FleetNotes()/Load() read fixtures instead of this container's own real files.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_WORKSPACE_NOTES", "")
	t.Setenv("AF_WORKSPACE_NOTES_DIR", "")
	return home
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSystemPromptOrdersFleetUserProjectSkills(t *testing.T) {
	home := isolateHome(t)

	fleetPath := filepath.Join(home, "fleet-notes.md")
	writeFile(t, fleetPath, "FLEET POLICY TEXT")
	t.Setenv("AF_WORKSPACE_NOTES", fleetPath)

	writeFile(t, filepath.Join(home, ".config", "agent-fleet", "user-notes.md"), "USER NOTES TEXT")

	repo := filepath.Join(home, "repo")
	writeFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(repo, "AGENTS.md"), "PROJECT AGENTS TEXT")

	writeFile(t, filepath.Join(repo, ".claude", "skills", "myskill", "SKILL.md"),
		"---\nname: myskill\ndescription: does a thing\n---\nbody")

	got := SystemPrompt(repo, "lcpp")

	fleetAt := strings.Index(got, "FLEET POLICY TEXT")
	userAt := strings.Index(got, "USER NOTES TEXT")
	projAt := strings.Index(got, "PROJECT AGENTS TEXT")
	skillAt := strings.Index(got, "myskill")
	if fleetAt < 0 || userAt < 0 || projAt < 0 || skillAt < 0 {
		t.Fatalf("missing a section, got:\n%s", got)
	}
	if !(fleetAt < userAt && userAt < projAt && projAt < skillAt) {
		t.Fatalf("wrong order (fleet=%d user=%d proj=%d skill=%d):\n%s", fleetAt, userAt, projAt, skillAt, got)
	}
}

func TestSystemPromptOmitsEmptySections(t *testing.T) {
	isolateHome(t)
	t.Setenv("AF_WORKSPACE_NOTES", filepath.Join(t.TempDir(), "missing.md"))
	repo := t.TempDir()
	got := SystemPrompt(repo, "lcpp")
	if got != "" {
		t.Fatalf("want empty prompt with nothing configured, got %q", got)
	}
}

func TestSystemPromptHandlesEmptyCwd(t *testing.T) {
	isolateHome(t)
	t.Setenv("AF_WORKSPACE_NOTES", filepath.Join(t.TempDir(), "missing.md"))
	if got := SystemPrompt("", "lcpp"); got != "" {
		t.Fatalf("want empty prompt, got %q", got)
	}
}

func TestProjectInstructionsWithinOneDirectoryOrdersAgentsBeforeClaude(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "AGENTS BODY")
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "CLAUDE BODY")
	got := projectInstructions(dir)
	agentsAt := strings.Index(got, "AGENTS BODY")
	claudeAt := strings.Index(got, "CLAUDE BODY")
	if agentsAt < 0 || claudeAt < 0 || agentsAt > claudeAt {
		t.Fatalf("want AGENTS.md before CLAUDE.md, got:\n%s", got)
	}
}

func TestProjectInstructionsWalksUpToGitRootRootFirst(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "ROOT AGENTS")
	nested := filepath.Join(root, "pkg", "sub")
	writeFile(t, filepath.Join(nested, "AGENTS.md"), "NESTED AGENTS")

	got := projectInstructions(nested)
	rootAt := strings.Index(got, "ROOT AGENTS")
	nestedAt := strings.Index(got, "NESTED AGENTS")
	if rootAt < 0 || nestedAt < 0 || rootAt > nestedAt {
		t.Fatalf("want root AGENTS.md before the nested one, got:\n%s", got)
	}
}

func TestProjectInstructionsStopsAtGitRoot(t *testing.T) {
	outer := t.TempDir()
	writeFile(t, filepath.Join(outer, "AGENTS.md"), "SHOULD NOT APPEAR")
	repo := filepath.Join(outer, "repo")
	writeFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(repo, "AGENTS.md"), "REPO AGENTS")

	got := projectInstructions(repo)
	if strings.Contains(got, "SHOULD NOT APPEAR") {
		t.Fatalf("read above the git root:\n%s", got)
	}
	if !strings.Contains(got, "REPO AGENTS") {
		t.Fatalf("missing the repo's own AGENTS.md:\n%s", got)
	}
}

func TestProjectInstructionsWithNoGitRootReadsCwdOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "LONE AGENTS")
	got := projectInstructions(dir)
	if !strings.Contains(got, "LONE AGENTS") {
		t.Fatalf("missing cwd's own AGENTS.md:\n%s", got)
	}
}

func TestForeignSkillsSkipsUserInvocableFalse(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "x\n")
	writeFile(t, filepath.Join(dir, ".codex", "skills", "hidden", "SKILL.md"),
		"---\nname: hidden\nuser-invocable: false\n---\nbody")
	writeFile(t, filepath.Join(dir, ".agents", "skills", "shown", "SKILL.md"),
		"---\nname: shown\ndescription: visible one\n---\nbody")

	got := foreignSkillsPrompt(dir)
	if strings.Contains(got, "hidden") {
		t.Fatalf("user-invocable:false skill was advertised:\n%s", got)
	}
	if !strings.Contains(got, "shown") {
		t.Fatalf("missing the visible skill:\n%s", got)
	}
}
