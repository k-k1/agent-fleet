package msp

import (
	"fmt"
	"time"
)

// ClientName identifies Agent Fleet on the wire. The schema constrains it to `[a-z0-9_]+`,
// so this is not a display string; Title carries the readable one.
const ClientName = "agent_fleet"

// handshakeTimeout bounds `initialize` alone. Every other call waits for as long as the turn
// takes, but a host that does not answer its own handshake is wedged, and the launch path
// needs to say so rather than hang a session in starting.
const handshakeTimeout = 30 * time.Second

// Handshake runs `initialize` then the `initialized` notification, which is the sequence MSP
// requires before any other method (a call before it is refused `notInitialized`).
//
// want names the capabilities to request. A capability the host does not grant simply does
// not appear in the result, so the caller checks Granted rather than assuming: measured,
// sessionMcp is granted and is what carries per-session MCP servers (ADR 0095 decision 11).
func Handshake(c *Client, version string, want []CapabilityName) (*InitializeResult, error) {
	title := "Agent Fleet"
	requested := make([]string, 0, len(want))
	for _, w := range want {
		requested = append(requested, string(w))
	}
	params := InitializeParams{
		ClientInfo: ClientInfo{Name: ClientName, Title: &title, Version: version},
		Capabilities: &ClientCapabilities{
			RequestedCapabilities: requested,
		},
	}
	var res InitializeResult
	if err := c.CallInto(MethodInitialize, params, handshakeTimeout, &res); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := c.Notify(NotificationInitialized, map[string]any{}); err != nil {
		return nil, fmt.Errorf("initialized: %w", err)
	}
	return &res, nil
}

// SchemaDrift reports how the host's own schema fingerprint differs from the one these types
// were generated from, or "" when they agree.
//
// The schema calls a mismatch "a warning condition, not an error", and that is the right
// posture at runtime: refusing to launch a session because a patch release re-rendered the
// bundle would be worse than decoding the parts that did not move. The build-time lock is
// fingerprint_test.go; this is the runtime breadcrumb that explains a later decode failure.
func SchemaDrift(res *InitializeResult) string {
	if res == nil || res.Schema.Fingerprint == SchemaFingerprint {
		return ""
	}
	return fmt.Sprintf("host schema %s, types generated from %s",
		res.Schema.Fingerprint, SchemaFingerprint)
}

// Granted reports whether the host granted a capability. The handshake result is fixed for
// the connection's lifetime, so this is asked once and remembered.
func Granted(res *InitializeResult, name CapabilityName) bool {
	if res == nil {
		return false
	}
	for _, g := range res.GrantedCapabilities {
		if g == name {
			return true
		}
	}
	return false
}
