package routegw

import (
	"bufio"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Caller binding for a harness that can only speak TCP (ADR 0128 P2c). opencode 2.2.6 cannot
// reach a Unix socket: its Bedrock provider takes an http(s) base URL only (measured: a unix://
// URL is refused, "protocol must be http:, https: or s3:"). So the gateway also listens on a
// loopback port, and a connection there is attributed the way the kernel can prove it: the
// client's end of the connection is a socket in /proc/net/tcp (its local address is the peer's
// address the gateway sees, its remote address the gateway's own), the socket's inode is held
// open by exactly one process, and that process is the caller. The rest of the check is the
// Unix socket's: the process is pinned by its start time and must be in the owning session's
// launch-pane tree.
//
// What this proves is weaker than SO_PEERCRED in one way: the holder is read when the gateway
// accepts, not when the client connected, so a client that hands its connected socket to another
// process before the accept is attributed to that process (the same user can do the equivalent
// on the Unix socket with SCM_RIGHTS). An inode held by more than one process (a socket
// inherited across fork) is refused, never guessed.

// tcpPeer resolves the process at the other end of a loopback TCP connection whose gateway end
// is local and whose client end is remote, reading procRoot.
func tcpPeer(procRoot string, local, remote *net.TCPAddr) (procID, bool) {
	if local == nil || remote == nil || !remote.IP.IsLoopback() {
		return procID{}, false
	}
	inode := ""
	for _, f := range []string{"tcp", "tcp6"} {
		if ino, ok := findTCPSocket(filepath.Join(procRoot, "net", f), remote, local); ok {
			if inode != "" {
				return procID{}, false
			}
			inode = ino
		}
	}
	if inode == "" {
		return procID{}, false
	}
	return socketHolder(procRoot, inode)
}

// findTCPSocket finds the established socket whose local address is local and whose remote
// address is remote in one /proc/net/tcp{,6} table, and returns its inode. A second match
// refuses: the table is ambiguous then.
func findTCPSocket(path string, local, remote *net.TCPAddr) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	found := ""
	for sc.Scan() {
		c := strings.Fields(sc.Text())
		// sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode
		if len(c) < 10 || c[3] != "01" { // 01 = TCP_ESTABLISHED
			continue
		}
		la, ok1 := parseProcAddr(c[1])
		ra, ok2 := parseProcAddr(c[2])
		if !ok1 || !ok2 || !sameAddr(la, local) || !sameAddr(ra, remote) {
			continue
		}
		if c[9] == "0" || found != "" {
			return "", false
		}
		found = c[9]
	}
	return found, found != ""
}

// parseProcAddr reads an "IP:PORT" of /proc/net/tcp{,6}: the IP in hex, as the kernel stores it
// (each 32-bit word in host byte order, little-endian on every platform the Agent runs on), the
// port in hex.
func parseProcAddr(s string) (*net.TCPAddr, bool) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return nil, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return nil, false
	}
	raw, err := hex.DecodeString(h)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return nil, false
	}
	ip := make(net.IP, len(raw))
	for w := 0; w < len(raw); w += 4 {
		ip[w], ip[w+1], ip[w+2], ip[w+3] = raw[w+3], raw[w+2], raw[w+1], raw[w]
	}
	return &net.TCPAddr{IP: ip, Port: int(port)}, true
}

// sameAddr compares two addresses, an IPv4 address equal to its IPv4-mapped IPv6 form.
func sameAddr(a, b *net.TCPAddr) bool { return a.Port == b.Port && a.IP.Equal(b.IP) }

// socketHolder is the one process that holds socket inode open, reading every process's fd
// table under procRoot. Zero holders or more than one refuse.
//
// The holder is pinned as a process, not a pid: its start time is read before its fd table and
// again after, and both reads must agree. A process that exited after its fd table was read, and
// whose pid a later process (say, a new child in the owner's tree) took, would otherwise lend the
// old socket's evidence to the new process's start time.
func socketHolder(procRoot, inode string) (procID, bool) {
	want := "socket:[" + inode + "]"
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return procID{}, false
	}
	var holder procID
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		before, ok := readProc(procRoot, pid)
		if !ok {
			continue
		}
		if !holdsSocket(filepath.Join(procRoot, e.Name(), "fd"), want) {
			continue
		}
		afterFDScan(pid)
		after, ok := readProc(procRoot, pid)
		if !ok || after.StartTicks != before.StartTicks {
			return procID{}, false
		}
		if holder.pid != 0 && holder.pid != pid {
			return procID{}, false
		}
		holder = procID{pid: pid, start: before.StartTicks}
	}
	return holder, holder.pid != 0
}

// afterFDScan runs between reading a holder's fd table and re-reading its start time; a test
// replaces the process there.
var afterFDScan = func(int) {}

// holdsSocket reports whether the fd table at fdDir has want open. An unreadable table (another
// user's process, or one that is gone) cannot hold this user's socket here.
func holdsSocket(fdDir, want string) bool {
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		if l, err := os.Readlink(filepath.Join(fdDir, fd.Name())); err == nil && l == want {
			return true
		}
	}
	return false
}
