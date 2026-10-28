package acp

import (
	"encoding/json"
	"os/exec"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// The ForTest methods give the kind packages' tests what the shared handle keeps unexported.
// No production code calls them.

// AttachForTest connects the handle to cl as a running session sid, wired as Spawn wires a new
// child, without starting one.
func (h *Handle) AttachForTest(cl *Client, sid string) {
	cl.OnRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(cl, id, method, params)
	}
	if h.hooks.OnNotify != nil {
		cl.OnNotify = h.hooks.OnNotify
	}
	h.mu.Lock()
	h.cl, h.sid, h.alive, h.state = cl, sid, true, agents.TurnCompleted
	h.mu.Unlock()
}

// IdleForTest reports that no pump runs and no turn is in flight.
func (h *Handle) IdleForTest() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.pumping && !h.running
}

// ClientForTest is the current client.
func (h *Handle) ClientForTest() *Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cl
}

// SidForTest is the runtime's session id.
func (h *Handle) SidForTest() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sid
}

// SetChildForTest makes cmd the handle's child, for the progress probe.
func (h *Handle) SetChildForTest(cmd *exec.Cmd) {
	h.mu.Lock()
	h.cmd = cmd
	h.mu.Unlock()
}

// ProgressForTest is the handle's activity stamp and open-call set.
func (h *Handle) ProgressForTest() (*agents.Activity, *agents.InFlight) { return &h.act, &h.calls }
