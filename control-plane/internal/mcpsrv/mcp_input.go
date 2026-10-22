package mcpsrv

import (
	"context"
	"encoding/json"
	"errors"
)

// mcpInputBody is the body of send_to_session's POST to the Agent's /mcp-input (#2542).
//
// The origin is read from the authenticated principal that dispatchTraced put on ctx, never from
// the tool's arguments, and a call with no principal on ctx is refused: the Agent wraps the
// prompt in an envelope naming the connection, and a send that could drop it would be
// indistinguishable from the member's own typing. The route itself is the other half: the Agent's
// /input, which the Console reaches through the CP, never builds this envelope.
//
// connection is the OAuth grant id or PAT id, via the name the member gave the connection, and
// impl the client's own clientInfo name, which is informational. The prompt text is not logged
// anywhere on this side.
func mcpInputBody(ctx context.Context, prompt string) ([]byte, error) {
	ci := mcpCallFrom(ctx)
	if ci.patID == "" {
		return nil, errors.New("send_to_session needs an authenticated connection")
	}
	return json.Marshal(map[string]string{
		"prompt":     prompt,
		"connection": ci.patID,
		"via":        ci.principalName,
		"impl":       ci.impl,
	})
}
