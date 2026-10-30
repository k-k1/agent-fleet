package codex

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
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

// Seams for tests: the real ones read the mount type and /proc.
var (
	onNFS        = statfsIsNFS
	codexRunning = anyCodexProcess
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

var sqliteHomeKeyRe = regexp.MustCompile(`^sqlite_home\s*=`)

// configSetsSQLiteHome reports whether config.toml sets sqlite_home at the top level, the
// manual workaround from #3013. Precedence between that key and SQLiteHomeEnv is not something
// to depend on, so a user who set it keeps exactly what they set.
func configSetsSQLiteHome(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			return false // a table header ends the top level
		}
		if sqliteHomeKeyRe.MatchString(line) {
			return true
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
	if err == nil {
		err = syncDir(filepath.Dir(dst))
	}
	if err != nil {
		_ = os.RemoveAll(stage)
		return 0, err
	}
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
// every boot after the move, because the rename after the staging rename can be interrupted.
// A file whose retired name is already taken is left alone: it was created after the move by
// a codex that did not see SQLiteHomeEnv, and overwriting the retired copy would lose the
// pre-migration bytes instead.
func retireLeftovers(src string) string {
	entries, err := os.ReadDir(src)
	if err != nil {
		return ""
	}
	var kept []string
	retired := 0
	for _, e := range entries {
		if _, rt := dbFile(e.Name()); !rt || !e.Type().IsRegular() {
			continue
		}
		from := filepath.Join(src, e.Name())
		to := from + retiredSuffix
		if _, err := os.Lstat(to); err == nil {
			kept = append(kept, e.Name())
			continue
		}
		if err := os.Rename(from, to); err != nil {
			kept = append(kept, e.Name())
			continue
		}
		retired++
	}
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

// anyCodexProcess reports whether a native codex binary is running in this container. The npm
// launcher is node; the process holding the databases is the binary it execs. When in doubt
// (no /proc) it answers true, which only postpones the move.
func anyCodexProcess() bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
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
			return true
		}
	}
	return false
}
