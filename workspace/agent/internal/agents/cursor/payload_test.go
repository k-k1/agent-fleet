package cursor

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// payload is what the messages payload gets from the test handle, through the path the
// read layer takes.
func payload(t *testing.T, h *threadHandle) agents.TranscriptData {
	t.Helper()
	handlesMu.Lock()
	handles[h.name] = h
	handlesMu.Unlock()
	defer func() {
		handlesMu.Lock()
		delete(handles, h.name)
		handlesMu.Unlock()
	}()
	return managedTranscript(session.Meta{Name: h.name, Driver: session.DriverManaged})
}
