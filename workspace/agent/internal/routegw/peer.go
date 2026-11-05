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

// procID is one process, not one pid: a pid can be reused by a later process, the start time
// (clock ticks since boot) tells the two apart.
type procID struct {
	pid   int
	start uint64
}

// peer is the process at the other end of one connection, pinned when the connection was
// accepted. A keep-alive connection outlives requests, and its connector may exit while the
// connection lives on in a process it handed the descriptor to; the pid alone could then name
// a later process of the owner's tree.
type peer struct {
	id procID
	ok bool
}

// connContext records the connection's peer process for every request it carries.
func connContext(ctx context.Context, c net.Conn) context.Context {
	id, ok := peerOf(c)
	return context.WithValue(ctx, peerKey{}, peer{id: id, ok: ok})
}

// requestPeer is the calling process of r; ok is false when the kernel did not say (a TCP
// connection, a platform without SO_PEERCRED), and such a caller is refused.
func requestPeer(r *http.Request) (procID, bool) {
	p, _ := r.Context().Value(peerKey{}).(peer)
	return p.id, p.ok && p.id.pid > 1
}

// maxTreeDepth bounds the walk up the process tree: a real chain from a pane to a model call is
// a handful of processes, and a loop in a corrupt /proc must not hang a call.
const maxTreeDepth = 256

// readProc reads pid's stat from procRoot. ok is false when it cannot be read or parsed, and
// when the process is a zombie or dead: such a process runs nothing and its pid is about to be
// free for reuse.
func readProc(procRoot string, pid int) (procx.Info, bool) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return procx.Info{}, false
	}
	info, ok := procx.Parse(pid, string(b))
	if !ok || info.State == 'Z' || info.State == 'X' || info.State == 'x' {
		return procx.Info{}, false
	}
	return info, true
}

// live reports whether id is still that process, alive.
func live(procRoot string, id procID) bool {
	info, ok := readProc(procRoot, id.pid)
	return ok && info.StartTicks == id.start
}

// inTree reports whether the process id is, alive and the same process it was when pinned, one
// of roots or a descendant of one, reading /proc under procRoot. Every process on the chain,
// the root included, must be readable and alive, and each parent must have started no later
// than its child: a parent pid reused by a later process cannot have, so a chain that steps
// onto one is refused. The caller is checked again after the walk, so it cannot have exited
// and been replaced meanwhile. When in doubt the call is refused; a process that double-forked
// away from its pane is refused too, which is the safe direction.
func inTree(procRoot string, id procID, roots []int) bool {
	if len(roots) == 0 {
		return false
	}
	pid := id.pid
	info, ok := readProc(procRoot, pid)
	if !ok || info.StartTicks != id.start {
		return false
	}
	found := false
	// pid > 1 also keeps init out of roots: everything descends from it.
	for steps := 0; steps < maxTreeDepth && pid > 1; steps++ {
		if slices.Contains(roots, pid) {
			found = true
			break
		}
		parent, ok := readProc(procRoot, info.PPID)
		if !ok || info.PPID == pid || parent.StartTicks > info.StartTicks {
			return false
		}
		pid, info = info.PPID, parent
	}
	return found && live(procRoot, id)
}

// liveRoots is roots without the processes that are gone, zombies or unreadable: a pane that
// tmux still lists after its process ended (remain-on-exit) must not keep a token alive.
func liveRoots(procRoot string, roots []int) []int {
	var out []int
	for _, r := range roots {
		if r > 1 {
			if _, ok := readProc(procRoot, r); ok {
				out = append(out, r)
			}
		}
	}
	return out
}
