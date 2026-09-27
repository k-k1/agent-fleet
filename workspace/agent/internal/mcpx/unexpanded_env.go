package mcpx

import (
	"os"
	"strings"
)

// dropUnexpandedEnv unsets every variable whose value is still the reference mcpreg wrote for
// it, `${env:<its own name>}`.
//
// cursor scrubs its MCP children's environment, so its config file carries references that
// cursor expands from its own process environment (mcpreg.cursorStdioEnv) — and an unset one
// is passed through as the literal text (measured). Read as a value, `${env:AGENT_ADDR}` is an
// address nothing listens on and `${env:AF_SESSION_NAME}` a session name that matches nothing;
// unset, each takes the path it takes when the variable was never there. Only the exact
// self-reference is cleared, so no value anybody chose can be mistaken for one.
func dropUnexpandedEnv() {
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if ok && value == "${env:"+name+"}" {
			_ = os.Unsetenv(name)
		}
	}
}
