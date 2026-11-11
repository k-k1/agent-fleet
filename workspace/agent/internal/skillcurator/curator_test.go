package skillcurator

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetskills"
)

var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func days(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }

// skill writes root/name/SKILL.md with body and sets its mtime.
func skill(t *testing.T, root, name, body string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return dir
}

func agentBody() string { return "---\nname: x\ndescription: d\n---\n" + AgentMarker + "\nbody\n" }

func touch(t *testing.T, p string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func present(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestAgeThresholds(t *testing.T) {
	root := t.TempDir()
	skill(t, root, "afa-fresh", agentBody(), days(13))
	skill(t, root, "afa-stale", agentBody(), days(14))
	skill(t, root, "afa-old", agentBody(), days(30))
	skill(t, root, "afa-future", agentBody(), now.Add(48*time.Hour)) // clock jumped

	res := Run(root, now, DefaultConfig(), nil)
	if !reflect.DeepEqual(res.Stale, []string{"afa-stale"}) || !reflect.DeepEqual(res.Archived, []string{"afa-old"}) {
		t.Fatalf("result %+v", res)
	}
	for _, n := range []string{"afa-fresh", "afa-future", "afa-stale"} {
		if !present(filepath.Join(root, n, "SKILL.md")) {
			t.Fatalf("%s must stay", n)
		}
	}
	if present(filepath.Join(root, "afa-fresh", StaleFile)) || present(filepath.Join(root, "afa-future", StaleFile)) {
		t.Fatal("a fresh skill was marked stale")
	}
	if !present(filepath.Join(root, "afa-stale", StaleFile)) {
		t.Fatal("stale mark missing")
	}
	if !present(filepath.Join(root, ArchiveDir, "afa-old", "SKILL.md")) || present(filepath.Join(root, "afa-old")) {
		t.Fatal("old skill not moved to the archive")
	}
	// A second pass changes nothing.
	if res := Run(root, now, DefaultConfig(), nil); len(res.Stale)+len(res.Archived)+len(res.Revived) != 0 {
		t.Fatalf("second pass: %+v", res)
	}
}

// Only a skill that proves it is agent-authored is ever touched. Every other shape is old enough
// to be archived and must survive untouched.
func TestOnlyMarkedAgentSkillsAreTouched(t *testing.T) {
	root := t.TempDir()
	user := "---\nname: u\n---\nmy own skill\n"
	skill(t, root, "afa-nomarker", user, days(400))                                       // user's own, agent prefix
	skill(t, root, "my-skill", agentBody(), days(400))                                    // marker but no prefix
	skill(t, root, "af-notes", "---\n---\n"+fleetskills.Marker+"\nx\n", days(400))        // fleetskills' own
	skill(t, root, "afa-quoted", "---\n---\ndocs say: "+AgentMarker+"\n", days(400))      // marker quoted, not leading
	skill(t, root, "afa-fleetmarker", "---\n---\n"+fleetskills.Marker+"\nx\n", days(400)) // wrong marker
	skill(t, root, "afa-nofm", AgentMarker+"\nbody\n", days(400))                         // no frontmatter, marker first: ours
	other := t.TempDir()
	skill(t, other, "afa-target", agentBody(), days(400))
	if err := os.Symlink(filepath.Join(other, "afa-target"), filepath.Join(root, "afa-link")); err != nil {
		t.Fatal(err)
	}

	res := Run(root, now, DefaultConfig(), nil)
	if !reflect.DeepEqual(res.Archived, []string{"afa-nofm"}) || len(res.Stale) != 0 {
		t.Fatalf("result %+v", res)
	}
	for _, n := range []string{"afa-nomarker", "my-skill", "af-notes", "afa-quoted", "afa-fleetmarker", "afa-link"} {
		if !present(filepath.Join(root, n)) {
			t.Fatalf("%s was moved", n)
		}
	}
	if !present(filepath.Join(other, "afa-target", "SKILL.md")) || present(filepath.Join(other, "afa-target", StaleFile)) {
		t.Fatal("the symlink target was touched")
	}
}

func TestPinnedIsSkipped(t *testing.T) {
	root := t.TempDir()
	dir := skill(t, root, "afa-pin", agentBody(), days(400))
	touch(t, filepath.Join(dir, PinnedFile), days(400))
	if res := Run(root, now, DefaultConfig(), nil); len(res.Stale)+len(res.Archived) != 0 {
		t.Fatalf("%+v", res)
	}
	if present(filepath.Join(dir, StaleFile)) || !present(filepath.Join(dir, "SKILL.md")) {
		t.Fatal("pinned skill touched")
	}
}

// A recorded use outranks an old write; a rewrite outranks an old recorded use.
func TestLastUsedFile(t *testing.T) {
	root := t.TempDir()
	a := skill(t, root, "afa-used", agentBody(), days(100))
	touch(t, filepath.Join(a, LastUsedFile), days(1))
	b := skill(t, root, "afa-rewritten", agentBody(), days(1))
	touch(t, filepath.Join(b, LastUsedFile), days(100))
	if res := Run(root, now, DefaultConfig(), nil); len(res.Stale)+len(res.Archived) != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestUsedAgainClearsStaleMark(t *testing.T) {
	root := t.TempDir()
	dir := skill(t, root, "afa-x", agentBody(), days(20))
	Run(root, now, DefaultConfig(), nil)
	if !present(filepath.Join(dir, StaleFile)) {
		t.Fatal("not marked")
	}
	touch(t, filepath.Join(dir, LastUsedFile), now)
	res := Run(root, now, DefaultConfig(), nil)
	if !reflect.DeepEqual(res.Revived, []string{"afa-x"}) || present(filepath.Join(dir, StaleFile)) {
		t.Fatalf("%+v", res)
	}
}

func TestStopsWhenNoLongerIdle(t *testing.T) {
	root := t.TempDir()
	skill(t, root, "afa-a", agentBody(), days(100))
	skill(t, root, "afa-b", agentBody(), days(100))
	calls := 0
	res := Run(root, now, DefaultConfig(), func() bool { calls++; return calls == 1 })
	if len(res.Archived) != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestArchiveClashAndRestore(t *testing.T) {
	root := t.TempDir()
	skill(t, root, "afa-a", agentBody(), days(100))
	Run(root, now, DefaultConfig(), nil)
	skill(t, root, "afa-a", agentBody(), days(100)) // same name again
	Run(root, now, DefaultConfig(), nil)
	if got := Archived(root); !reflect.DeepEqual(got, []string{"afa-a", "afa-a.2"}) {
		t.Fatalf("archive %v", got)
	}
	if err := Restore(root, "afa-a.2", now); err != nil {
		t.Fatal(err)
	}
	if !present(filepath.Join(root, "afa-a", "SKILL.md")) {
		t.Fatal("not restored")
	}
	if err := Restore(root, "afa-a", now); err == nil {
		t.Fatal("restore over an existing skill must refuse")
	}
	// Restored with a fresh use time: the next pass leaves it.
	if res := Run(root, now, DefaultConfig(), nil); len(res.Archived) != 0 {
		t.Fatalf("restored skill archived again: %+v", res)
	}
	for _, bad := range []string{"../afa-a", "nope", "afa-zzz"} {
		if err := Restore(root, bad, now); err == nil {
			t.Fatalf("restore %q accepted", bad)
		}
	}
}

func TestMissingRootIsNoop(t *testing.T) {
	if res := Run(filepath.Join(t.TempDir(), "none"), now, DefaultConfig(), nil); len(res.Archived) != 0 {
		t.Fatal(res)
	}
}

// A skill whose real name ends in .N is restored under that name, not the clash-free stem.
func TestRestoreKeepsDottedName(t *testing.T) {
	root := t.TempDir()
	skill(t, root, "afa-a.2", agentBody(), days(100))
	Run(root, now, DefaultConfig(), nil)
	if err := Restore(root, "afa-a.2", now); err != nil {
		t.Fatal(err)
	}
	if !present(filepath.Join(root, "afa-a.2", "SKILL.md")) || present(filepath.Join(root, "afa-a")) {
		t.Fatal("restored under the wrong name")
	}
}

// An .archive that is a link must not receive the skill, and nothing may leave the root.
func TestArchiveSymlinkRefused(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	if err := os.Symlink(other, filepath.Join(root, ArchiveDir)); err != nil {
		t.Fatal(err)
	}
	skill(t, root, "afa-old", agentBody(), days(100))
	res := Run(root, now, DefaultConfig(), nil)
	if len(res.Archived) != 0 || !present(filepath.Join(root, "afa-old", "SKILL.md")) {
		t.Fatalf("%+v", res)
	}
	if ents, _ := os.ReadDir(other); len(ents) != 0 {
		t.Fatal("something was written through the link")
	}
	if err := Restore(root, "afa-old", now); err == nil {
		t.Fatal("restore through a linked archive accepted")
	}
}

// Restore must not write through a .last-used link.
func TestRestoreDoesNotFollowLastUsedLink(t *testing.T) {
	root := t.TempDir()
	skill(t, root, "afa-a", agentBody(), days(100))
	Run(root, now, DefaultConfig(), nil)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, ArchiveDir, "afa-a", LastUsedFile)); err != nil {
		t.Fatal(err)
	}
	if err := Restore(root, "afa-a", now); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "ORIGINAL" {
		t.Fatalf("victim rewritten: %q", b)
	}
}

func TestPinClearsStaleMark(t *testing.T) {
	root := t.TempDir()
	dir := skill(t, root, "afa-p", agentBody(), days(20))
	Run(root, now, DefaultConfig(), nil)
	touch(t, filepath.Join(dir, PinnedFile), now)
	Run(root, now, DefaultConfig(), nil)
	if present(filepath.Join(dir, StaleFile)) {
		t.Fatal("stale mark left on a pinned skill")
	}
}
