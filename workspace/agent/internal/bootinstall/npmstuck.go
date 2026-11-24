package bootinstall

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// errNPMStuck: a process of an npm install was still running after SIGKILL and the grace (a
// write blocked in uninterruptible I/O finishes before the kill lands). It is recorded in the
// stuck file, and lockNPM refuses every destructive step until it is gone: a restore made now
// could be overwritten by the write it is still finishing.
var errNPMStuck = errors.New("a process of the npm install did not stop after SIGKILL")

// npmProc names one process for good: a pid is reused, a pid with its start time is not.
type npmProc struct {
	pid   int
	start uint64
}

// npmStuckPath records the processes behind errNPMStuck, one "<scope> <pid> <start>" per line.
// It outlives this process on purpose: the next start must not restore under them either.
func (e Env) npmStuckPath() string { return filepath.Join(e.Home, ".local", ".af-npm-stuck") }

// procScope names where a pid and start time mean one process: this boot (start times count from
// it) and this pid namespace (a new container numbers its processes afresh). "?" when either
// cannot be read: such an entry is never taken for another scope's.
func procScope() string {
	b, err := os.ReadFile(filepath.Join(procRoot, "sys", "kernel", "random", "boot_id"))
	ns, nerr := os.Readlink(filepath.Join(procRoot, "self", "ns", "pid"))
	boot := strings.TrimSpace(string(b))
	if err != nil || nerr != nil || boot == "" || strings.ContainsAny(ns, " \t\n") {
		return "?"
	}
	return boot + "/" + ns
}

// procRuns reports whether p is still the same process and has a thread that has not exited. A
// zombie whose threads have all exited writes nothing and does not count; one whose leader thread
// alone exited still runs.
func procRuns(p npmProc) bool {
	st, ok := readStat(p.pid)
	if !ok || st.start != p.start {
		return false
	}
	return st.live || threadsRun(p.pid)
}

var reStuck = regexp.MustCompile(`(?m)^stuck ([0-9]+) ([0-9]+)$`)

// parseStuck reads the supervisor's report (reportStuck).
func parseStuck(s string) []npmProc {
	var out []npmProc
	for _, m := range reStuck.FindAllStringSubmatch(s, -1) {
		pid, err1 := strconv.Atoi(m[1])
		start, err2 := strconv.ParseUint(m[2], 10, 64)
		if err1 == nil && err2 == nil && pid > 0 {
			out = append(out, npmProc{pid, start})
		}
	}
	return out
}

// npmStuckMaxBytes bounds the record: about 100,000 entries, far more processes than one install
// leaves, and little enough to read under the lock.
const npmStuckMaxBytes = 8 << 20

// npmReadStuck returns the recorded processes that may belong to this scope. A missing file is
// none; any other failure (a file that is not a regular one, such as a FIFO an open would block
// on, included) is returned, and the caller must take it as "may still run".
func (e Env) npmReadStuck() ([]npmProc, error) {
	f, err := os.OpenFile(e.npmStuckPath(), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", e.npmStuckPath())
	}
	// Read whole, or not at all: an entry dropped by a cut would let a restore run under it, and
	// a file past any real record (a sparse one, say) must not cost the memory to read it.
	b, err := io.ReadAll(io.LimitReader(f, npmStuckMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > npmStuckMaxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", e.npmStuckPath(), npmStuckMaxBytes)
	}
	scope := procScope()
	var out []npmProc
	for _, l := range strings.Split(string(b), "\n") {
		fl := strings.Fields(l)
		if len(fl) != 3 {
			continue
		}
		// Only two known, different scopes rule an entry out.
		if fl[0] != scope && fl[0] != "?" && scope != "?" {
			continue
		}
		pid, err1 := strconv.Atoi(fl[1])
		start, err2 := strconv.ParseUint(fl[2], 10, 64)
		if err1 == nil && err2 == nil && pid > 0 {
			out = append(out, npmProc{pid, start})
		}
	}
	return out, nil
}

// npmRecordStuck adds the processes of ps that still run to the stuck file, keeping the recorded
// ones that still run, and reports whether any of ps does. The caller holds the npm lock.
//
// A procfs of another pid namespace names other processes: nothing is recorded from it, as
// before this record existed, rather than a guard that waits for an unrelated process.
func (e Env) npmRecordStuck(ps []npmProc) (bool, error) {
	if len(ps) == 0 || !procfsOurs() {
		return false, nil
	}
	var runs []npmProc
	seen := map[npmProc]bool{}
	for _, p := range ps {
		if !seen[p] && procRuns(p) {
			seen[p] = true
			runs = append(runs, p)
		}
	}
	if len(runs) == 0 {
		return false, nil
	}
	old, _ := e.npmReadStuck() // an unreadable file is replaced: the new entries must land
	for _, p := range old {
		if !seen[p] && procRuns(p) {
			seen[p] = true
			runs = append(runs, p)
		}
	}
	scope := procScope()
	var b strings.Builder
	for _, p := range runs {
		fmt.Fprintf(&b, "%s %d %d\n", scope, p.pid, p.start)
	}
	if err := os.MkdirAll(filepath.Dir(e.npmStuckPath()), 0o755); err != nil {
		return true, err
	}
	f, err := os.CreateTemp(filepath.Dir(e.npmStuckPath()), ".af-npm-stuck.tmp.")
	if err != nil {
		return true, err
	}
	tmp := f.Name()
	_, werr := f.WriteString(b.String())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, e.npmStuckPath())
	}
	if werr != nil {
		os.Remove(tmp)
	}
	return true, werr
}

// npmStuckCheck returns errNPMStuck (wrapped) while a recorded process still runs, or the file
// cannot be read. When every recorded process is gone the file goes. The caller holds the npm
// lock, so no install records a new one meanwhile.
func (e Env) npmStuckCheck() error {
	if _, err := os.Lstat(e.npmStuckPath()); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	ps, err := e.npmReadStuck()
	if err != nil {
		return fmt.Errorf("%w: cannot read %s: %v", errNPMStuck, e.npmStuckPath(), err)
	}
	if len(ps) > 0 && !procfsOurs() {
		// Nothing here can tell these processes apart (see npmRecordStuck): the guard is off, as
		// before it existed, rather than waiting on whatever the numbers name in this procfs.
		return nil
	}
	for _, p := range ps {
		if procRuns(p) {
			return fmt.Errorf("%w: pid %d still runs (recorded in %s)", errNPMStuck, p.pid, e.npmStuckPath())
		}
	}
	// Every entry is gone, so a file that cannot be removed blocks nothing: the next look finds
	// them gone too.
	_ = os.Remove(e.npmStuckPath())
	return nil
}
