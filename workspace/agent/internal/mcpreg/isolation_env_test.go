package mcpreg

import (
	"slices"
	"strings"
	"testing"
)

// While the Agent withholds the workload role, every stdio MCP child has to keep
// AWS_EC2_METADATA_DISABLED, a member's own server included: codex and cursor rebuild the
// child's environment from an allow-list, and an AWS SDK server with no profile would
// otherwise reach the slot's instance profile wherever the network does not block IMDS.
func TestIsolationSentinelReachesEveryStdioServer(t *testing.T) {
	member := ServerDef{Name: "wiki", Transport: TransportStdio, Command: "npx", Args: []string{"-y", "wiki-mcp"}}
	builtin := ServerDef{Name: "pagerduty", ID: "pagerduty", Origin: OriginBuiltin, Transport: TransportStdio, Command: "/usr/bin/workspace-agent"}
	const name = "AWS_EC2_METADATA_DISABLED"

	t.Run("isolated", func(t *testing.T) {
		t.Setenv(name, "true")
		for _, d := range []ServerDef{member, builtin} {
			if !slices.Contains(ForwardEnvNames(d), name) {
				t.Errorf("ForwardEnvNames(%s) = %v", d.Name, ForwardEnvNames(d))
			}
			if got := cursorStdioEnv(d)[name]; got != "${env:"+name+"}" {
				t.Errorf("cursor env for %s: %s = %v", d.Name, name, got)
			}
		}
		args, _ := CodexOverrides([]ServerDef{member}, CodexOpts{})
		if !strings.Contains(strings.Join(args, " "), `mcp_servers.wiki.env_vars=["`+name+`"]`) {
			t.Errorf("codex overrides do not forward %s: %v", name, args)
		}
		if toml := strings.Join(codexServerBlocks([]ServerDef{member}), "\n"); !strings.Contains(toml, name) {
			t.Errorf("codex config.toml does not forward %s:\n%s", name, toml)
		}
	})

	// Native, or a deployment that opted back in: the Agent has no such variable and
	// nothing new is forwarded.
	t.Run("not isolated", func(t *testing.T) {
		if got := ForwardEnvNames(member); len(got) != 0 {
			t.Errorf("ForwardEnvNames(member) = %v", got)
		}
		if _, ok := cursorStdioEnv(member)[name]; ok {
			t.Errorf("cursor env carries %s without isolation", name)
		}
	})
}
