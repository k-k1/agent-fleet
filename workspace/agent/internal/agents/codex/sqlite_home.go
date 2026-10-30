package codex

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// codex keeps its SQLite databases (state_5.sqlite and siblings) in CODEX_HOME unless told
// otherwise. On ecs-ec2 ~/.codex is a symlink onto the keep volume, an NFS mount with
// local_lock=none, so every fcntl lock SQLite's WAL takes on the -shm file is a server round
// trip: measured ~13 ms per lock/unlock against ~0 on local xfs, and with several sessions in
// the one shared app-server single stalls passed 30 s (#3013). The databases move to the home
// volume; the rollouts (sessions/*.jsonl) and auth.json stay on keep.

// SQLiteHomeEnv is codex's override for where the databases live.
const SQLiteHomeEnv = "CODEX_SQLITE_HOME"

// retiredSuffix marks a keep-side database file that was copied to the home volume. It keeps
// the bytes for a manual rollback while making sure a codex that misses SQLiteHomeEnv (an
// older Agent, a codex started outside af) does not reopen pre-migration data as current.
const retiredSuffix = ".moved-to-local"

// SQLiteHomeDir is where the databases go: beside the Agent's own state, which is on the home
// volume and already denied to the file browser (fs.go).
func SQLiteHomeDir() string {
	return filepath.Join(paths.AgentStateDir(), "codex-sqlite")
}

// Seams for tests: the real ones read the mount type, /proc and the tmux server.
var (
	onNFS        = statfsIsNFS
	codexRunning = func() bool { return len(codexPIDs()) > 0 }
	tmuxUp       = func() bool { return tmuxx.Cmd("list-sessions").Run() == nil }
)

// RelocateSQLite moves codex's databases off an NFS CODEX_HOME once and returns the directory
// to export as SQLiteHomeEnv, or "" to leave codex on its default. note is a line for the boot
// log, empty when there is nothing worth saying.
//
// It must run before any codex process starts: a copy taken while a process has the database
// open can miss committed pages, and that process would keep writing the keep-side copy. The
// existence of SQLiteHomeDir is the marker that the move finished — it is created by one
// rename of a fully written staging directory, so a crash leaves either nothing or all of it.
func RelocateSQLite() (dir, note string) {
	if v := os.Getenv(SQLiteHomeEnv); v != "" {
		return "", "" // the operator's own choice
	}
	src := paths.CodexHome()
	if configSetsSQLiteHome(filepath.Join(src, "config.toml")) {
		return "", "codex sqlite: config.toml sets sqlite_home; leaving it to that"
	}
	dst := SQLiteHomeDir()
	if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
		if codexRunning() {
			return dst, "" // never rename a database out from under a process that has it open
		}
		return dst, retireLeftovers(src)
	}
	// Only where it pays: ~/.codex on NFS and the home itself local. On Fargate the whole home
	// is EFS, and docker / native keep ~/.codex local, so both stay as they are. Moving there
	// would only change what Clean home loses (it keeps ~/.codex, not ~/.local/state).
	if !onNFS(src) || onNFS(paths.HomeDir()) {
		return "", ""
	}
	if codexRunning() {
		return "", "codex sqlite: a codex process is running; the move off NFS waits for the next start"
	}
	// A shell already open in a pane predates this boot's environment, so a codex typed there
	// would open ~/.codex and create a second set of databases. The server only outlives the
	// Agent on an in-place restart; a container start has none, which is when the move happens.
	if tmuxUp() {
		return "", "codex sqlite: tmux sessions from before this boot are open; the move off NFS waits for the next start"
	}
	n, err := copyDatabases(src, dst)
	if err != nil {
		return "", fmt.Sprintf("codex sqlite: not moved off NFS, staying in %s: %v", src, err)
	}
	moved := fmt.Sprintf("codex sqlite: moved %d file(s) from %s to %s", n, src, dst)
	if r := retireLeftovers(src); r != "" {
		moved += "; " + r
	}
	return dst, moved
}

var (
	sqliteHomeKeyRe = regexp.MustCompile(`^(?:sqlite_home|"sqlite_home"|'sqlite_home')\s*=`)
	tableHeaderRe   = regexp.MustCompile(`^\[\[?\s*[A-Za-z0-9_."' -]+\]\]?\s*(?:#.*)?$`)
)

