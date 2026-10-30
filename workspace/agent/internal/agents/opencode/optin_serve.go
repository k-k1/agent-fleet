package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Keeping the supervised serve in step with the opt-in switch (optin.go). A daemon is bound to
// the opencode it was started as: flipping the switch must replace it, and nothing else may
// ever talk to the old one as if it were the new.

// serveRecord says which opencode the daemon af started is, and which process it was. It exists
// only while the opt-in is the way the daemon was started, so a workspace that never opts in
// never gets the file. Start, Exe and Boot identify the process instance: a pid alone is
// recycled.
type serveRecord struct {
	PID   int    `json:"pid"`
	Key   string `json:"key"`
	Start uint64 `json:"start"` // /proc/<pid>/stat field 22, clock ticks since boot
	Exe   string `json:"exe"`
	Boot  string `json:"boot"`
	Port  string `json:"port"`
}

func serveRecordFile() string { return filepath.Join(paths.AgentStateDir(), "opencode-serve.json") }

// procStart is the start time of pid in clock ticks since boot; 0 when unreadable.
func procStart(pid int) uint64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// comm (field 2) may hold spaces and parentheses: fields resume after the last ')'.
	i := strings.LastIndex(string(b), ") ")
	if i < 0 {
		return 0
	}
	f := strings.Fields(string(b)[i+2:])
	if len(f) < 20 { // field 22 overall = index 19 here (fields start at 3)
		return 0
	}
	n, _ := strconv.ParseUint(f[19], 10, 64)
	return n
}

func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// servePort is the port of the configured serve address.
func servePort(addr string) string {
	i := strings.LastIndex(strings.TrimSuffix(addr, "/"), ":")
	if i < 0 {
		return ""
	}
	return strings.TrimSuffix(addr, "/")[i+1:]
}

// noteServeStart records the daemon just started, or removes the record when it was started the
// default way.
func noteServeStart(pid int, key string) {
	f := serveRecordFile()
	if key == "" {
		_ = os.Remove(f)
		return
	}
	exe, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	rec := serveRecord{PID: pid, Key: key, Start: procStart(pid), Exe: exe, Boot: bootID(), Port: servePort(serveAddr())}
	b, err := json.Marshal(rec)
	if err == nil {
		err = writeAtomic(f, b, 0o600)
	}
	if err != nil {
		log.Printf("opencode serve: could not record how the daemon was started, a restarted Agent cannot tell it apart: %v", err)
	}
}

// isRecordedServe says whether pid is still the process that record describes: the same boot,
// the same start time, the same executable, and an argv that is exactly an `serve` on the
// recorded port (NUL-separated tokens, not a substring of the command line).
func isRecordedServe(r serveRecord) bool {
	if r.PID <= 0 || r.Key == "" || r.Start == 0 || r.Exe == "" || r.Boot == "" || r.Boot != bootID() {
		return false
	}
	pid := strconv.Itoa(r.PID)
	if procStart(r.PID) != r.Start {
		return false
	}
	if exe, err := os.Readlink("/proc/" + pid + "/exe"); err != nil || exe != r.Exe {
		return false
	}
	raw, err := os.ReadFile("/proc/" + pid + "/cmdline")
	if err != nil {
		return false
	}
	args := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	serve, port := false, false
	for i, a := range args {
		if i > 0 && a == "serve" {
			serve = true
		}
		if a == "--port" && i+1 < len(args) && args[i+1] == r.Port {
			port = true
		}
	}
	return serve && port && r.Port != ""
}

// readRecord is the record file, whatever process it names.
func readRecord() (serveRecord, bool) {
	b, err := os.ReadFile(serveRecordFile())
	if err != nil {
		return serveRecord{}, false
	}
	var r serveRecord
	if json.Unmarshal(b, &r) != nil {
		return serveRecord{}, false
	}
	return r, true
}

// recordedServe is the record, and false when there is none or its process is not that
// `opencode serve` any more (a recycled pid must never be signalled).
func recordedServe() (serveRecord, bool) {
	r, ok := readRecord()
	if !ok || !isRecordedServe(r) {
		return serveRecord{}, false
	}
	return r, true
}

// signalRecorded sends sig to the recorded process through a pidfd opened BEFORE the identity is
// checked, so the pid cannot be recycled between the check and the signal.
func signalRecorded(r serveRecord, sig syscall.Signal) bool {
	const sysPidfdOpen, sysPidfdSendSignal = 434, 424 // the same number on every architecture
	fd, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(r.PID), 0, 0)
	if errno != 0 {
		return false
	}
	defer syscall.Close(int(fd))
	if !isRecordedServe(r) {
		return false
	}
	_, _, errno = syscall.Syscall6(sysPidfdSendSignal, fd, uintptr(sig), 0, 0, 0, 0)
	return errno == 0
}

// adoptedKey is how a daemon found already running was started: by the record when there is
// one, else the default way (an Agent without the opt-in could only start it that way).
func adoptedKey() string {
	if r, ok := recordedServe(); ok {
		return r.Key
	}
	return ""
}

// retireStale replaces a daemon that was started under the other setting of the switch. It
// runs before ensure takes the lock because the replacement drains (up to the drain timeout),
// which must not hold the lock the Console's polling needs. Restart is the same path a key
// change takes: running turns finish or are aborted, handles are re-resumed.
func (s *Supervisor) retireStale() error { return s.retireStaleFor(launchKey()) }

// retireStaleFor is retireStale against the key of a reading the caller already took.
func (s *Supervisor) retireStaleFor(want string) error {
	s.mu.Lock()
	up, have, owned := s.up, s.key, s.cmd != nil
	s.mu.Unlock()
	if !up {
		// Not supervised, but one may still be listening from before an Agent restart.
		if _, k := readRecordKey(); k == want || !healthy(serveAddr()) {
			return nil
		}
		have = adoptedKey()
		if have == want {
			return nil
		}
	} else if have == want {
		return nil
	}
	log.Printf("opencode serve: started as %q, now %q: replacing it", have, want)
	if owned {
		s.Restart("opencode major switched")
		return nil
	}
	return s.retireAdopted(serveAddr())
}

// readRecordKey reads the record's key without the identity check, for the cheap "is there
// anything to compare" test on every ensure.
func readRecordKey() (serveRecord, string) {
	r, _ := readRecord()
	return r, r.Key
}

// retireAdopted ends a daemon this Agent did not start itself, by the pid it recorded. Without a
// record the daemon cannot be signalled, and the caller says so rather than attach to the wrong
// opencode.
func (s *Supervisor) retireAdopted(addr string) error {
	r, ok := recordedServe()
	if !ok {
		return fmt.Errorf("opencode serve が %s で別のメジャー版のまま動いています。止めてからやり直してください", addr)
	}
	s.mu.Lock()
	s.stopping, s.up = true, false
	s.mu.Unlock()
	// Drained whether or not this Agent supervised the daemon: after an Agent restart the daemon
	// is healthy and its Terminal panes still run turns on it.
	s.drain(addr)
	signalRecorded(r, syscall.SIGTERM)
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && healthy(addr) {
		time.Sleep(100 * time.Millisecond)
	}
	if healthy(addr) {
		signalRecorded(r, syscall.SIGKILL)
		time.Sleep(500 * time.Millisecond)
	}
	if healthy(addr) {
		return errors.New("opencode serve を止められませんでした")
	}
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
	recordLifecycle("stopped", s.Generation(), "opencode major switched")
	goReconcile("opencode major switched")
	return nil
}
