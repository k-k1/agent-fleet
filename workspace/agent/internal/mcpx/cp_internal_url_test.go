package mcpx

import (
	"os"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/testguard"
)

// TestMain clears AF_CP_INTERNAL_URL: a workspace that runs these tests may itself carry it,
// and cpurl.Request would then send the tests' requests past their fake CP.
//
// It also points the Agent at a port nothing listens on. The memo tools call the Agent, and
// agentBaseURL falls back to the live Agent's own port, which relays to the real Control Plane:
// a test that forgot its fake Agent must fail to connect, not file a note in the member's queue.
// A test that needs an Agent sets AGENT_ADDR to its own fake.
func TestMain(m *testing.M) {
	os.Exit(testguard.Run(m, func() {
		_ = os.Unsetenv("AF_CP_INTERNAL_URL")
		_ = os.Setenv("AGENT_ADDR", testUnreachableAgentAddr)
	}))
}

// testUnreachableAgentAddr is the discard port on loopback: refused, never answered.
const testUnreachableAgentAddr = "127.0.0.1:9"
