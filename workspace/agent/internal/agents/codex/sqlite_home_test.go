package codex

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
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
	origNFS, origRun, origTmux, origSync, origNow := onNFS, codexRunning, tmuxUp, parentSync, now
	t.Cleanup(func() {
		onNFS, codexRunning, tmuxUp, parentSync, now = origNFS, origRun, origTmux, origSync, origNow
	})
	now = func() time.Time { return time.Unix(100, 0) }
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
	want := "auth.json,logs_2.sqlite.moved-to-local-100,sessions,state_5.sqlite-shm.moved-to-local-100," +
		"state_5.sqlite-wal.moved-to-local-100,state_5.sqlite.moved-to-local-100"
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

// After the move, database files a codex outside af created at the old path are left alone
// (renaming them would take a live database from that process) and only named in the log.
func TestRelocateSQLiteLeavesLaterDatabasesAlone(t *testing.T) {
	src, dst := sqliteHomeFixture(t)
	writeFile(t, filepath.Join(src, "state_5.sqlite"), "old")
	if dir, _ := RelocateSQLite(); dir != dst {
		t.Fatalf("first boot dir = %q", dir)
	}
	writeFile(t, filepath.Join(src, "state_5.sqlite"), "new")
	writeFile(t, filepath.Join(src, "state_5.sqlite-wal"), "new-wal")
	dir, note := RelocateSQLite()
	if dir != dst || !strings.Contains(note, "state_5.sqlite, state_5.sqlite-wal") {
		t.Fatalf("dir = %q, note = %q", dir, note)
	}
	for name, want := range map[string]string{
		"state_5.sqlite": "new", "state_5.sqlite-wal": "new-wal", "state_5.sqlite.moved-to-local-100": "old",
	} {
		if b, _ := os.ReadFile(filepath.Join(src, name)); string(b) != want {
			t.Fatalf("%s = %q, want %q", name, b, want)
		}
	}
}

// A boot that stopped between the renames, or whose directory sync failed, leaves the manifest
// in dst; the next boot finishes the listed renames with the same stamp, and none happen while
// a codex process is running.
func TestRelocateSQLiteResumesTheRetire(t *testing.T) {
	src, dst := sqliteHomeFixture(t)
	writeFile(t, filepath.Join(src, "state_5.sqlite"), "db")
	writeFile(t, filepath.Join(src, "state_5.sqlite-wal"), "wal")
	parentSync = func(string) error { return errors.New("EIO") }
	dir, note := RelocateSQLite()
	if dir != dst || !strings.Contains(note, "left until the next start") {
		t.Fatalf("dir = %q, note = %q", dir, note)
	}
	if got := strings.Join(dirNames(t, src), ","); got != "state_5.sqlite,state_5.sqlite-wal" {
		t.Fatalf("keep side retired before dst was durable: %s", got)
	}
	// An Agent-only restart sees dst, which proves nothing about durability: still no retire.
	if dir, _ := RelocateSQLite(); dir != dst {
		t.Fatalf("restart dir = %q", dir)
	}
	if got := strings.Join(dirNames(t, src), ","); got != "state_5.sqlite,state_5.sqlite-wal" {
		t.Fatalf("keep side retired while the sync still fails: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dst, retireManifest)); err != nil {
		t.Fatalf("manifest gone while the sync still fails: %v", err)
	}
	parentSync = syncDir
	now = func() time.Time { return time.Unix(999, 0) } // the stamp comes from the manifest

	// Simulate a stop after the first rename.
	if err := os.Rename(filepath.Join(src, "state_5.sqlite"), filepath.Join(src, "state_5.sqlite.moved-to-local-100")); err != nil {
		t.Fatal(err)
	}
	codexRunning = func() bool { return true }
	if dir, _ := RelocateSQLite(); dir != dst {
		t.Fatalf("running: dir = %q", dir)
	}
	if _, err := os.Stat(filepath.Join(src, "state_5.sqlite-wal")); err != nil {
		t.Fatalf("renamed while a codex was running: %v", err)
	}
	codexRunning = func() bool { return false }
	if dir, _ := RelocateSQLite(); dir != dst {
		t.Fatalf("dir = %q", dir)
	}
	if got := strings.Join(dirNames(t, src), ","); got != "state_5.sqlite-wal.moved-to-local-100,state_5.sqlite.moved-to-local-100" {
		t.Fatalf("keep side = %s", got)
	}
	if _, err := os.Stat(filepath.Join(dst, retireManifest)); !os.IsNotExist(err) {
		t.Fatalf("manifest left after the retire finished: %v", err)
	}
}

// A file recreated at a listed name after the copy, or a listed name whose retired name already
// exists, is never renamed: a retired copy is not overwritten, nor paired with another
// generation's WAL.
func TestRelocateSQLiteResumeSkipsAnotherGeneration(t *testing.T) {
	src, dst := sqliteHomeFixture(t)
	writeFile(t, filepath.Join(src, "state_5.sqlite"), "A")
	writeFile(t, filepath.Join(src, "state_5.sqlite-wal"), "A-wal")
	writeFile(t, filepath.Join(src, "logs_2.sqlite"), "L")
	parentSync = func(string) error { return errors.New("EIO") }
	if dir, _ := RelocateSQLite(); dir != dst {
		t.Fatal("first boot did not move")
	}
	parentSync = syncDir
	// state_5: both retired, but the manifest not yet removed, then a codex without the
	// variable created B at the old name and checkpointed its WAL away.
	for _, n := range []string{"state_5.sqlite", "state_5.sqlite-wal"} {
		if err := os.Rename(filepath.Join(src, n), filepath.Join(src, n+".moved-to-local-100")); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(src, "state_5.sqlite"), "B")
	// logs_2: replaced in place by a new file (a new inode) before any retire ran.
	if err := os.Remove(filepath.Join(src, "logs_2.sqlite")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "logs_2.sqlite"), "L2")

	dir, note := RelocateSQLite()
	if dir != dst || !strings.Contains(note, "changed since the copy: logs_2.sqlite") {
		t.Fatalf("dir = %q, note = %q", dir, note)
	}
	for name, want := range map[string]string{
		"state_5.sqlite": "B", "state_5.sqlite.moved-to-local-100": "A",
		"state_5.sqlite-wal.moved-to-local-100": "A-wal", "logs_2.sqlite": "L2",
	} {
		if b, _ := os.ReadFile(filepath.Join(src, name)); string(b) != want {
			t.Fatalf("%s = %q, want %q", name, b, want)
		}
	}
	if _, err := os.Stat(filepath.Join(src, "logs_2.sqlite.moved-to-local-100")); !os.IsNotExist(err) {
		t.Fatalf("a recreated file was retired: %v", err)
	}
}

