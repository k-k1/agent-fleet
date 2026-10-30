package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sqliteHomeFixture isolates HOME and CODEX_HOME and stubs the probes: codex home on NFS, home
// local, no codex and no tmux server running.
func sqliteHomeFixture(t *testing.T) (src, dst string) {
	t.Helper()
	home := t.TempDir()
	src = filepath.Join(home, "keep-codex")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", src)
	t.Setenv(SQLiteHomeEnv, "")
	os.Unsetenv(SQLiteHomeEnv)
	origNFS, origRun, origTmux := onNFS, codexRunning, tmuxUp
	t.Cleanup(func() { onNFS, codexRunning, tmuxUp = origNFS, origRun, origTmux })
	onNFS = func(p string) bool { return p == src }
	codexRunning = func() bool { return false }
	tmuxUp = func() bool { return false }
	return src, SQLiteHomeDir()
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func TestRelocateSQLiteMovesDatabasesOffNFS(t *testing.T) {
	src, dst := sqliteHomeFixture(t)
	writeFile(t, filepath.Join(src, "state_5.sqlite"), "db")
	writeFile(t, filepath.Join(src, "state_5.sqlite-wal"), "wal")
	writeFile(t, filepath.Join(src, "state_5.sqlite-shm"), "shm")
	writeFile(t, filepath.Join(src, "logs_2.sqlite"), "logs")
	writeFile(t, filepath.Join(src, "auth.json"), "{}")
	if err := os.MkdirAll(filepath.Join(src, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}

	dir, note := RelocateSQLite()
	if dir != dst {
		t.Fatalf("dir = %q (note %q), want %q", dir, note, dst)
	}
	// The database travels with its -wal; -shm is rebuilt by SQLite and not carried.
	if got, want := strings.Join(dirNames(t, dst), ","), "logs_2.sqlite,state_5.sqlite,state_5.sqlite-wal"; got != want {
		t.Fatalf("destination = %s, want %s", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "state_5.sqlite-wal")); string(b) != "wal" {
		t.Fatalf("wal content = %q", b)
	}
	// Keep side: auth and rollouts untouched, every database name retired.
	want := "auth.json,logs_2.sqlite.moved-to-local,sessions,state_5.sqlite-shm.moved-to-local," +
		"state_5.sqlite-wal.moved-to-local,state_5.sqlite.moved-to-local"
	if got := strings.Join(dirNames(t, src), ","); got != want {
		t.Fatalf("keep side = %s, want %s", got, want)
	}
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("staging directory left behind: %v", err)
	}

	// The next boot finds the marker and changes nothing.
	if dir, _ := RelocateSQLite(); dir != dst {
		t.Fatalf("second boot dir = %q", dir)
	}
	if got := strings.Join(dirNames(t, src), ","); got != want {
		t.Fatalf("keep side changed on the second boot: %s", got)
	}
}

func TestRelocateSQLiteLeavesOtherLayoutsAlone(t *testing.T) {
	cases := map[string]func(t *testing.T, src string){
		"codex home local": func(*testing.T, string) { onNFS = func(string) bool { return false } },
		"home on NFS too":  func(*testing.T, string) { onNFS = func(string) bool { return true } },
		"codex running":    func(*testing.T, string) { codexRunning = func() bool { return true } },
		"tmux server up":   func(*testing.T, string) { tmuxUp = func() bool { return true } },
		"env set":          func(t *testing.T, _ string) { t.Setenv(SQLiteHomeEnv, "/elsewhere") },
		"config sets it": func(t *testing.T, src string) {
			writeFile(t, filepath.Join(src, "config.toml"), "model = \"x\"\nsqlite_home = \"/local\"\n[features]\n")
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			src, dst := sqliteHomeFixture(t)
			writeFile(t, filepath.Join(src, "state_5.sqlite"), "db")
			arrange(t, src)
			if dir, _ := RelocateSQLite(); dir != "" {
				t.Fatalf("dir = %q, want none", dir)
			}
			if _, err := os.Stat(dst); !os.IsNotExist(err) {
				t.Fatalf("destination created: %v", err)
			}
			if _, err := os.Stat(filepath.Join(src, "state_5.sqlite")); err != nil {
				t.Fatalf("keep-side database moved: %v", err)
			}
		})
	}
}

// An interrupted earlier attempt leaves a staging directory, never the marker; the next boot
// starts over from the keep side instead of trusting the partial copy.
func TestRelocateSQLiteDiscardsAnInterruptedStaging(t *testing.T) {
	src, dst := sqliteHomeFixture(t)
	writeFile(t, filepath.Join(src, "state_5.sqlite"), "db")
	if err := os.MkdirAll(dst+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dst+".tmp", "state_5.sqlite"), "torn")
	if dir, note := RelocateSQLite(); dir != dst {
		t.Fatalf("dir = %q (%s)", dir, note)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "state_5.sqlite")); string(b) != "db" {
		t.Fatalf("destination = %q, want the keep-side bytes", b)
	}
}

