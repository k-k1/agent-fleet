package mcpreg

import (
	"context"
	"net"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/listenports"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// CallerPID finds the process holding the CLI's end of a relayed connection: local is the
// relay's end, remote the CLI's. main sets it, because the lookup (routegw) lives in a package
// that imports this one; nil attributes nothing.
var CallerPID func(local, remote *net.TCPAddr) (int, bool)

// OpenCodeSessionIDMetaKey is OpenCode 2's per-call native session ID in params._meta.
const OpenCodeSessionIDMetaKey = "ai.opencode/sessionID"

// SharedOpenCodeCaller resolves a native ID for the peer PID against the Agent's slot mapping.
// Like CallerPID, it is set by main before the relay starts.
var SharedOpenCodeCaller func(ctx context.Context, pid int, id string) string

// procRoot is where callerSession reads a process's environment; a test points it elsewhere.
var procRoot = "/proc"

// callerSession prefers the CLI's process environment. The shared OpenCode daemon has no
// AF_SESSION_NAME, so its native ID is a lookup key for the Agent's own mapping.
//
// The name is a label for where a held call is shown, not a decision: every session runs as the
// same user and can put any name in its environment. Whether a call may be answered is decided
// by the member's signed request alone (the answer route requires the member-origin mark).
func callerSession(r *http.Request, openCodeID string) string {
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
	if openCodeID != "" && SharedOpenCodeCaller != nil {
		return SharedOpenCodeCaller(r.Context(), pid, openCodeID)
	}
	return ""
}
