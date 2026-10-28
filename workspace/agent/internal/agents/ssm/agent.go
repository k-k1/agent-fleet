// Package ssm is the ssm kind: an AWS Session Manager shell on a remote instance, and the
// per-session aws config and pane program it launches with.
package ssm

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Agent is the ssm kind's agents.Agent.
type Agent struct{ agents.NoGenericTranscript }

// New returns the ssm kind's Agent.
func New() Agent { return Agent{} }

func (Agent) Kind() string      { return session.KindSSM }
func (Agent) Caps() agents.Caps { return agents.Caps{MCPClient: agents.TriNo} }

func (Agent) BuildLaunch(m session.Meta, opts agents.LaunchOpts) (agents.LaunchPlan, error) {
	// An SSM session logs into the operator's OWN AWS via `aws sso login` (the
	// device-code URL is surfaced in this terminal — click it to authenticate in
	// another tab) then opens Session Manager on the target instance. No AWS
	// credentials pass through Agent Fleet: the aws CLI authenticates directly and
	// caches the short-lived token in the home volume. Launch dir is home (the work
	// happens on the remote instance).
	if m.SSM == nil || m.SSM.Target == "" {
		return agents.LaunchPlan{}, agents.ErrSSMNoTarget
	}
	p, err := buildSSMProgram(m.Name, *m.SSM, opts.SSMForce)
	if err != nil {
		return agents.LaunchPlan{}, err
	}
	return agents.LaunchPlan{Program: p, Cwd: paths.HomeDir()}, nil
}

func (Agent) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	return agents.LiveInfo{Resumable: true}
}

func (Agent) ClearResume(string) {}
