package browserx

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// A Chromium remote-debugging port is NOT "first process wins, the rest fail".
// Measured (docs/log/53 §53.16): launching a second Chromium with the same
// `--remote-debugging-port=P --remote-debugging-address=127.0.0.1` while another
// one already holds it does not fail and does not warn — the first keeps
// 127.0.0.1:P (IPv4) and the second silently binds [::1]:P (IPv6) and runs on.
//
// Discovery here always dials 127.0.0.1 (browser_attachment_types.go), so the
// session that launched the SECOND browser gets the FIRST one's targets and can
// attach to a Page it never opened — in a shared Workspace container that is
// another session's, possibly logged-in, browser.
//
// Loopback is per network namespace, so this never crosses containers: another
// user's Workspace is unreachable from 127.0.0.1 here, and the attach API takes
// no host. The exposure is strictly session-vs-session inside one container,
// which is exactly what this file detects.
//
// /proc/net/tcp{,6} lists every listening socket in our namespace with its inode;
// /proc/<pid>/fd/* maps those inodes back to the owning process. Both are
// namespace-scoped, so the answer covers this container and nothing else.

const (
	procNetTCP4 = "/proc/net/tcp"
	procNetTCP6 = "/proc/net/tcp6"
	procRoot    = "/proc"
	// TCP_LISTEN in the st column of /proc/net/tcp{,6}.
	procTCPListen = "0A"
)

type cdpPortListener struct {
	PID         int
	IPv6        bool
	UserDataDir string
}

// lookupCDPPortListeners is the seam the ambiguity guard calls, so tests can
// exercise the decision without having to own two processes on one port.
var lookupCDPPortListeners = cdpPortListeners

// listeningInodes returns the socket inodes of every LISTEN socket bound to port
// in this network namespace, with the address family that owns each one. The
// bool reports whether /proc could be read at all: false means "unknown", which
// callers must treat as "no opinion" rather than "no collision".
func listeningInodes(port int) (map[uint64]bool, bool) {
	inodes := map[uint64]bool{}
	read := false
	for _, spec := range []struct {
		path string
		ipv6 bool
	}{{procNetTCP4, false}, {procNetTCP6, true}} {
		raw, err := os.ReadFile(spec.path)
		if err != nil {
			continue
		}
		read = true
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != procTCPListen {
				continue
			}
			local := fields[1]
			colon := strings.LastIndex(local, ":")
			if colon < 0 {
				continue
			}
			bound, err := strconv.ParseUint(local[colon+1:], 16, 32)
			if err != nil || int(bound) != port {
				continue
			}
			inode, err := strconv.ParseUint(fields[9], 10, 64)
			if err != nil {
				continue
			}
			inodes[inode] = spec.ipv6
		}
	}
	return inodes, read
}

// cdpPortListeners reports which processes hold a LISTEN socket on port. An
// empty slice with ok=false means the question could not be answered (no /proc,
// or the sockets could not be attributed) — callers must not read that as "only
// one listener".
func cdpPortListeners(port int) ([]cdpPortListener, bool) {
	inodes, read := listeningInodes(port)
	if !read || len(inodes) == 0 {
		return nil, false
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, false
	}
	owners := map[int]cdpPortListener{}
	seen := map[uint64]bool{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(procRoot, entry.Name(), "fd"))
		if err != nil {
			continue // another user's process, or it exited mid-scan
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(procRoot, entry.Name(), "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") || !strings.HasSuffix(link, "]") {
				continue
			}
			inode, err := strconv.ParseUint(link[len("socket:["):len(link)-1], 10, 64)
			if err != nil {
				continue
			}
			ipv6, listening := inodes[inode]
			if !listening {
				continue
			}
			seen[inode] = true
			owner, known := owners[pid]
			owner.PID = pid
			owner.IPv6 = owner.IPv6 || ipv6
			if !known {
				owner.UserDataDir = procUserDataDir(pid)
			}
			owners[pid] = owner
		}
	}
	// An unattributed socket means someone else's listener we cannot name. That
	// is still a listener, so the caller must not conclude "exactly one owner".
	if len(seen) != len(inodes) {
		return nil, false
	}
	list := make([]cdpPortListener, 0, len(owners))
	for _, owner := range owners {
		list = append(list, owner)
	}
	// Map order is random; a stable message keeps the error (and its test) readable.
	sort.Slice(list, func(i, j int) bool { return list[i].PID < list[j].PID })
	return list, true
}

// procUserDataDir extracts --user-data-dir from a process command line so an
// ambiguity error can name the profile a human can recognise. The rest of the
// command line is deliberately NOT surfaced: it may carry URLs or tokens.
func procUserDataDir(pid int) string {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	for _, arg := range strings.Split(string(raw), "\x00") {
		if dir, ok := strings.CutPrefix(arg, "--user-data-dir="); ok {
			return truncateBrowserText(dir, BrowserAttachmentMaxLabel)
		}
	}
	return ""
}

// describeCDPPortListeners renders the owning profiles for an error message.
func describeCDPPortListeners(listeners []cdpPortListener) string {
	parts := make([]string, 0, len(listeners))
	for _, listener := range listeners {
		family := "IPv4"
		if listener.IPv6 {
			family = "IPv6"
		}
		part := family + " pid=" + strconv.Itoa(listener.PID)
		if listener.UserDataDir != "" {
			part += " user-data-dir=" + listener.UserDataDir
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}