// After the move, a database a codex outside af created at the old path is not renamed over
// the retired copy, its WAL is not separated from it, and nothing is renamed while a codex
// process may hold it open.
func TestRelocateSQLiteRetiresWithoutClobbering(t *testing.T) {
	src, dst := sqliteHomeFixture(t)
	writeFile(t, filepath.Join(src, "state_5.sqlite"), "old")
	if dir, _ := RelocateSQLite(); dir != dst {
		t.Fatalf("first boot dir = %q", dir)
	}
	writeFile(t, filepath.Join(src, "state_5.sqlite"), "new")
	writeFile(t, filepath.Join(src, "state_5.sqlite-wal"), "new-wal") // free name, same group
	writeFile(t, filepath.Join(src, "goals_1.sqlite"), "goals")

	codexRunning = func() bool { return true }
	if dir, _ := RelocateSQLite(); dir != dst {
		t.Fatalf("running: dir = %q", dir)
	}
	if _, err := os.Stat(filepath.Join(src, "goals_1.sqlite")); err != nil {
		t.Fatalf("renamed while a codex was running: %v", err)
	}

	codexRunning = func() bool { return false }
	dir, note := RelocateSQLite()
	if dir != dst || !strings.Contains(note, "state_5.sqlite") {
		t.Fatalf("dir = %q, note = %q", dir, note)
	}
	if b, _ := os.ReadFile(filepath.Join(src, "state_5.sqlite"+retiredSuffix)); string(b) != "old" {
		t.Fatalf("retired copy = %q, want the pre-migration bytes", b)
	}
	if b, _ := os.ReadFile(filepath.Join(src, "state_5.sqlite-wal")); string(b) != "new-wal" {
		t.Fatalf("live database's WAL was separated from it (got %q)", b)
	}
	if _, err := os.Stat(filepath.Join(src, "goals_1.sqlite"+retiredSuffix)); err != nil {
		t.Fatalf("free name not retired: %v", err)
	}
}

// Clean home removes ~/.local/state but keeps ~/.codex: the next boot starts codex on an
// empty local directory rather than resurrecting the retired keep-side copies.
func TestRelocateSQLiteAfterCleanHome(t *testing.T) {
	src, dst := sqliteHomeFixture(t)
	writeFile(t, filepath.Join(src, "state_5.sqlite"), "db")
	if dir, _ := RelocateSQLite(); dir != dst {
		t.Fatal("first boot did not move")
	}
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}
	if dir, _ := RelocateSQLite(); dir != dst {
		t.Fatalf("after clean home dir = %q", dir)
	}
	if got := dirNames(t, dst); len(got) != 0 {
		t.Fatalf("destination = %v, want empty", got)
	}
}

func TestConfigSetsSQLiteHome(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"sqlite_home = \"/x\"\n", true},
		{"# note\n  sqlite_home=\"/x\"\n[features]\n", true},
		{"[features]\nsqlite_home = \"/x\"\n", false}, // inside a table, not the top-level key
		{"# sqlite_home = \"/x\"\n", false},
		{"sqlite_home_extra = 1\n", false},
		{"model = \"gpt\"\n", false},
		{"\"sqlite_home\" = \"/x\"\n", true},
		{"dirs = [\n  [\"a\"],\n  [1, 2],\n]\nsqlite_home = \"/x\"\n", true}, // array rows are not headers
		{"note = \"\"\"\nsqlite_home = \"/x\"\n\"\"\"\n", false},             // inside a multi-line string
		{"note = \"\"\"\nx\n\"\"\"\nsqlite_home = \"/x\"\n", true},
		{"[ features ] # c\nsqlite_home = \"/x\"\n", false},
	}
	for _, c := range cases {
		p := filepath.Join(t.TempDir(), "config.toml")
		writeFile(t, p, c.body)
		if got := configSetsSQLiteHome(p); got != c.want {
			t.Errorf("%q: got %v, want %v", c.body, got, c.want)
		}
	}
	if configSetsSQLiteHome(filepath.Join(t.TempDir(), "missing.toml")) {
		t.Error("missing file reported as set")
	}
}

// The real probes, on what this machine has: a temp dir is not NFS, and the scan must at
// least see a codex binary it started itself.
func TestSQLiteHomeProbes(t *testing.T) {
	if statfsIsNFS(t.TempDir()) {
		t.Skip("TMPDIR is on NFS here")
	}
	fake := filepath.Join(t.TempDir(), "codex")
	b, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skip("no /bin/sleep")
	}
	writeFile(t, fake, string(b))
	if err := os.Chmod(fake, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(fake, "30")
	if err := cmd.Start(); err != nil {
		t.Skip("cannot exec from TMPDIR: ", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for _, pid := range codexPIDs() {
		if pid == cmd.Process.Pid {
			return
		}
	}
	t.Fatalf("the codex binary this test started (pid %d) was not seen", cmd.Process.Pid)
}
