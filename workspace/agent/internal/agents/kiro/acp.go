package kiro

import (
	"io"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/acp"
)

// newACPClient connects the JSON-RPC client to the runtime child's stdio (acp.Client).
func newACPClient(stdin io.Writer, stdout io.Reader) *acp.Client {
	return acp.NewClient(acp.Config{Runtime: "kiro", ErrorData: true}, stdin, stdout)
}
