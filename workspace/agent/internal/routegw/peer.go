package routegw

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
)

// Caller binding (ADR 0128 decision 5). Every session of a Workspace runs as the same user, so
// a route token read from another session's process environment would otherwise work as well
// as the owner's. The gateway therefore listens on a Unix socket only, reads the connecting
// process from the kernel (SO_PEERCRED, taken at connect time and not forgeable by the
// caller), and serves a token only to a process that is the owning session's launch-pane
// process or descends from it.
//
// Measured (claude 2.1.318): with CLAUDE_CODE_USE_BEDROCK=1, ANTHROPIC_UNIX_SOCKET carries the
// Bedrock model calls over the socket, the base URL's host and port are never dialled, and the
// peer credentials name the claude process itself.

type peerKey struct{}

// peer is the process at the other end of one connection.
type peer struct {
	pid int
	ok  bool
}

// connContext records the connection's peer process for every request it carries.
func connContext(ctx context.Context, c net.Conn) context.Context {
	pid, ok := peerPID(c)
	return context.WithValue(ctx, peerKey{}, peer{pid: pid, ok: ok})
}

// peerOf is the calling process of r; ok is false when the kernel did not say (a TCP
// connection, a platform without SO_PEERCRED), and such a caller is refused.
func peerOf(r *http.Request) (int, bool) {
	p, _ := r.Context().Value(peerKey{}).(peer)
	return p.pid, p.ok && p.pid > 1
}

// maxTreeDepth bounds the walk up the process tree: a real chain from a pane to a model call is
// a handful of processes, and a loop in a corrupt /proc must not hang a call.
const maxTreeDepth = 256

// inTree reports whether pid is one of roots or descends from one, reading parents from procRoot
// (/proc). A process that cannot be read, or a chain that reaches init first, is outside:
// when in doubt the call is refused. A process that double-forked away from its pane is
// therefore refused too, which is the safe direction.
func inTree(procRoot string, pid int, roots []int) bool {
	// pid > 1 also keeps init out of roots: everything descends from it.
	for steps := 0; steps < maxTreeDepth && pid > 1; steps++ {
		if slices.Contains(roots, pid) {
			return true
		}
		b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
		if err != nil {
			return false
		}
		info, ok := procx.Parse(pid, string(b))
		if !ok || info.PPID == pid {
			return false
		}
		pid = info.PPID
	}
	return false
}
