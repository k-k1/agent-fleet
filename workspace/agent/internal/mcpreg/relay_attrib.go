package mcpreg

import (
	"net"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/listenports"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// CallerPID finds the process holding the CLI's end of a relayed connection: local is the
// relay's end, remote the CLI's. main sets it, because the lookup (routegw) lives in a package
// that imports this one; nil attributes nothing.
var CallerPID func(local, remote *net.TCPAddr) (int, bool)

// procRoot is where callerSession reads a process's environment; a test points it elsewhere.
var procRoot = "/proc"

// callerSession attributes r's call to the session named in its CLI's environment
// (AF_SESSION_NAME, which every launch route hands down), "" when it cannot be told. The shared
// managed daemons (codex app-server, opencode serve) serve every session and name none, so their
// calls stay unattributed.
//
// The name is a label for where a held call is shown, not a decision: every session runs as the
// same user and can put any name in its environment. Whether a call may be answered is decided
// by the member's signed request alone (the answer route requires the member-origin mark).
func callerSession(r *http.Request) string {
	if CallerPID == nil || r == nil {
		return ""
	}
	local, _ := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	remote, err := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if local == nil || err != nil {
		return ""
	}
	pid, ok := CallerPID(local, remote)
	if !ok {
		return ""
	}
	if name := listenports.SessionOfPID(procRoot, pid); session.ValidName(name) {
		return name
	}
	return ""
}
