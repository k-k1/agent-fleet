// The plain CP→Agent HTTP helper and its error type. Both stay in package main for
// visibility, not ownership: agentHTTPError's status / body are unexported fields, and
// memo.go reads he.status / he.body after picking the error up with
// `errors.As(err, &he)`. Moving the type to another package leaves that errors.As
// compiling but permanently false, which silently kills the fallback for an old Agent
// with no /turn (agentEndpointMissing) and the forwarding of the Agent's stable error
// codes (memoAgentError). agentText is the only place that produces the type, so it
// stays with it; mcpsrv reaches it through CP.AgentText.
//
// Callers are memo.go, session_share.go, claude_audit.go and mcpsrv — this is not
// MCP-specific, and it belongs next to agent_client.go.

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// agentText performs an authenticated CP→Agent request and returns the body as
// text (the Agent already returns JSON; we pass it through to the model).
func agentText(ctx context.Context, rt runtime.Runtime, method, path string, body []byte) (string, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rt.Endpoint()+path, r)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("workspace agent unreachable (is the workspace running?)")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", &agentHTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(b))}
	}
	return string(b), nil
}

// agentHTTPError keeps the Agent's status/body inspectable by callers that need
// protocol-level fallback or want to preserve a stable Agent error code. Error()
// intentionally retains agentText's former text so existing MCP responses do not
// change merely because the error became typed.
type agentHTTPError struct {
	status int
	body   string
}

func (e *agentHTTPError) Error() string {
	return fmt.Sprintf("agent %d: %s", e.status, e.body)
}
