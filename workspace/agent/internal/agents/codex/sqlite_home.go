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
	"time"

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

// retiredSuffix, plus the move's stamp, marks a keep-side database file that was copied to the
// home volume. It keeps the bytes for a manual rollback while making sure a codex that misses
// SQLiteHomeEnv (an older Agent after a rollback) does not reopen pre-migration data as current.
// The stamp keeps a second move (after Clean home) from renaming over the first one's copies.
const retiredSuffix = ".moved-to-local-"

// retireManifest lists, inside the destination, the keep-side files the move still has to
// rename. It is written into the staging directory, so it appears together with the marker,
// and removed once every rename is done: a crash between renames resumes on the next boot
// instead of leaving a database retired without its WAL.
const retireManifest = ".af-retire-pending"

// SQLiteHomeDir is where the databases go: beside the Agent's own state, which is on the home
// volume and already denied to the file browser (fs.go).
func SQLiteHomeDir() string {
	return filepath.Join(paths.AgentStateDir(), "codex-sqlite")
}

// Seams for tests: the real ones read the mount type, /proc and the tmux server, sync the
// destination's parent and stamp the move.
var (
	onNFS        = statfsIsNFS
	codexRunning = func() bool { return len(codexPIDs()) > 0 }
	tmuxUp       = func() bool { return tmuxx.Cmd("list-sessions").Run() == nil }
	parentSync   = syncDir
	now          = time.Now
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
		return dst, retirePending(src, dst)
	}
	// Only where it pays: ~/.codex on NFS and the home itself local. On Fargate the whole home
	// is EFS, and docker / native keep ~/.codex local, so both stay as they are. Moving there
	// would only change what Clean home loses (it keeps ~/.codex, not ~/.local/state).
	if !onNFS(src) || onNFS(paths.HomeDir()) {
		return "", ""
	}
	if why := busy(); why != "" {
		return "", "codex sqlite: " + why + "; the move off NFS waits for the next start"
	}
	n, durable, err := copyDatabases(src, dst)
	if err != nil {
		return "", fmt.Sprintf("codex sqlite: not moved off NFS, staying in %s: %v", src, err)
	}
	moved := fmt.Sprintf("codex sqlite: moved %d file(s) from %s to %s", n, src, dst)
	if !durable {
		// dst is in use from here on, but its rename may not survive a host crash yet; the
		// keep-side originals stay until a later boot finds dst and the manifest still there.
		return dst, moved + "; keep-side files left until the next start (directory sync failed)"
	}
	if r := retirePending(src, dst); r != "" {
		moved += "; " + r
	}
	return dst, moved
}

// busy reports why the keep-side databases cannot be touched now, or "".
func busy() string {
	if codexRunning() {
		return "a codex process is running"
	}
	// A shell already open in a pane predates this boot's environment, so a codex typed there
	// would open ~/.codex and create a second set of databases. The server only outlives the
	// Agent on an in-place restart; a container start has none, which is when the move happens.
	if tmuxUp() {
		return "tmux sessions from before this boot are open"
	}
	return ""
}

var sqliteHomeKeyRe = regexp.MustCompile(`^(?:sqlite_home|"sqlite_home"|'sqlite_home')\s*=`)

// configSetsSQLiteHome reports whether config.toml sets sqlite_home at the top level, the
// manual workaround from #3013. Precedence between that key and SQLiteHomeEnv is not something
// to depend on, so a user who set it keeps exactly what they set.
//
// The module has no TOML parser, so this is a small lexer over what can hide a line's start:
// strings (basic, literal, both multi-line forms), comments, and open arrays. A line that
// starts outside all of them is either a key (checked) or, starting with "[", a table header,
// which ends the top level.
func configSetsSQLiteHome(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lx tomlLexer
	for sc.Scan() {
		line := sc.Text()
		if lx.atStatement() {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "[") {
				return false
			}
			if sqliteHomeKeyRe.MatchString(t) {
				return true
			}
		}
		lx.feed(line)
	}
	return false
}

// tomlLexer carries the state that spans lines: an open multi-line string and array depth.
type tomlLexer struct {
	multi string // `"""` or `'''` while inside one
	depth int    // open [ of array values
}

func (l *tomlLexer) atStatement() bool { return l.multi == "" && l.depth == 0 }

