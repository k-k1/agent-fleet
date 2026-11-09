package mcpsrv

import (
	"bytes"
	"encoding/json"
)

// SetsWatchTarget reports whether a member's MCP registration body marks the server as a watch
// source (targets.watch, ADR 0133 §8). It reads the body exactly as the Agent does
// (json.Decoder.Decode: the first JSON value only, whatever follows it), so no body can read as
// "not a source" here and as one there. ok is false when no value could be read; the caller then
// passes the body on, and the Agent refuses it as invalid JSON.
func SetsWatchTarget(body []byte) (watch, ok bool) {
	var in struct {
		Targets struct {
			Watch bool `json:"watch"`
		} `json:"targets"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&in); err != nil {
		return false, false
	}
	return in.Targets.Watch, true
}