// configSetsSQLiteHome reports whether config.toml sets sqlite_home at the top level, the
// manual workaround from #3013. Precedence between that key and SQLiteHomeEnv is not something
// to depend on, so a user who set it keeps exactly what they set. A line scan, not a TOML
// parser (the module has none): it skips multi-line strings, and a line counts as a table
// header only in the [name] / [[name]] shape, so an array value continued on a line that
// starts with "[" does not end the top level early.
func configSetsSQLiteHome(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	inString := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if inString != "" {
			if strings.Count(line, inString)%2 == 1 {
				inString = ""
			}
			continue
		}
		if tableHeaderRe.MatchString(line) {
			return false
		}
		if sqliteHomeKeyRe.MatchString(line) {
			return true
		}
		for _, q := range []string{`"""`, `'''`} {
			if strings.Count(line, q)%2 == 1 {
				inString = q
			}
		}
	}
	return false
}

// dbFile classifies a CODEX_HOME entry: copy says the bytes are needed at the destination,
// retire says the keep-side name has to go. -shm is retired but not copied: it is an index
// SQLite rebuilds from the -wal on open.
func dbFile(name string) (copy, retire bool) {
	switch {
	case strings.HasSuffix(name, ".sqlite"),
		strings.HasSuffix(name, ".sqlite-wal"),
		strings.HasSuffix(name, ".sqlite-journal"):
		return true, true
	case strings.HasSuffix(name, ".sqlite-shm"):
		return false, true
	}
	return false, false
}

// copyDatabases copies every database file of src into a staging directory, syncs it, and
// renames it to dst. A database and its -wal must arrive together, which per-file renames
// into dst could not promise.
func copyDatabases(src, dst string) (int, error) {
	stage := dst + ".tmp"
	if err := os.RemoveAll(stage); err != nil { // an earlier, interrupted attempt
		return 0, err
	}
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return 0, err
	}
	n, err := copyInto(src, stage)
	if err == nil {
		err = syncDir(stage)
	}
	if err == nil {
		err = os.Rename(stage, dst)
	}
	if err != nil {
		_ = os.RemoveAll(stage)
		return 0, err
	}
	// Past the rename dst is the marker, and every later boot uses it. Reporting a failed
	// directory sync as a failed move would run codex on the keep-side copy for this boot only,
	// and the next boot would come back to dst without those writes.
	_ = syncDir(filepath.Dir(dst))
	return n, nil
}

func copyInto(src, stage string) (int, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // codex never ran: nothing to carry, the marker is still worth setting
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if cp, _ := dbFile(e.Name()); !cp || !e.Type().IsRegular() {
			continue
		}
		if err := copySynced(filepath.Join(src, e.Name()), filepath.Join(stage, e.Name())); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

func copySynced(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// retireLeftovers renames the keep-side database files out of codex's way. It also runs on
// every boot after the move, because the renames after the staging rename can be interrupted.
//
// A database and its -wal / -shm / -journal move as one group or not at all. A group with any
// retired name already taken was created after the move by a codex that did not see
// SQLiteHomeEnv: renaming part of it would separate a live database from its WAL, and
// renaming over the retired copy would lose the pre-migration bytes.
func retireLeftovers(src string) string {
	entries, err := os.ReadDir(src)
	if err != nil {
		return ""
	}
	groups := map[string][]string{} // database file name -> its files present here
	for _, e := range entries {
		if _, rt := dbFile(e.Name()); !rt || !e.Type().IsRegular() {
			continue
		}
		base := e.Name()
		if i := strings.LastIndex(base, ".sqlite"); i >= 0 {
			base = base[:i+len(".sqlite")]
		}
		groups[base] = append(groups[base], e.Name())
	}
	var kept []string
	retired := 0
	for base, names := range groups {
		taken := false
		for _, n := range []string{"", "-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(filepath.Join(src, base+n+retiredSuffix)); err == nil {
				taken = true
			}
		}
		if taken {
			kept = append(kept, base)
			continue
		}
		for _, n := range names {
			from := filepath.Join(src, n)
			if err := os.Rename(from, from+retiredSuffix); err != nil {
				kept = append(kept, n)
				continue
			}
			retired++
		}
	}
	sort.Strings(kept)
	var parts []string
	if retired > 0 {
		parts = append(parts, fmt.Sprintf("retired %d keep-side file(s) as *%s", retired, retiredSuffix))
	}
	if len(kept) > 0 {
		parts = append(parts, fmt.Sprintf("left in %s (a codex outside af may still use them): %s", src, strings.Join(kept, ", ")))
	}
	if len(parts) == 0 {
		return ""
	}
	return "codex sqlite: " + strings.Join(parts, "; ")
}

// codexPIDs lists the native codex binaries running in this container. The npm launcher is
// node; the process holding the databases is the binary it execs. Without /proc it answers
// with a placeholder pid, which only postpones the move.
func codexPIDs() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return []int{-1}
	}
	var pids []int
	self := os.Getpid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil {
			continue // gone, or not ours to read
		}
		if filepath.Base(strings.TrimSuffix(exe, " (deleted)")) == "codex" {
			pids = append(pids, pid)
		}
	}
	return pids
}
