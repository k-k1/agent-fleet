package harness

import (
	"encoding/json"
	"strings"
)

// decodeArgs unmarshals a ToolCall's raw Arguments string into v. An empty/blank
// body is treated as "no arguments" (some chat templates omit an empty {} object)
// rather than an error — a tool whose schema requires a field then reports that
// itself once it sees the zero value, with a message specific to what is missing.
func decodeArgs(argsJSON string, v any) error {
	s := strings.TrimSpace(argsJSON)
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), v)
}
