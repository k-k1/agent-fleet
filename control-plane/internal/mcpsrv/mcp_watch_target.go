package mcpsrv

import "encoding/json"

// SetsWatchTarget reports whether a member's MCP registration body marks the server as a watch
// source (targets.watch, ADR 0133 §8). A body that does not parse, or has no such flag, does not.
func SetsWatchTarget(body []byte) bool {
	var in struct {
		Targets struct {
			Watch bool `json:"watch"`
		} `json:"targets"`
	}
	return json.Unmarshal(body, &in) == nil && in.Targets.Watch
}
