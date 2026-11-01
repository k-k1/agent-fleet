// Package shell is the shell kind: a login shell in the session's directory, no agent CLI.
package shell

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// Agent is the shell kind's agents.Agent.
type Agent struct{ agents.NoGenericTranscript }

// New returns the shell kind's Agent.
func New() Agent { return Agent{} }

func (Agent) Kind() string      { return session.KindShell }
func (Agent) Caps() agents.Caps { return agents.Caps{MCPClient: agents.TriNo} }

func (Agent) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	// A shell falls back to home if its recorded dir is gone.
	cwd := m.CWD()
	if !session.DirExists(cwd) {
		cwd = paths.HomeDir()
	}
	return agents.LaunchPlan{Program: "bash -l", Cwd: cwd}, nil
}

func (Agent) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	return agents.LiveInfo{Resumable: true}
}

func (Agent) ClearResume(string) {}

// UsageMeasured is agents.UsageMeasurer: usagex.MeasuredNone: runs no agent CLI, so nothing reports tokens.
func (Agent) UsageMeasured() string { return usagex.MeasuredNone }

// NoHeadlessChat is agents.NoHeadlessChat: chatx has no provider for this kind.
func (Agent) NoHeadlessChat() {}