func (l *tomlLexer) feed(line string) {
	for i := 0; i < len(line); i++ {
		rest := line[i:]
		switch {
		case l.multi != "":
			if l.multi == `"""` && line[i] == '\\' {
				i++
			} else if strings.HasPrefix(rest, l.multi) {
				l.multi, i = "", i+2
			}
		case line[i] == '#':
			return
		case strings.HasPrefix(rest, `"""`), strings.HasPrefix(rest, `'''`):
			l.multi, i = rest[:3], i+2
		case line[i] == '"':
			for i++; i < len(line) && line[i] != '"'; i++ {
				if line[i] == '\\' {
					i++
				}
			}
		case line[i] == '\'':
			if j := strings.IndexByte(line[i+1:], '\''); j >= 0 {
				i += j + 1
			} else {
				i = len(line)
			}
		case line[i] == '[':
			l.depth++
		case line[i] == ']' && l.depth > 0:
			l.depth--
		}
	}
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

// copyDatabases copies every database file of src into a staging directory together with the
// retire manifest, syncs it, and renames it to dst. A database and its -wal must arrive
// together, which per-file renames into dst could not promise. durable=false means the rename
// happened but the directory holding it could not be synced.
func copyDatabases(src, dst string) (n int, durable bool, err error) {
	stage := dst + ".tmp"
	if err := os.RemoveAll(stage); err != nil { // an earlier, interrupted attempt
		return 0, false, err
	}
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return 0, false, err
	}
	n, retire, err := copyInto(src, stage)
	if err == nil {
		manifest := fmt.Sprintf("%d\n%s", now().Unix(), strings.Join(retire, "\n"))
		err = writeSynced(filepath.Join(stage, retireManifest), []byte(manifest))
	}
	if err == nil {
		err = syncDir(stage)
	}
	if err == nil {
		// A codex that started during the copy (its npm launcher was still node when probed)
		// may have written the source since: discard rather than publish a stale copy.
		if why := busy(); why != "" {
			err = fmt.Errorf("%s since the copy began", why)
		}
	}
	if err == nil {
		err = os.Rename(stage, dst)
	}
	if err != nil {
		_ = os.RemoveAll(stage)
		return 0, false, err
	}
	// Past the rename dst is the marker and every later boot uses it, so a failed sync is not
	// a failed move: falling back would run this boot on the keep-side copy and the next one on
	// dst without those writes. It only holds back the retire step.
	return n, parentSync(filepath.Dir(dst)) == nil, nil
}

// copyInto copies the database files into stage and returns the keep-side names to retire.
func copyInto(src, stage string) (n int, retire []string, err error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil, nil // codex never ran: nothing to carry, the marker is still worth setting
		}
		return 0, nil, err
	}
	for _, e := range entries {
		cp, rt := dbFile(e.Name())
		if !rt || !e.Type().IsRegular() {
			continue
		}
		retire = append(retire, e.Name())
		if !cp {
			continue
		}
		if err := copySynced(filepath.Join(src, e.Name()), filepath.Join(stage, e.Name())); err != nil {
			return 0, nil, err
		}
		n++
	}
	return n, retire, nil
}

func writeSynced(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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

// retirePending renames the keep-side files the manifest in dst lists, then removes the
// manifest. A rename that fails stops the step and keeps the manifest, so the next boot carries
// on from there. Database files a codex outside af created at the old path after the move are
// not in the manifest and are left alone: renaming a live database would take it from that
// process, and they are named in the log instead.
func retirePending(src, dst string) string {
	manifest := filepath.Join(dst, retireManifest)
	b, err := os.ReadFile(manifest)
	if err != nil {
		if os.IsNotExist(err) {
			return strangers(src)
		}
		return "codex sqlite: reading the retire manifest: " + err.Error()
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	suffix := retiredSuffix + lines[0]
	retired := 0
	for _, name := range lines[1:] {
		if name == "" || strings.ContainsRune(name, '/') {
			continue
		}
		from := filepath.Join(src, name)
		if _, err := os.Lstat(from); os.IsNotExist(err) {
			continue // renamed by an earlier, interrupted boot
		}
		if err := os.Rename(from, from+suffix); err != nil {
			return fmt.Sprintf("codex sqlite: retired %d keep-side file(s), stopped at %s (resumes next start): %v", retired, name, err)
		}
		retired++
	}
	if err := os.Remove(manifest); err != nil {
		return "codex sqlite: removing the retire manifest: " + err.Error()
	}
	note := ""
	if retired > 0 {
		note = fmt.Sprintf("codex sqlite: retired %d keep-side file(s) as *%s", retired, suffix)
	}
	if s := strangers(src); s != "" {
		if note != "" {
			note += "; "
		}
		note += s
	}
	return note
}

// strangers names database files at the keep-side path that the move did not put there.
func strangers(src string) string {
	entries, err := os.ReadDir(src)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if _, rt := dbFile(e.Name()); rt && e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("codex sqlite: left in %s (a codex outside af created them after the move): %s",
		src, strings.Join(names, ", "))
}

// codexPIDs lists the codex processes running in this container: the native binary that holds
// the databases, and the npm launcher (node running bin/codex.js) that is about to start one.
// Without /proc it answers with a placeholder pid, which only postpones the move.
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
		if isCodexProcess(filepath.Join("/proc", e.Name())) {
			pids = append(pids, pid)
		}
	}
	return pids
}

func isCodexProcess(proc string) bool {
	if exe, err := os.Readlink(filepath.Join(proc, "exe")); err == nil &&
		filepath.Base(strings.TrimSuffix(exe, " (deleted)")) == "codex" {
		return true
	}
	b, err := os.ReadFile(filepath.Join(proc, "cmdline"))
	if err != nil {
		return false // gone, or not ours to read
	}
	argv := strings.Split(string(b), "\x00")
	for i := 0; i < len(argv) && i < 2; i++ {
		switch filepath.Base(argv[i]) {
		case "codex", "codex.js":
			return true
		}
	}
	return false
}
