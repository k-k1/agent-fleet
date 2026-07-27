package mcpreg

// Builtin ops integrations (docs/25 Phase 1), normalized into ServerDef so the
// registry is ONE list instead of "builtin catalog or registered server" branching
// everywhere (ADR0031 決定 6).
//
// They stay code-defined and not editable: each is launched as
// `workspace-agent mcp-run <id>`, a wrapper that injects the user's stored key as
// env at spawn — so the credential never reaches an MCP config file. That indirection
// is the whole point of the builtins and is not something a user-registered stdio
// definition can express.

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

const (
	BuiltinPagerDuty  = "pagerduty"
	BuiltinGrafana    = "grafana"
	BuiltinCloudWatch = "cloudwatch"
)

// builtinSpec is the code half of a builtin: the mcp-run subcommand and the check
// for "has the user connected the credential this needs".
type builtinSpec struct {
	label   string
	runArgs []string
	ready   func(*secrets.Data) bool
}

var builtinSpecs = map[string]builtinSpec{
	BuiltinPagerDuty: {
		label:   "PagerDuty",
		runArgs: []string{"mcp-run", "pagerduty"},
		ready:   func(s *secrets.Data) bool { return s.PagerDuty != nil && s.PagerDuty.APIKey != "" },
	},
	BuiltinGrafana: {
		label:   "Grafana",
		runArgs: []string{"mcp-run", "grafana"},
		ready: func(s *secrets.Data) bool {
			return s.Grafana != nil && s.Grafana.URL != "" && s.Grafana.Token != ""
		},
	},
	BuiltinCloudWatch: {
		label:   "CloudWatch",
		runArgs: []string{"mcp-run", "cloudwatch"},
		ready:   func(s *secrets.Data) bool { return s.CloudWatch != nil && s.CloudWatch.Profile != "" },
	},
}

// IsBuiltin reports whether id names a builtin integration.
func IsBuiltin(id string) bool {
	_, ok := builtinSpecs[id]
	return ok
}

// BuiltinRunArgs returns the subcommand args that launch a builtin's MCP server.
// Callers supply the executable themselves because it is not always this binary's
// own path: agy's chat runs from an isolated HOME and needs its own resolved exe.
func BuiltinRunArgs(id string) ([]string, bool) {
	spec, ok := builtinSpecs[id]
	if !ok {
		return nil, false
	}
	return append([]string(nil), spec.runArgs...), true
}

// BuiltinReady reports whether the user has configured the credential the builtin
// needs. An unconfigured builtin is simply absent from the effective registry, so an
// assistant holding it just has no ops tools (the pre-registry behavior).
func BuiltinReady(id string, s *secrets.Data) bool {
	spec, ok := builtinSpecs[id]
	if !ok || s == nil {
		return false
	}
	return spec.ready(s)
}

// builtinDefs returns the builtins the user has actually connected, as ServerDefs.
// Targets is Assistant-only on purpose: these were assistant integrations before the
// registry existed, and quietly attaching them to every interactive session would be
// a behavior change nobody asked for. Session use goes through a user registration.
func builtinDefs(s *secrets.Data) []ServerDef {
	var out []ServerDef
	for _, id := range []string{BuiltinPagerDuty, BuiltinGrafana, BuiltinCloudWatch} {
		spec := builtinSpecs[id]
		if !spec.ready(s) {
			continue
		}
		out = append(out, ServerDef{
			ID:        id,
			Name:      id,
			Label:     spec.label,
			Origin:    OriginBuiltin,
			Transport: TransportStdio,
			Command:   paths.ExePath(),
			Args:      append([]string(nil), spec.runArgs...),
			Enabled:   true,
			Targets:   Targets{Assistant: true},
		})
	}
	return out
}
