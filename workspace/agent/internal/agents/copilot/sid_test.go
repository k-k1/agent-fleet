package copilot

import (
	"os"
	"path/filepath"
	"testing"
)

// realWorkspaceYAML is copied verbatim from a copilot v1.0.73 session on disk. The parts
// that matter are the name line whose value contains a colon and the created_at with
// milliseconds plus Z: a naive split breaks on both.
const realWorkspaceYAML = `id: 254e5d40-17fb-4de1-9a29-3db2da0c9c36
cwd: /tmp/repo
client_name: github/cli
name: You must call the probe structured_probe tool exactly once. Then output a compact JSON object.
user_named: false
summary_count: 0
created_at: 2026-08-01T15:15:38.514Z
updated_at: 2026-08-01T15:15:40.479Z
`

func writeSession(t *testing.T, home, sid, yaml string) {
	t.Helper()
	dir := filepath.Join(home, "session-state", sid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if yaml == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

// id / cwd / created_at must be readable out of the real workspace.yaml shape. Without
// that, drift recovery produces no candidates at all and the feature silently does
// nothing.
func TestCliSessionsReadsRealWorkspaceYAML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	writeSession(t, home, "254e5d40-17fb-4de1-9a29-3db2da0c9c36", realWorkspaceYAML)

	got := cliSessions("/tmp/repo")
	if len(got) != 1 {
		t.Fatalf("cliSessions = %+v, want 1 session", got)
	}
	if got[0].ID != "254e5d40-17fb-4de1-9a29-3db2da0c9c36" {
		t.Fatalf("id = %q", got[0].ID)
	}
	if got[0].Created.IsZero() {
		t.Fatal("created_at was not read - the predecessor session can no longer be told apart")
	}
	if y, m, d := got[0].Created.Date(); y != 2026 || m != 8 || d != 1 {
		t.Fatalf("created = %v, want 2026-08-01", got[0].Created)
	}
}

// Sessions from another cwd are not candidates. copilot's session-state is not split by
// cwd - every slot sits in the same directory - so this filter is the only attribution
// there is.
func TestCliSessionsFiltersByCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	writeSession(t, home, "aaaaaaaa-0000-4000-8000-000000000001",
		"id: aaaaaaaa-0000-4000-8000-000000000001\ncwd: /tmp/repo\ncreated_at: 2026-08-01T15:15:38.514Z\n")
	writeSession(t, home, "bbbbbbbb-0000-4000-8000-000000000002",
		"id: bbbbbbbb-0000-4000-8000-000000000002\ncwd: /tmp/other\ncreated_at: 2026-08-01T15:15:38.514Z\n")

	got := cliSessions("/tmp/repo")
	if len(got) != 1 || got[0].ID != "aaaaaaaa-0000-4000-8000-000000000001" {
		t.Fatalf("cliSessions = %+v, want only the /tmp/repo one", got)
	}
}

// A session with no workspace.yaml, or one whose shape changed so the cwd cannot be read,
// is dropped. Accepting something whose attribution cannot be confirmed is exactly how
// someone else's conversation ends up in the mirror.
func TestCliSessionsSkipsUnattributable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	writeSession(t, home, "cccccccc-0000-4000-8000-000000000003", "") // no workspace.yaml

	if got := cliSessions("/tmp/repo"); len(got) != 0 {
		t.Fatalf("cliSessions = %+v, want none", got)
	}
}