// A codex whose npm launcher was still node at the first probe and started during the copy
// may have written the source: the staged copy is thrown away, the keep side untouched.
func TestRelocateSQLiteDiscardsACopyRacedByCodex(t *testing.T) {
	src, dst := sqliteHomeFixture(t)
	writeFile(t, filepath.Join(src, "state_5.sqlite"), "db")
	calls := 0
	codexRunning = func() bool { calls++; return calls > 1 }
	if dir, _ := RelocateSQLite(); dir != "" {
		t.Fatalf("dir = %q, want none", dir)
	}
	for _, p := range []string{dst, dst + ".tmp"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s exists: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(src, "state_5.sqlite")); err != nil {
		t.Fatalf("keep-side database moved: %v", err)
	}
}

func TestIsCodexProcessSeesTheLauncher(t *testing.T) {
	for cmdline, want := range map[string]bool{
		"node\x00/home/u/.local/bin/codex\x00app-server\x00":                   true,
		"node\x00/usr/lib/node_modules/@openai/codex/bin/codex.js\x00exec\x00": true,
		"/usr/bin/codex\x00login\x00":                                          true,
		"node\x00/srv/app.js\x00codex\x00":                                     false, // an argument, not the program
		"bash\x00-c\x00echo codex\x00":                                         false,
	} {
		proc := t.TempDir()
		writeFile(t, filepath.Join(proc, "cmdline"), cmdline)
		if got := isCodexProcess(proc); got != want {
			t.Errorf("%q: got %v, want %v", cmdline, got, want)
		}
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
		{"# documentation uses \"\"\" here\nsqlite_home = \"/local\"\n", true}, // quotes in a comment
		{"dirs = [\n  [\"a\"]\n]\nsqlite_home = \"/local\"\n", true},           // last nested row, no comma
		{"a = \"[x]\" # [y]\nsqlite_home = \"/x\"\n", true},
		{"a = 'C:\\\\\"' \nsqlite_home = \"/x\"\n", true}, // literal strings have no escapes
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
