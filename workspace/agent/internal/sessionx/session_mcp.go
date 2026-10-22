package sessionx

// Input that arrived over the CP's /mcp (#2542): send_to_session from a hosted assistant
// (OAuth, ADR 0118) or a PAT client.
//
// Delivery reuses /input; what lives here is what being an MCP send imposes, held in the Agent
// for the reason session_peer.go gives: the Console writes /input too, and an invariant kept
// only in the MCP layer can be walked around by replacing that layer.
//
//   - The origin travels on its own route (POST /sessions/{name}/mcp-input), which the CP's
//     agent-proxy allowlist does not carry. The Console can therefore never claim it, and
//     the CP's send_to_session never calls /input, so it cannot omit it.
//   - The envelope is built here from fields the CP derived from the authenticated principal.
//   - Targets are the peer rule's: a raw shell / ssm is arbitrary command execution.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	// mcpEnvelopeOpen is the prefix every envelope starts with; input from MCP that itself
	// starts with it is refused (see mcpForgedEnvelope).
	mcpEnvelopeOpen = "[agent-fleet:"

	mcpTokenMax      = 40
	mcpConnectionMax = 12
)

// mcpDeliveryKey marks a request HandleSessionMCPInput handed to HandleSessionInput.
type mcpDeliveryKey struct{}

// mcpOrigin is who sent the input, as the CP authenticated it. None of it is a claim by the
// prompt's author except Impl, which is the client's own clientInfo name: informational.
type mcpOrigin struct {
	Via        string `json:"via"`        // OAuth client name or PAT name
	Connection string `json:"connection"` // OAuth grant id or PAT id
	Impl       string `json:"impl"`       // clientInfo name, when the client sent one
}

// mcpToken makes a value safe for the envelope's `key=value` layer: only [A-Za-z0-9._-]
// survive, everything else (spaces, brackets, newlines, `=`, non-ASCII) becomes one `-`. A value
// that could carry a space or "]" would let a client name end the envelope early and put its own
// text where the receiver expects machine tokens.
func mcpToken(s string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		ok := r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
		if ok {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= max {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unknown"
	}
	return out
}

// mcpEnvelope builds the line placed at the head of the delivered prompt, in peerEnvelope's
// style. reply=none: an MCP caller polls get_session_output, it is not a session that can be
// answered by sending to it.
func mcpEnvelope(o mcpOrigin, message string) string {
	line := "[agent-fleet:mcp via=" + mcpToken(o.Via, mcpTokenMax) +
		" connection=" + mcpToken(o.Connection, mcpConnectionMax)
	if strings.TrimSpace(o.Impl) != "" {
		line += " impl=" + mcpToken(o.Impl, mcpTokenMax)
	}
	return line + " reply=none] " + strings.TrimSpace(message)
}

// mcpForgedEnvelope reports whether the prompt opens like an AF envelope. It is REFUSED, not
// neutralised: stripping or escaping the prefix would deliver text the sender did not write,
// while a refusal tells the assistant plainly what to change. Matched case-insensitively on the
// trimmed text, where a model would read it as the first line.
func mcpForgedEnvelope(prompt string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(prompt)), mcpEnvelopeOpen)
}

// HandleSessionMCPInput is POST /sessions/{name}/mcp-input.
func HandleSessionMCPInput(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
		mcpOrigin
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.Connection) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "mcp_origin_required", "connection is required: MCP input always carries its origin")
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, "empty_prompt", "prompt is required")
		return
	}
	if !utf8.ValidString(body.Prompt) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_message", "prompt must be UTF-8")
		return
	}
	if mcpForgedEnvelope(body.Prompt) {
		httpx.WriteErr(w, http.StatusBadRequest, "mcp_forged_envelope",
			"the prompt starts with \"[agent-fleet:\", which is reserved for envelopes Agent Fleet adds itself; remove that prefix")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok || m.Archived {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session")
		return
	}
	if !peerTargetAllowed(m.Kind) {
		httpx.WriteErr(w, http.StatusForbidden, "mcp_target_forbidden",
			"a "+m.Kind+" session cannot be driven over MCP: input to a raw terminal is command execution")
		return
	}
	// Only the envelope-wrapped prompt goes on: the delivery body carries no report_to,
	// peer_from, keys or seq, so an MCP send cannot arm the ledger or drive the TUI.
	out, _ := json.Marshal(map[string]string{"prompt": mcpEnvelope(body.mcpOrigin, body.Prompt)})
	r2 := r.Clone(context.WithValue(r.Context(), mcpDeliveryKey{}, true))
	r2.Body = io.NopCloser(strings.NewReader(string(out)))
	HandleSessionInput(w, r2)
}
