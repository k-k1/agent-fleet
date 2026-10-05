// Package listenports finds the TCP ports each session's own processes are listening on, so a
// session row can offer "open in pane" for the dev server that session started (#1062).
//
// The Workspace has no ss / lsof, and does not need them: /proc/net/tcp{,6} lists every
// listening socket of the container's network namespace with its inode, and /proc/<pid>/fd
// links name the socket inodes a process holds. What neither says is which SESSION a process
// belongs to, and that is the part that must not be guessed — another session's server shown
// on this row would send a person to the wrong app.
//
// A process belongs to a session when its environment carries that session's
// AF_SESSION_NAME. Every launch route hands the variable down (the tmux plan for terminal
// sessions, agents.WithSessionName for the per-session managed kinds) and every child
// inherits it, so this reaches a dev server that double-forked out of the pane's tree, which
// walking ppid links from the pane root would lose. Two things it deliberately does not
// reach: the tool commands of the SHARED managed daemons (codex app-server, opencode serve),
// whose environment names no session, and a process that cleared its own environment.
//
// Only sockets bound to 127.0.0.1 or to every interface (0.0.0.0 / ::) count: the browser
// pane opens http://127.0.0.1:{port}, and a server bound to ::1 alone cannot be reached there.
package listenports

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sessionEnvVar is agents.SessionNameEnvVar, repeated so this leaf package imports nothing of
// the Agent's.
const sessionEnvVar = "AF_SESSION_NAME"

// agentPort is the Agent's own listener. The preview refuses it (browserTarget), so a row
// must never offer it either.
const agentPort = 7700

// maxPortsPerSession caps one row. A session holding more listeners than this is running a
// cluster, and the row is not where that is read.
const maxPortsPerSession = 8

// listenState is TCP_LISTEN in the st column of /proc/net/tcp.
const listenState = "0A"

var loopback4 = net.IPv4(127, 0, 0, 1)

// Scan reads root (normally "/proc") once and returns session name → listening ports,
// ascending. Sessions with no listener are absent.
func Scan(root string) map[string][]int {
	inodes := map[string]int{}
	readListeners(filepath.Join(root, "net", "tcp"), inodes)
	readListeners(filepath.Join(root, "net", "tcp6"), inodes)
	out := map[string][]int{}
	// Nothing listens that a row could show: skip the per-process walk, which is the whole
	// cost of a scan.
	if len(inodes) == 0 {
		return out
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	seen := map[string]map[int]bool{}
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		pidDir := filepath.Join(root, e.Name())
		name := sessionOf(pidDir)
		if name == "" {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(pidDir, "fd"))
		if err != nil {
			continue // exited, or not ours to read
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(pidDir, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			port, ok := inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")]
			if !ok {
				continue
			}
			if seen[name] == nil {
				seen[name] = map[int]bool{}
			}
			seen[name][port] = true
		}
	}
	for name, ports := range seen {
		list := make([]int, 0, len(ports))
		for p := range ports {
			list = append(list, p)
		}
		sort.Ints(list)
		if len(list) > maxPortsPerSession {
			list = list[:maxPortsPerSession]
		}
		out[name] = list
	}
	return out
}

// sessionOf returns the AF_SESSION_NAME in a process's environment, "" when it has none or
// the environment cannot be read (another user's process, or one that has exited).
func sessionOf(pidDir string) string {
	b, err := os.ReadFile(filepath.Join(pidDir, "environ"))
	if err != nil {
		return ""
	}
	prefix := []byte(sessionEnvVar + "=")
	for _, kv := range bytes.Split(b, []byte{0}) {
		if bytes.HasPrefix(kv, prefix) {
			return string(kv[len(prefix):])
		}
	}
	return ""
}

// readListeners adds inode → port for each listening socket in one /proc/net/tcp* table whose
// bind address the browser pane can reach.
func readListeners(path string, into map[string]int) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	header := true
	for sc.Scan() {
		if header {
			header = false
			continue
		}
		// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode …
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 || fields[3] != listenState {
			continue
		}
		ip, port, ok := parseAddr(fields[1])
		if !ok || port == agentPort || !reachable(ip) {
			continue
		}
		if inode := fields[9]; inode != "0" {
			into[inode] = port
		}
	}
}

// reachable reports whether a server bound to ip answers on http://127.0.0.1.
func reachable(ip net.IP) bool {
	return ip.IsUnspecified() || ip.Equal(loopback4)
}

// parseAddr decodes the kernel's "HEXIP:HEXPORT". The address is the raw in_addr / in6_addr
// printed as 32-bit words in host byte order, so every 4-byte group is reversed on the
// little-endian machines a Workspace runs on (amd64, arm64).
func parseAddr(s string) (net.IP, int, bool) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil || port == 0 {
		return nil, 0, false
	}
	raw, err := hex.DecodeString(h)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return nil, 0, false
	}
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	return net.IP(raw), int(port), true
}

// Cache holds one Scan for a while, so a session list polled by several Consoles every few
// seconds walks /proc at most once per TTL however many rows and readers there are.
type Cache struct {
	Root string
	TTL  time.Duration

	mu  sync.Mutex
	at  time.Time
	val map[string][]int
}

// Get returns the current scan, rescanning when the held one is older than TTL. Callers
// serialise on the scan itself: a second reader waits for the first's answer rather than
// starting a walk of its own.
func (c *Cache) Get(now time.Time) map[string][]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.val != nil && now.Sub(c.at) < c.TTL {
		return c.val
	}
	c.val = Scan(c.Root)
	c.at = now
	return c.val
}
