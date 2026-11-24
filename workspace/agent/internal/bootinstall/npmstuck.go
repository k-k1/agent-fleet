package bootinstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// npmStuckPath records the processes behind errNPMStuck, one "<boot id> <pid> <start>" per line.
// It outlives this process on purpose: the next start must not restore under them either.
func (e Env) npmStuckPath() string { return filepath.Join(e.Home, ".local", ".af-npm-stuck") }

// bootID tells this boot from the next: start times count from boot, so an entry of an earlier
// boot names nothing.
func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// procRuns reports whether p is still the same process and has a thread that has not exited. A
// zombie whose threads have all exited writes nothing and does not count; one whose leader thread
// alone exited still runs.
func procRuns(p npmProc) bool {
	st, ok := readStat(p.pid)
	if !ok || st.start != p.start {
		return false
	}
	if st.live {
		return true
	}
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", p.pid))
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		return true // cannot tell: assume it still runs
	}
	for _, d := range tasks {
		tid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		if t, ok := readStatFile(tid, fmt.Sprintf("/proc/%d/task/%d/stat", p.pid, tid)); ok && t.live {
			return true
		}
	}
	return false
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

// npmReadStuck returns the recorded processes of this boot. A missing file is none; any other
// read error is returned, and the caller must take it as "may still run".
func (e Env) npmReadStuck() ([]npmProc, error) {
	b, err := os.ReadFile(e.npmStuckPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	boot := bootID()
	var out []npmProc
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) != 3 || f[0] != boot {
			continue
		}
		pid, err1 := strconv.Atoi(f[1])
		start, err2 := strconv.ParseUint(f[2], 10, 64)
		if err1 == nil && err2 == nil && pid > 0 {
			out = append(out, npmProc{pid, start})
		}
	}
	return out, nil
}

// npmRecordStuck adds the processes of ps that still run to the stuck file, keeping the recorded
// ones that still run, and reports whether any of ps does. The caller holds the npm lock.
func (e Env) npmRecordStuck(ps []npmProc) (bool, error) {
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
	boot := bootID()
	var b strings.Builder
	for _, p := range runs {
		fmt.Fprintf(&b, "%s %d %d\n", boot, p.pid, p.start)
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
