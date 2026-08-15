package codex

// Turn-level failures the codex managed driver (app-server) reports. Unlike opencode
// (errors.go there), codex's app-server DOES expose a structured reason — it was just
// never read. `turn/completed`'s Turn carries `error: TurnError{message,
// additionalDetails, codexErrorInfo}` when status is "failed" (verified against the real
// v0.146.0 app-server protocol schema, `codex app-server generate-json-schema`).
// codexErrorInfo is a Rust-style tagged union: either a bare string enum
// ("usageLimitExceeded", "contextWindowExceeded", "serverOverloaded", …) or a
// single-key object for the variants that carry an httpStatusCode
// ({"httpConnectionFailed":{"httpStatusCode":429}} and friends).
//
// A second failure shape exists: a turn can be rejected synchronously at `turn/start`
// (a JSON-RPC error response, no Turn ever created — the usage-limit-exhausted case
// observed in the field never wrote anything to the rollout, not even the user's own
// prompt) instead of completing with status "failed". Both shapes funnel into the same
// codexError so the operator report / chat bridge / Console error block render them
// identically regardless of which one codex used.

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// turnErrorWire is TurnError from the app-server v2 schema.
type turnErrorWire struct {
	Message           string          `json:"message"`
	AdditionalDetails *string         `json:"additionalDetails"`
	CodexErrorInfo    json.RawMessage `json:"codexErrorInfo"`
}

// codexError is the normalized failure detail, mirroring opencode's messageError shape
// (label + detail) so both agent kinds render the same way downstream.
type codexError struct {
	message string
	label   string // codexErrorInfo variant name, e.g. "usageLimitExceeded"; "" if absent
	status  int    // httpStatusCode, when the variant carries one
}

// decodeCodexError parses a TurnError payload (turn.error's raw bytes). ok is false when
// raw is empty/null or carries no message (nothing worth surfacing).
func decodeCodexError(raw []byte) (codexError, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return codexError{}, false
	}
	var w turnErrorWire
	if json.Unmarshal(raw, &w) != nil || strings.TrimSpace(w.Message) == "" {
		return codexError{}, false
	}
	e := codexError{message: w.Message}
	if w.AdditionalDetails != nil {
		if d := strings.TrimSpace(*w.AdditionalDetails); d != "" {
			e.message = e.message + " (" + d + ")"
		}
	}
	e.label, e.status = decodeCodexErrorInfo(w.CodexErrorInfo)
	return e, true
}

// decodeCodexErrorInfo reads CodexErrorInfo's two wire shapes: a bare string enum, or a
// single-key object ({"variant":{"httpStatusCode":...}}) for the variants that carry one.
func decodeCodexErrorInfo(raw json.RawMessage) (label string, status int) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, 0
	}
	var obj map[string]struct {
		HTTPStatusCode int `json:"httpStatusCode"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		for k, v := range obj {
			return k, v.HTTPStatusCode
		}
	}
	return "", 0
}

// codexErrorFromRPC recovers a codexError from a rejected turn/start call. call()
// (appclient.go) returns an *rpcError when the JSON-RPC response carried a decodable
// error object; its Data is tried as a TurnError first (codex may echo the same shape
// there), else the bare message stands alone.
func codexErrorFromRPC(err error) (codexError, bool) {
	var re *rpcError
	if !errors.As(err, &re) || strings.TrimSpace(re.Message) == "" {
		return codexError{}, false
	}
	if ce, ok := decodeCodexError(re.Data); ok {
		return ce, true
	}
	return codexError{message: re.Message}, true
}

func (e codexError) labelText() string {
	if e.status > 0 {
		return e.label + " (HTTP " + strconv.Itoa(e.status) + ")"
	}
	return e.label
}

// summary is the one-line form used where a turn is flattened to text: the operator's
// report / the chat-bridge body / the Agent log.
func (e codexError) summary() string {
	if e.label == "" {
		return "[error] " + e.message
	}
	return "[error] " + e.labelText() + ": " + e.message
}

// part renders the failure as the ordered part the Console renders as a distinct error
// block (mirror-error / ErrorBlock — same Kind opencode's errors.go targets).
func (e codexError) part() transcript.Part {
	label := e.labelText()
	if label == "" {
		label = "error"
	}
	return transcript.Part{Kind: "error", Info: label, Text: e.message}
}

func (e codexError) retryable() bool {
	if e.status >= 500 && e.status <= 599 {
		return true
	}
	switch e.label {
	case "serverOverloaded", "httpConnectionFailed", "responseStreamFailed":
		return true
	default:
		return false
	}
}
